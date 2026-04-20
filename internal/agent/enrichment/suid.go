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

// getPackageOwner queries the system package manager (dpkg, rpm, or pacman) for
// the package owning path. Absolute paths bypass the CleanRoom's BusyBox PATH.
func getPackageOwner(runner Runner, path string) string {
	// dpkg -S: system dpkg supports file-to-package search; BusyBox does not.
	for _, bin := range []string{"/usr/bin/dpkg", "/bin/dpkg"} {
		stdout, _, exitCode, _ := runner.ExecuteTrusted(bin, []string{"-S", path})
		if exitCode == 0 && strings.Contains(stdout, ": ") {
			return strings.TrimSpace(strings.SplitN(stdout, "\n", 2)[0])
		}
	}
	// rpm -qf: system rpm supports file-to-package query; BusyBox rpm does not.
	for _, bin := range []string{"/usr/bin/rpm", "/bin/rpm"} {
		stdout, _, exitCode, _ := runner.ExecuteTrusted(bin, []string{"-qf", "--queryformat", "%{NAME}-%{VERSION}-%{RELEASE}.%{ARCH}", path})
		if exitCode == 0 && stdout != "" && !strings.Contains(stdout, "not owned") {
			return strings.TrimSpace(stdout)
		}
	}
	// pacman -Qo: Arch Linux package manager.
	stdout, _, exitCode, _ := runner.ExecuteTrusted("/usr/bin/pacman", []string{"-Qo", path})
	if exitCode == 0 && stdout != "" {
		// Output: "/path/to/file is owned by pkgname version"
		if parts := strings.Fields(strings.TrimSpace(stdout)); len(parts) >= 5 {
			return parts[4] + "-" + parts[5]
		}
	}
	return ""
}
