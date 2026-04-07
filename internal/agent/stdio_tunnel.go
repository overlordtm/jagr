package agent

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"

	"go.uber.org/zap"
)

// Wire format for the stdio tunnel:
//
//	[1 byte type][4 bytes stream_id][4 bytes payload_length][payload...]
//
// Types:
//
//	0x01 tunnelOpen  – remote accepted a new inbound TCP connection
//	0x02 tunnelClose – one side closed the stream
//	0x03 tunnelData  – payload bytes for stream_id
const (
	tunnelOpen  uint8 = 0x01
	tunnelClose uint8 = 0x02
	tunnelData  uint8 = 0x03

	// ready sentinel written by the server before entering its accept loop
	tunnelReady uint8 = 0xFF

	tunnelHeaderLen = 9 // 1 + 4 + 4
	tunnelBufSize   = 32 * 1024
)

type tunnelFrame struct {
	typ      uint8
	streamID uint32
	payload  []byte
}

func writeTunnelFrame(w io.Writer, f tunnelFrame) error {
	hdr := [tunnelHeaderLen]byte{}
	hdr[0] = f.typ
	binary.BigEndian.PutUint32(hdr[1:5], f.streamID)
	binary.BigEndian.PutUint32(hdr[5:9], uint32(len(f.payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(f.payload) > 0 {
		_, err := w.Write(f.payload)
		return err
	}
	return nil
}

func readTunnelFrame(r io.Reader) (tunnelFrame, error) {
	var hdr [tunnelHeaderLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return tunnelFrame{}, err
	}
	f := tunnelFrame{
		typ:      hdr[0],
		streamID: binary.BigEndian.Uint32(hdr[1:5]),
	}
	n := binary.BigEndian.Uint32(hdr[5:9])
	if n > 0 {
		f.payload = make([]byte, n)
		if _, err := io.ReadFull(r, f.payload); err != nil {
			return tunnelFrame{}, fmt.Errorf("read payload: %w", err)
		}
	}
	return f, nil
}

// RunStdioTunnelServer listens on listenAddr, accepts TCP connections, and
// multiplexes them over stdout/stdin using the tunnel frame protocol.
// It is intended to run on the remote host (invoked via an SSH exec session).
func RunStdioTunnelServer(listenAddr string, stdin io.Reader, stdout io.Writer) error {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", listenAddr, err)
	}
	defer ln.Close()

	var (
		mu     sync.Mutex
		conns  = make(map[uint32]net.Conn)
		nextID uint32 = 1
	)

	// Serialise all writes to stdout so frames are not interleaved.
	var wmu sync.Mutex
	writeFrame := func(f tunnelFrame) {
		wmu.Lock()
		defer wmu.Unlock()
		writeTunnelFrame(stdout, f) //nolint:errcheck
	}

	// Signal the local side that we are ready.
	wmu.Lock()
	stdout.Write([]byte{tunnelReady}) //nolint:errcheck
	wmu.Unlock()

	// Read loop: route incoming frames from local side to remote connections.
	go func() {
		for {
			f, err := readTunnelFrame(stdin)
			if err != nil {
				return
			}
			mu.Lock()
			conn := conns[f.streamID]
			mu.Unlock()
			switch f.typ {
			case tunnelData:
				if conn != nil {
					conn.Write(f.payload) //nolint:errcheck
				}
			case tunnelClose:
				if conn != nil {
					conn.Close()
					mu.Lock()
					delete(conns, f.streamID)
					mu.Unlock()
				}
			}
		}
	}()

	// Accept loop: forward new connections to the local side.
	for {
		conn, err := ln.Accept()
		if err != nil {
			return nil
		}

		mu.Lock()
		id := nextID
		nextID++
		conns[id] = conn
		mu.Unlock()

		writeFrame(tunnelFrame{typ: tunnelOpen, streamID: id})

		go func(id uint32, c net.Conn) {
			defer func() {
				writeFrame(tunnelFrame{typ: tunnelClose, streamID: id})
				mu.Lock()
				delete(conns, id)
				mu.Unlock()
				c.Close()
			}()
			buf := make([]byte, tunnelBufSize)
			for {
				n, err := c.Read(buf)
				if n > 0 {
					writeFrame(tunnelFrame{typ: tunnelData, streamID: id, payload: buf[:n]})
				}
				if err != nil {
					return
				}
			}
		}(id, conn)
	}
}

// RunStdioTunnelClient reads multiplexed frames from r and proxies each stream
// to targetAddr. It writes frames back on w. It is the local counterpart to
// RunStdioTunnelServer and runs in a goroutine after the exec session starts.
func RunStdioTunnelClient(r io.Reader, w io.Writer, targetAddr string, logger *zap.Logger) {
	var (
		mu    sync.Mutex
		conns = make(map[uint32]net.Conn)
	)

	var wmu sync.Mutex
	writeFrame := func(f tunnelFrame) {
		wmu.Lock()
		defer wmu.Unlock()
		writeTunnelFrame(w, f) //nolint:errcheck
	}

	for {
		f, err := readTunnelFrame(r)
		if err != nil {
			return
		}

		switch f.typ {
		case tunnelOpen:
			conn, err := net.Dial("tcp", targetAddr)
			if err != nil {
				logger.Warn("Stdio tunnel: failed to connect to gateway",
					zap.Uint32("stream_id", f.streamID),
					zap.Error(err))
				writeFrame(tunnelFrame{typ: tunnelClose, streamID: f.streamID})
				continue
			}
			mu.Lock()
			conns[f.streamID] = conn
			mu.Unlock()

			go func(id uint32, c net.Conn) {
				defer func() {
					writeFrame(tunnelFrame{typ: tunnelClose, streamID: id})
					mu.Lock()
					delete(conns, id)
					mu.Unlock()
					c.Close()
				}()
				buf := make([]byte, tunnelBufSize)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						writeFrame(tunnelFrame{typ: tunnelData, streamID: id, payload: buf[:n]})
					}
					if err != nil {
						return
					}
				}
			}(f.streamID, conn)

		case tunnelData:
			mu.Lock()
			conn := conns[f.streamID]
			mu.Unlock()
			if conn != nil {
				conn.Write(f.payload) //nolint:errcheck
			}

		case tunnelClose:
			mu.Lock()
			conn := conns[f.streamID]
			delete(conns, f.streamID)
			mu.Unlock()
			if conn != nil {
				conn.Close()
			}
		}
	}
}
