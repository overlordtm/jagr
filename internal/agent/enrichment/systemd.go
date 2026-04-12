package enrichment

import (
	"fmt"
	"strings"
)

// EnrichSystemd lists custom/modified systemd units in /etc/systemd/system and /run/systemd/system,
// enriching each with binary metadata and drop-in override detection.
func EnrichSystemd(runner Runner) string {
	var entries []systemdEntry

	// Custom units in /etc/systemd/system (admin-installed)
	scanSystemdDir(runner, "/etc/systemd/system", &entries)

	// Runtime units in /run/systemd/system
	scanSystemdDir(runner, "/run/systemd/system", &entries)

	if len(entries) == 0 {
		return "Systemd Units: No custom/modified units found in /etc/systemd/system or /run/systemd/system."
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Systemd Custom Units (%d total):\n\n", len(entries)))

	for i, e := range entries {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, e.Path))
		b.WriteString(fmt.Sprintf("   Type: %s | Enabled: %s\n", e.UnitType, e.Enabled))
		if e.ExecStart != "" {
			b.WriteString(fmt.Sprintf("   ExecStart: %s\n", e.ExecStart))
		}
		if e.ExecStartPre != "" {
			b.WriteString(fmt.Sprintf("   ExecStartPre: %s\n", e.ExecStartPre))
		}
		if e.BinaryPath != "" {
			b.WriteString(fmt.Sprintf("   Binary: %s", e.BinaryPath))
			if e.BinaryExists {
				b.WriteString(" (exists")
				if e.BinaryPkg != "" {
					b.WriteString(fmt.Sprintf(", pkg: %s", e.BinaryPkg))
				}
				if e.BinaryType != "" {
					b.WriteString(fmt.Sprintf(", type: %s", e.BinaryType))
				}
				b.WriteString(")")
			} else {
				b.WriteString(" (DOES NOT EXIST)")
			}
			b.WriteString("\n")
		}
		if e.IsDropIn {
			b.WriteString("   ** Drop-in override **\n")
		}
		b.WriteString(fmt.Sprintf("   Unit modified: %s\n", e.FileMtime))
		b.WriteString("\n")
	}

	return b.String()
}

type systemdEntry struct {
	Path         string
	UnitType     string
	Enabled      string
	ExecStart    string
	ExecStartPre string
	BinaryPath   string
	BinaryExists bool
	BinaryPkg    string
	BinaryType   string
	IsDropIn     bool
	FileMtime    string
}

func scanSystemdDir(runner Runner, baseDir string, entries *[]systemdEntry) {
	stdout, _, exitCode, _ := runner.ExecuteTrusted("ls", []string{"-1", baseDir})
	if exitCode != 0 {
		return
	}

	for _, name := range splitNonEmpty(stdout) {
		path := baseDir + "/" + name

		// Skip symlinks that point to /dev/null (masked units) or are just wants/requires links
		if strings.HasSuffix(name, ".wants") || strings.HasSuffix(name, ".requires") {
			continue
		}

		// Check if it's a drop-in directory (*.d)
		if strings.HasSuffix(name, ".d") {
			scanDropInDir(runner, path, entries)
			continue
		}

		// Only process unit files
		unitType := getUnitType(name)
		if unitType == "" {
			continue
		}

		entry := systemdEntry{
			Path:     path,
			UnitType: unitType,
		}

		// Check if enabled
		stdout, _, _, _ := runner.ExecuteTrusted("systemctl", []string{"is-enabled", name})
		entry.Enabled = strings.TrimSpace(stdout)
		if entry.Enabled == "" {
			entry.Enabled = "unknown"
		}

		// Parse ExecStart from unit file
		content, _, _, _ := runner.ExecuteTrusted("cat", []string{path})
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "ExecStart=") {
				entry.ExecStart = strings.TrimPrefix(line, "ExecStart=")
			} else if strings.HasPrefix(line, "ExecStartPre=") {
				entry.ExecStartPre = strings.TrimPrefix(line, "ExecStartPre=")
			}
		}

		// Enrich binary
		if entry.ExecStart != "" {
			entry.BinaryPath = extractBinaryPath(entry.ExecStart)
			enrichBinary(runner, &entry)
		}

		entry.FileMtime = getFileMtime(runner, path)
		*entries = append(*entries, entry)
	}
}

func scanDropInDir(runner Runner, dirPath string, entries *[]systemdEntry) {
	stdout, _, exitCode, _ := runner.ExecuteTrusted("ls", []string{"-1", dirPath})
	if exitCode != 0 {
		return
	}
	for _, name := range splitNonEmpty(stdout) {
		if !strings.HasSuffix(name, ".conf") {
			continue
		}
		path := dirPath + "/" + name
		entry := systemdEntry{
			Path:     path,
			UnitType: "drop-in",
			IsDropIn: true,
		}

		content, _, _, _ := runner.ExecuteTrusted("cat", []string{path})
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "ExecStart=") {
				entry.ExecStart = strings.TrimPrefix(line, "ExecStart=")
			}
		}

		if entry.ExecStart != "" {
			entry.BinaryPath = extractBinaryPath(entry.ExecStart)
			enrichBinary(runner, &entry)
		}

		entry.FileMtime = getFileMtime(runner, path)
		*entries = append(*entries, entry)
	}
}

func enrichBinary(runner Runner, entry *systemdEntry) {
	if entry.BinaryPath == "" {
		return
	}
	// Strip leading - or @ prefixes used by systemd
	bp := strings.TrimLeft(entry.BinaryPath, "-@!")
	entry.BinaryPath = bp

	_, _, exitCode, _ := runner.ExecuteTrusted("test", []string{"-f", bp})
	entry.BinaryExists = exitCode == 0

	if !entry.BinaryExists {
		return
	}

	entry.BinaryPkg = getPackageOwner(runner, bp)

	stdout, _, _, _ := runner.ExecuteTrusted("file", []string{"-b", bp})
	entry.BinaryType = strings.TrimSpace(stdout)
}

func getUnitType(name string) string {
	for _, suffix := range []string{".service", ".timer", ".socket", ".mount", ".path", ".slice", ".target"} {
		if strings.HasSuffix(name, suffix) {
			return strings.TrimPrefix(suffix, ".")
		}
	}
	return ""
}
