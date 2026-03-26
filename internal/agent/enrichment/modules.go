package enrichment

import (
	"fmt"
	"strings"
)

// EnrichModules parses loaded kernel modules and cross-references with
// on-disk module files and dmesg load messages.
func EnrichModules(runner Runner) string {
	stdout, _, _, _ := runner.ExecuteTrusted("cat", []string{"/proc/modules"})
	if stdout == "" {
		return "Kernel Modules: Failed to read /proc/modules"
	}

	lines := splitNonEmpty(stdout)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Loaded Kernel Modules (%d):\n\n", len(lines)))

	// Get kernel release for module path lookup
	uname, _, _, _ := runner.ExecuteTrusted("uname", []string{"-r"})
	kernelRelease := strings.TrimSpace(uname)

	// Get dmesg module messages
	dmesg, _, _, _ := runner.ExecuteTrusted("dmesg", nil)
	dmesgLines := strings.Split(dmesg, "\n")

	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		name := fields[0]
		size := fields[1]
		refcount := fields[2]
		usedBy := ""
		if len(fields) > 3 {
			usedBy = strings.TrimSuffix(fields[3], ",")
		}

		b.WriteString(fmt.Sprintf("%d. %s (size: %s, refs: %s", i+1, name, size, refcount))
		if usedBy != "" && usedBy != "-" {
			b.WriteString(fmt.Sprintf(", used_by: %s", usedBy))
		}
		b.WriteString(")\n")

		// Check if module file exists on disk
		if kernelRelease != "" {
			stdout, _, exitCode, _ := runner.ExecuteTrusted("find", []string{
				"/lib/modules/" + kernelRelease,
				"-name", name + ".ko*",
				"-type", "f",
			})
			if exitCode == 0 && strings.TrimSpace(stdout) != "" {
				modPath := strings.TrimSpace(strings.SplitN(stdout, "\n", 2)[0])
				b.WriteString(fmt.Sprintf("   On-disk: %s\n", modPath))
				pkg := getPackageOwner(runner, modPath)
				if pkg != "" {
					b.WriteString(fmt.Sprintf("   Package: %s\n", pkg))
				} else {
					b.WriteString("   Package: NOT from any installed package\n")
				}
			} else {
				b.WriteString("   On-disk: NOT FOUND in /lib/modules\n")
			}
		}

		// Check /sys/module parameters
		stdout, _, exitCode, _ := runner.ExecuteTrusted("ls", []string{"/sys/module/" + name + "/parameters/"})
		if exitCode == 0 && strings.TrimSpace(stdout) != "" {
			params := splitNonEmpty(stdout)
			if len(params) > 0 {
				b.WriteString(fmt.Sprintf("   Parameters: %s\n", strings.Join(params, ", ")))
			}
		}

		// Check dmesg for module-related messages
		for _, dl := range dmesgLines {
			if strings.Contains(dl, name) && (strings.Contains(dl, "module") || strings.Contains(dl, "loaded") || strings.Contains(dl, "registered")) {
				b.WriteString(fmt.Sprintf("   dmesg: %s\n", strings.TrimSpace(dl)))
				break // only show first relevant message
			}
		}

		b.WriteString("\n")
	}

	return b.String()
}
