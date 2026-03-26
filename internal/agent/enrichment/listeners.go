package enrichment

import (
	"fmt"
	"strings"
)

// EnrichListeners parses ss output for TCP/UDP listeners and enriches each
// with process binary metadata and deleted-binary detection.
func EnrichListeners(runner Runner) string {
	var b strings.Builder

	// TCP listeners
	tcpOut, _, _, _ := runner.ExecuteTrusted("ss", []string{"-tlnp"})
	tcpEntries := parseSSOutput(tcpOut)

	// UDP listeners
	udpOut, _, _, _ := runner.ExecuteTrusted("ss", []string{"-ulnp"})
	udpEntries := parseSSOutput(udpOut)

	allEntries := append(tcpEntries, udpEntries...)

	b.WriteString(fmt.Sprintf("Network Listeners (%d TCP, %d UDP):\n\n", len(tcpEntries), len(udpEntries)))

	for i, e := range allEntries {
		b.WriteString(fmt.Sprintf("%d. %s %s → %s\n", i+1, e.Proto, e.LocalAddr, e.PeerAddr))

		if e.Process != "" {
			b.WriteString(fmt.Sprintf("   Process: %s (PID: %s)\n", e.Process, e.PID))
		}

		if e.PID != "" {
			// Get binary path from /proc/PID/exe
			exePath := fmt.Sprintf("/proc/%s/exe", e.PID)
			stdout, _, _, _ := runner.ExecuteTrusted("readlink", []string{"-f", exePath})
			binPath := strings.TrimSpace(stdout)

			if binPath != "" {
				b.WriteString(fmt.Sprintf("   Binary: %s", binPath))

				// Check if binary exists (deleted binary detection)
				if strings.Contains(binPath, "(deleted)") {
					b.WriteString(" ** DELETED FROM DISK **")
				} else {
					_, _, exitCode, _ := runner.ExecuteTrusted("test", []string{"-f", binPath})
					if exitCode != 0 {
						b.WriteString(" ** NOT FOUND ON DISK **")
					} else {
						// Package ownership
						pkg := getPackageOwner(runner, binPath)
						if pkg != "" {
							b.WriteString(fmt.Sprintf(" (pkg: %s)", pkg))
						} else {
							b.WriteString(" (NOT from any installed package)")
						}
					}
				}
				b.WriteString("\n")
			}

			// Get command line
			cmdline, _, _, _ := runner.ExecuteTrusted("cat", []string{fmt.Sprintf("/proc/%s/cmdline", e.PID)})
			if cmdline != "" {
				// cmdline uses null bytes as separators
				cmdline = strings.ReplaceAll(cmdline, "\x00", " ")
				b.WriteString(fmt.Sprintf("   Cmdline: %s\n", strings.TrimSpace(cmdline)))
			}
		}

		b.WriteString("\n")
	}

	return b.String()
}

type ssEntry struct {
	Proto     string
	LocalAddr string
	PeerAddr  string
	Process   string
	PID       string
}

func parseSSOutput(output string) []ssEntry {
	var entries []ssEntry
	lines := strings.Split(output, "\n")

	for _, line := range lines[1:] { // skip header
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		// ss -tlnp format: State Recv-Q Send-Q Local:Port Peer:Port Process
		entry := ssEntry{
			LocalAddr: fields[3],
			PeerAddr:  fields[4],
		}

		// Determine protocol from header context
		if strings.Contains(output, "tcp") {
			entry.Proto = "TCP"
		} else {
			entry.Proto = "UDP"
		}

		// Parse process info from the last field if present
		// Format: users:(("process",pid=1234,fd=5))
		for _, f := range fields[5:] {
			if strings.Contains(f, "pid=") {
				entry.Process, entry.PID = parseProcessField(f)
			}
		}

		entries = append(entries, entry)
	}

	return entries
}

func parseProcessField(field string) (string, string) {
	var process, pid string

	// Extract process name from (("name",pid=N,fd=N))
	if idx := strings.Index(field, "((\""); idx >= 0 {
		rest := field[idx+3:]
		if end := strings.Index(rest, "\""); end >= 0 {
			process = rest[:end]
		}
	}

	if idx := strings.Index(field, "pid="); idx >= 0 {
		rest := field[idx+4:]
		if end := strings.IndexAny(rest, ",)"); end >= 0 {
			pid = rest[:end]
		} else {
			pid = rest
		}
	}

	return process, pid
}
