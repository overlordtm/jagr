package enrichment

import (
	"fmt"
	"strings"
)

// EnrichSUID finds all SUID/SGID binaries and files with capabilities,
// enriching each with package ownership, hash, and file type.
func EnrichSUID(runner Runner) string {
	var b strings.Builder

	// Find SUID/SGID binaries
	stdout, _, _, _ := runner.ExecuteTrusted("find", []string{
		"/", "-xdev",
		"(", "-perm", "-4000", "-o", "-perm", "-2000", ")",
		"-type", "f",
		"-ls",
	})

	suidLines := splitNonEmpty(stdout)
	b.WriteString(fmt.Sprintf("SUID/SGID Binaries (%d found):\n\n", len(suidLines)))

	for i, line := range suidLines {
		fields := strings.Fields(line)
		if len(fields) < 11 {
			b.WriteString(fmt.Sprintf("%d. %s\n", i+1, line))
			continue
		}

		// find -ls format: inode blocks perms links user group size month day time path
		perms := fields[2]
		owner := fields[4]
		group := fields[5]
		size := fields[6]
		path := fields[len(fields)-1]

		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, path))
		b.WriteString(fmt.Sprintf("   Perms: %s | Owner: %s:%s | Size: %s\n", perms, owner, group, size))

		// Package ownership
		pkg := getPackageOwner(runner, path)
		if pkg != "" {
			b.WriteString(fmt.Sprintf("   Package: %s\n", pkg))
		}

		// File type
		stdout, _, _, _ := runner.ExecuteTrusted("file", []string{"-b", path})
		if ft := strings.TrimSpace(stdout); ft != "" {
			b.WriteString(fmt.Sprintf("   Type: %s\n", ft))
		}

		// SHA256 hash
		stdout, _, _, _ = runner.ExecuteTrusted("sha256sum", []string{path})
		if hash := strings.Fields(strings.TrimSpace(stdout)); len(hash) > 0 {
			b.WriteString(fmt.Sprintf("   SHA256: %s\n", hash[0]))
		}

		// Mtime
		mtime := getFileMtime(runner, path)
		if mtime != "" {
			b.WriteString(fmt.Sprintf("   Modified: %s\n", mtime))
		}
		b.WriteString("\n")
	}

	// Files with capabilities
	b.WriteString("\n")
	stdout, _, _, _ = runner.ExecuteTrusted("getcap", []string{"-r", "/"})
	capLines := splitNonEmpty(stdout)
	b.WriteString(fmt.Sprintf("Files with Capabilities (%d found):\n\n", len(capLines)))
	for i, line := range capLines {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, line))
		// Extract path (before " =")
		parts := strings.SplitN(line, " ", 2)
		if len(parts) > 0 {
			pkg := getPackageOwner(runner, parts[0])
			if pkg != "" {
				b.WriteString(fmt.Sprintf("   Package: %s\n", pkg))
			}
		}
	}

	return b.String()
}

func getPackageOwner(runner Runner, path string) string {
	// dpkg -S outputs "package: /path" — BusyBox dpkg doesn't support -S,
	// so validate the output contains ": " to avoid false results.
	stdout, _, exitCode, _ := runner.ExecuteTrusted("dpkg", []string{"-S", path})
	if exitCode == 0 && strings.Contains(stdout, ": ") {
		return strings.TrimSpace(strings.SplitN(stdout, "\n", 2)[0])
	}
	// rpm -qf outputs "package-version.arch" — BusyBox rpm doesn't support -qf.
	stdout, _, exitCode, _ = runner.ExecuteTrusted("rpm", []string{"-qf", path})
	if exitCode == 0 && stdout != "" && !strings.Contains(stdout, "not owned") &&
		!strings.Contains(stdout, "BusyBox") && !strings.Contains(stdout, "Usage:") {
		return strings.TrimSpace(stdout)
	}
	return ""
}
