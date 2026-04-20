package enrichment

import (
	"fmt"
	"strings"
)

// EnrichPackages detects the system package manager and runs integrity
// verification, returning files that have been modified or are missing
// relative to their package manifests.
func EnrichPackages(runner Runner) string {
	var b strings.Builder

	pm, pmBin := detectPackageManager(runner)
	if pm == "" {
		return "No supported package manager found (dpkg, rpm, pacman).\n"
	}

	b.WriteString(fmt.Sprintf("Package Manager: %s (%s)\n\n", pm, pmBin))

	switch pm {
	case "dpkg":
		b.WriteString(enrichDpkg(runner, pmBin))
	case "rpm":
		b.WriteString(enrichRpm(runner, pmBin))
	case "pacman":
		b.WriteString(enrichPacman(runner, pmBin))
	}

	return b.String()
}

func detectPackageManager(runner Runner) (name, bin string) {
	for _, candidate := range []string{"/usr/bin/dpkg", "/bin/dpkg"} {
		_, _, exitCode, _ := runner.ExecuteTrusted(candidate, []string{"--version"})
		if exitCode == 0 {
			return "dpkg", candidate
		}
	}
	for _, candidate := range []string{"/usr/bin/rpm", "/bin/rpm"} {
		_, _, exitCode, _ := runner.ExecuteTrusted(candidate, []string{"--version"})
		if exitCode == 0 {
			return "rpm", candidate
		}
	}
	_, _, exitCode, _ := runner.ExecuteTrusted("/usr/bin/pacman", []string{"--version"})
	if exitCode == 0 {
		return "pacman", "/usr/bin/pacman"
	}
	return "", ""
}

func enrichDpkg(runner Runner, bin string) string {
	var b strings.Builder

	// List installed packages
	stdout, _, _, _ := runner.ExecuteTrusted(bin, []string{"-l"})
	pkgLines := splitNonEmpty(stdout)
	count := 0
	for _, l := range pkgLines {
		if strings.HasPrefix(l, "ii") {
			count++
		}
	}
	b.WriteString(fmt.Sprintf("Installed packages: %d\n\n", count))

	// Verify package file integrity via dpkg --verify (requires dpkg >= 1.17)
	b.WriteString("=== Modified/Missing Files (dpkg --verify) ===\n")
	stdout, stderr, exitCode, _ := runner.ExecuteTrusted(bin, []string{"--verify"})
	out := strings.TrimSpace(stdout + stderr)
	if exitCode != 0 && out == "" {
		b.WriteString("dpkg --verify not supported or failed with no output.\n")
		// Fall back to debsums if available
		b.WriteString(enrichDebsums(runner))
	} else if out == "" {
		b.WriteString("All package files intact.\n")
	} else {
		b.WriteString(truncateOutput(out, 500))
		b.WriteString("\n")
	}

	return b.String()
}

func enrichDebsums(runner Runner) string {
	var b strings.Builder
	b.WriteString("\n=== Modified/Missing Files (debsums -c) ===\n")
	stdout, stderr, exitCode, _ := runner.ExecuteTrusted("/usr/bin/debsums", []string{"-c"})
	out := strings.TrimSpace(stdout + stderr)
	if exitCode != 0 && strings.Contains(out, "not found") {
		b.WriteString("debsums not installed.\n")
		return b.String()
	}
	if out == "" {
		b.WriteString("All checksums OK.\n")
	} else {
		b.WriteString(truncateOutput(out, 500))
		b.WriteString("\n")
	}
	return b.String()
}

func enrichRpm(runner Runner, bin string) string {
	var b strings.Builder

	// Count installed packages
	stdout, _, _, _ := runner.ExecuteTrusted(bin, []string{"-qa"})
	pkgs := splitNonEmpty(stdout)
	b.WriteString(fmt.Sprintf("Installed packages: %d\n\n", len(pkgs)))

	// Verify all packages — rpm -Va outputs changed files with attribute flags
	b.WriteString("=== Modified/Missing Files (rpm -Va) ===\n")
	stdout, stderr, _, _ := runner.ExecuteTrusted(bin, []string{"-Va"})
	out := strings.TrimSpace(stdout + stderr)
	if out == "" {
		b.WriteString("All package files intact.\n")
	} else {
		b.WriteString(truncateOutput(out, 500))
		b.WriteString("\n")
	}

	return b.String()
}

func enrichPacman(runner Runner, bin string) string {
	var b strings.Builder

	// Count installed packages
	stdout, _, _, _ := runner.ExecuteTrusted(bin, []string{"-Q"})
	pkgs := splitNonEmpty(stdout)
	b.WriteString(fmt.Sprintf("Installed packages: %d\n\n", len(pkgs)))

	// Verify all packages — pacman -Qk outputs missing/modified files
	b.WriteString("=== Modified/Missing Files (pacman -Qkk) ===\n")
	stdout, stderr, _, _ := runner.ExecuteTrusted(bin, []string{"-Qkk"})
	out := strings.TrimSpace(stdout + stderr)
	if out == "" {
		b.WriteString("All package files intact.\n")
	} else {
		// Filter to only show warnings/errors (lines with "warning:" or missing files)
		var issues []string
		for _, line := range splitNonEmpty(out) {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "warning") || strings.Contains(lower, "error") ||
				strings.Contains(lower, "missing") || strings.Contains(lower, "altered") {
				issues = append(issues, line)
			}
		}
		if len(issues) == 0 {
			b.WriteString("All package files intact.\n")
		} else {
			b.WriteString(truncateOutput(strings.Join(issues, "\n"), 500))
			b.WriteString("\n")
		}
	}

	return b.String()
}
