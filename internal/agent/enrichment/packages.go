package enrichment

import (
	"fmt"
	"strings"
)

// RunPackageAction executes a package manager action. It auto-detects the
// system package manager (dpkg, rpm, pacman) unless overridden by the target.
//
// action values:
//
//	list           — list all installed packages
//	verify_all     — verify integrity of every installed package
//	verify_package — verify one package (target = package name)
//	query_file     — which package owns a file (target = absolute path)
//	query_package  — show files/info for one package (target = package name)
func RunPackageAction(runner Runner, action, target string) string {
	pm, pmBin := detectPackageManager(runner)
	if pm == "" {
		return "No supported package manager found (dpkg, rpm, pacman).\n"
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Package Manager: %s (%s)\n\n", pm, pmBin))

	switch action {
	case "list":
		b.WriteString(pkgList(runner, pm, pmBin))
	case "verify_all":
		b.WriteString(pkgVerifyAll(runner, pm, pmBin))
	case "verify_package":
		if target == "" {
			return "verify_package requires a target package name.\n"
		}
		b.WriteString(pkgVerifyOne(runner, pm, pmBin, target))
	case "query_file":
		if target == "" {
			return "query_file requires a target file path.\n"
		}
		b.WriteString(pkgQueryFile(runner, pm, pmBin, target))
	case "query_package":
		if target == "" {
			return "query_package requires a target package name.\n"
		}
		b.WriteString(pkgQueryPackage(runner, pm, pmBin, target))
	default:
		return fmt.Sprintf("Unknown action %q. Valid: list, verify_all, verify_package, query_file, query_package.\n", action)
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

// pkgList lists all installed packages.
func pkgList(runner Runner, pm, bin string) string {
	var args []string
	switch pm {
	case "dpkg":
		args = []string{"--get-selections"}
	case "rpm":
		args = []string{"-qa", "--queryformat", "%{NAME} %{VERSION}-%{RELEASE} %{ARCH}\n"}
	case "pacman":
		args = []string{"-Q"}
	}
	stdout, stderr, _, _ := runner.ExecuteTrusted(bin, args)
	out := strings.TrimSpace(stdout + stderr)
	return truncateOutput(out, 500) + "\n"
}

// pkgVerifyAll verifies all installed packages.
func pkgVerifyAll(runner Runner, pm, bin string) string {
	var b strings.Builder

	switch pm {
	case "dpkg":
		b.WriteString("=== dpkg --verify (modified conffiles and binaries) ===\n")
		stdout, stderr, exitCode, _ := runner.ExecuteTrusted(bin, []string{"--verify"})
		out := strings.TrimSpace(stdout + stderr)
		if exitCode != 0 && out == "" {
			b.WriteString("dpkg --verify not supported; trying debsums.\n")
			b.WriteString(debsumsVerifyAll(runner))
		} else if out == "" {
			b.WriteString("All package files intact.\n")
		} else {
			b.WriteString(truncateOutput(out, 500))
			b.WriteString("\n")
		}

	case "rpm":
		b.WriteString("=== rpm -Va (S=size M=mode 5=md5 L=symlink D=dev U=user G=group T=mtime) ===\n")
		stdout, stderr, _, _ := runner.ExecuteTrusted(bin, []string{"-Va"})
		out := strings.TrimSpace(stdout + stderr)
		if out == "" {
			b.WriteString("All package files intact.\n")
		} else {
			b.WriteString(truncateOutput(out, 500))
			b.WriteString("\n")
		}

	case "pacman":
		b.WriteString("=== pacman -Qkk (altered files only) ===\n")
		stdout, stderr, _, _ := runner.ExecuteTrusted(bin, []string{"-Qkk"})
		out := strings.TrimSpace(stdout + stderr)
		issues := filterPacmanIssues(out)
		if issues == "" {
			b.WriteString("All package files intact.\n")
		} else {
			b.WriteString(truncateOutput(issues, 500))
			b.WriteString("\n")
		}
	}

	return b.String()
}

// pkgVerifyOne verifies a single named package.
func pkgVerifyOne(runner Runner, pm, bin, pkg string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== Verifying package: %s ===\n", pkg)

	switch pm {
	case "dpkg":
		stdout, stderr, _, _ := runner.ExecuteTrusted(bin, []string{"--verify", pkg})
		out := strings.TrimSpace(stdout + stderr)
		if out == "" {
			b.WriteString("All files intact.\n")
		} else {
			b.WriteString(out + "\n")
		}
		// Also show installed version
		stdout, _, _, _ = runner.ExecuteTrusted(bin, []string{"-s", pkg})
		if info := strings.TrimSpace(stdout); info != "" {
			b.WriteString("\n--- Package info ---\n")
			b.WriteString(info + "\n")
		}

	case "rpm":
		stdout, stderr, _, _ := runner.ExecuteTrusted(bin, []string{"-V", pkg})
		out := strings.TrimSpace(stdout + stderr)
		if out == "" {
			b.WriteString("All files intact.\n")
		} else {
			b.WriteString(out + "\n")
		}
		stdout, _, _, _ = runner.ExecuteTrusted(bin, []string{"-qi", pkg})
		if info := strings.TrimSpace(stdout); info != "" {
			b.WriteString("\n--- Package info ---\n")
			b.WriteString(info + "\n")
		}

	case "pacman":
		stdout, stderr, _, _ := runner.ExecuteTrusted(bin, []string{"-Qkk", pkg})
		out := strings.TrimSpace(stdout + stderr)
		issues := filterPacmanIssues(out)
		if issues == "" {
			b.WriteString("All files intact.\n")
		} else {
			b.WriteString(issues + "\n")
		}
		stdout, _, _, _ = runner.ExecuteTrusted(bin, []string{"-Qi", pkg})
		if info := strings.TrimSpace(stdout); info != "" {
			b.WriteString("\n--- Package info ---\n")
			b.WriteString(info + "\n")
		}
	}

	return b.String()
}

// pkgQueryFile returns which package owns the given file path.
func pkgQueryFile(runner Runner, pm, bin, path string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("=== Package owning %s ===\n", path))

	switch pm {
	case "dpkg":
		stdout, stderr, exitCode, _ := runner.ExecuteTrusted(bin, []string{"-S", path})
		out := strings.TrimSpace(stdout + stderr)
		if exitCode != 0 || out == "" {
			b.WriteString("File not owned by any dpkg package.\n")
		} else {
			b.WriteString(out + "\n")
		}

	case "rpm":
		stdout, stderr, exitCode, _ := runner.ExecuteTrusted(bin, []string{"-qf", "--queryformat",
			"%{NAME}-%{VERSION}-%{RELEASE}.%{ARCH}\n", path})
		out := strings.TrimSpace(stdout + stderr)
		if exitCode != 0 || strings.Contains(out, "not owned") {
			b.WriteString("File not owned by any rpm package.\n")
		} else {
			b.WriteString(out + "\n")
		}

	case "pacman":
		stdout, stderr, exitCode, _ := runner.ExecuteTrusted(bin, []string{"-Qo", path})
		out := strings.TrimSpace(stdout + stderr)
		if exitCode != 0 {
			b.WriteString("File not owned by any pacman package.\n")
		} else {
			b.WriteString(out + "\n")
		}
	}

	return b.String()
}

// pkgQueryPackage returns the file list and metadata for a named package.
func pkgQueryPackage(runner Runner, pm, bin, pkg string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== Files in package: %s ===\n", pkg)

	switch pm {
	case "dpkg":
		stdout, stderr, _, _ := runner.ExecuteTrusted(bin, []string{"-L", pkg})
		out := strings.TrimSpace(stdout + stderr)
		b.WriteString(truncateOutput(out, 300) + "\n")
		stdout, _, _, _ = runner.ExecuteTrusted(bin, []string{"-s", pkg})
		if info := strings.TrimSpace(stdout); info != "" {
			b.WriteString("\n--- Package info ---\n")
			b.WriteString(info + "\n")
		}

	case "rpm":
		stdout, stderr, _, _ := runner.ExecuteTrusted(bin, []string{"-ql", pkg})
		out := strings.TrimSpace(stdout + stderr)
		b.WriteString(truncateOutput(out, 300) + "\n")
		stdout, _, _, _ = runner.ExecuteTrusted(bin, []string{"-qi", pkg})
		if info := strings.TrimSpace(stdout); info != "" {
			b.WriteString("\n--- Package info ---\n")
			b.WriteString(info + "\n")
		}

	case "pacman":
		stdout, stderr, _, _ := runner.ExecuteTrusted(bin, []string{"-Ql", pkg})
		out := strings.TrimSpace(stdout + stderr)
		b.WriteString(truncateOutput(out, 300) + "\n")
		stdout, _, _, _ = runner.ExecuteTrusted(bin, []string{"-Qi", pkg})
		if info := strings.TrimSpace(stdout); info != "" {
			b.WriteString("\n--- Package info ---\n")
			b.WriteString(info + "\n")
		}
	}

	return b.String()
}

func debsumsVerifyAll(runner Runner) string {
	stdout, stderr, exitCode, _ := runner.ExecuteTrusted("/usr/bin/debsums", []string{"-c"})
	out := strings.TrimSpace(stdout + stderr)
	if exitCode != 0 && strings.Contains(out, "not found") {
		return "debsums not installed.\n"
	}
	if out == "" {
		return "All checksums OK.\n"
	}
	return truncateOutput(out, 500) + "\n"
}

func filterPacmanIssues(out string) string {
	var issues []string
	for _, line := range splitNonEmpty(out) {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "warning") || strings.Contains(lower, "error") ||
			strings.Contains(lower, "missing") || strings.Contains(lower, "altered") {
			issues = append(issues, line)
		}
	}
	return strings.Join(issues, "\n")
}
