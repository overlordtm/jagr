package enrichment

import (
	"fmt"
	"strings"
)

// EnrichListeners parses netstat output for TCP/UDP listeners and enriches each
// with process binary metadata and deleted-binary detection.
func EnrichListeners(runner Runner) string {
	var b strings.Builder

	// TCP listeners
	tcpOut, _, _, _ := runner.ExecuteTrusted("netstat", []string{"-tlnp"})
	tcpEntries := parseNetstatOutput(tcpOut, "TCP")

	// UDP listeners
	udpOut, _, _, _ := runner.ExecuteTrusted("netstat", []string{"-ulnp"})
	udpEntries := parseNetstatOutput(udpOut, "UDP")

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

// parseNetstatOutput parses `netstat -tlnp` / `-ulnp` output.
// Format: Proto Recv-Q Send-Q Local Foreign [State] PID/Program
func parseNetstatOutput(output, proto string) []ssEntry {
	var entries []ssEntry
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		// Skip header lines
		p := strings.ToLower(fields[0])
		if p != "tcp" && p != "tcp6" && p != "udp" && p != "udp6" {
			continue
		}

		// netstat -tlnp:  Proto Recv-Q Send-Q Local Foreign State PID/Program
		// netstat -ulnp:  Proto Recv-Q Send-Q Local Foreign       PID/Program  (no State col for UDP)
		entry := ssEntry{
			Proto:     proto,
			LocalAddr: fields[3],
			PeerAddr:  fields[4],
		}

		// Find the PID/Program field — it looks like "1234/progname" or "-"
		for _, f := range fields[5:] {
			if strings.Contains(f, "/") {
				parts := strings.SplitN(f, "/", 2)
				if len(parts) == 2 && parts[0] != "-" {
					entry.PID = parts[0]
					entry.Process = parts[1]
				}
				break
			}
		}

		entries = append(entries, entry)
	}

	return entries
}
