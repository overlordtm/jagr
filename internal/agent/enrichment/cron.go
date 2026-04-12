package enrichment

import (
	"fmt"
	"strings"
)

// CronEntry represents a single cron job with enrichment metadata.
type CronEntry struct {
	Source       string
	Schedule     string
	ScheduleDesc string
	User         string
	Command      string
	BinaryPath   string
	BinaryExists bool
	BinaryPkg    string
	BinaryType   string
	BinarySize   string
	BinaryMtime  string
	FileMtime    string
}

// EnrichCron collects and enriches all cron entries on the system.
func EnrichCron(runner Runner) string {
	var entries []CronEntry

	// System crontab
	parseCronFile(runner, "/etc/crontab", true, &entries)

	// /etc/cron.d/*
	stdout, _, _, _ := runner.ExecuteTrusted("ls", []string{"-1", "/etc/cron.d/"})
	for _, name := range splitNonEmpty(stdout) {
		parseCronFile(runner, "/etc/cron.d/"+name, true, &entries)
	}

	// Periodic directories
	for _, dir := range []string{"/etc/cron.hourly", "/etc/cron.daily", "/etc/cron.weekly", "/etc/cron.monthly"} {
		stdout, _, _, _ := runner.ExecuteTrusted("ls", []string{"-1", dir})
		for _, name := range splitNonEmpty(stdout) {
			path := dir + "/" + name
			entries = append(entries, CronEntry{
				Source:   path,
				Schedule: dirToSchedule(dir),
				Command:  path,
			})
		}
	}

	// Per-user crontabs
	users := getShellUsers(runner)
	for _, user := range users {
		stdout, _, exitCode, _ := runner.ExecuteTrusted("crontab", []string{"-l", "-u", user})
		if exitCode == 0 && strings.TrimSpace(stdout) != "" {
			parseUserCrontab(stdout, "crontab -u "+user, user, &entries)
		}
	}

	// Enrich each entry
	for i := range entries {
		enrichCronEntry(runner, &entries[i])
	}

	return formatCronEntries(entries)
}

func parseCronFile(runner Runner, path string, hasUserField bool, entries *[]CronEntry) {
	stdout, _, exitCode, _ := runner.ExecuteTrusted("cat", []string{path})
	if exitCode != 0 || stdout == "" {
		return
	}

	// Get file mtime
	mtime := getFileMtime(runner, path)

	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.Contains(line, "=") {
			continue
		}

		fields := strings.Fields(line)
		minFields := 6
		if hasUserField {
			minFields = 7
		}
		if len(fields) < minFields {
			continue
		}

		schedule := strings.Join(fields[:5], " ")
		var user, command string
		if hasUserField {
			user = fields[5]
			command = strings.Join(fields[6:], " ")
		} else {
			command = strings.Join(fields[5:], " ")
		}

		*entries = append(*entries, CronEntry{
			Source:    path,
			Schedule:  schedule,
			User:      user,
			Command:   command,
			FileMtime: mtime,
		})
	}
}

func parseUserCrontab(content, source, user string, entries *[]CronEntry) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.Contains(line, "=") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		*entries = append(*entries, CronEntry{
			Source:   source,
			Schedule: strings.Join(fields[:5], " "),
			User:     user,
			Command:  strings.Join(fields[5:], " "),
		})
	}
}

func enrichCronEntry(runner Runner, entry *CronEntry) {
	entry.BinaryPath = extractBinaryPath(entry.Command)
	if entry.BinaryPath == "" {
		return
	}

	// Resolve via which
	stdout, _, exitCode, _ := runner.ExecuteTrusted("which", []string{entry.BinaryPath})
	if exitCode == 0 && strings.TrimSpace(stdout) != "" {
		entry.BinaryPath = strings.TrimSpace(stdout)
	}

	// Check existence
	_, _, exitCode, _ = runner.ExecuteTrusted("test", []string{"-f", entry.BinaryPath})
	entry.BinaryExists = exitCode == 0

	if !entry.BinaryExists {
		return
	}

	entry.BinaryPkg = getPackageOwner(runner, entry.BinaryPath)

	// File type
	stdout, _, _, _ = runner.ExecuteTrusted("file", []string{"-b", entry.BinaryPath})
	entry.BinaryType = strings.TrimSpace(stdout)

	// Size
	stdout, _, _, _ = runner.ExecuteTrusted("stat", []string{"-c", "%s", entry.BinaryPath})
	entry.BinarySize = strings.TrimSpace(stdout)

	// Mtime
	entry.BinaryMtime = getFileMtime(runner, entry.BinaryPath)

	// Schedule description
	entry.ScheduleDesc = humanizeCronSchedule(entry.Schedule)
}

func formatCronEntries(entries []CronEntry) string {
	if len(entries) == 0 {
		return "Cron Entries (0 total): No cron jobs found on this system."
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Cron Entries (%d total):\n\n", len(entries)))

	for i, e := range entries {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, e.Source))
		if e.Schedule != "" {
			desc := e.ScheduleDesc
			if desc == "" {
				desc = e.Schedule
			}
			b.WriteString(fmt.Sprintf("   Schedule: %s (%s)", e.Schedule, desc))
			if e.User != "" {
				b.WriteString(fmt.Sprintf(" | User: %s", e.User))
			}
			b.WriteString("\n")
		}
		b.WriteString(fmt.Sprintf("   Command: %s\n", e.Command))
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
				if e.BinarySize != "" {
					b.WriteString(fmt.Sprintf(", size: %s bytes", e.BinarySize))
				}
				if e.BinaryMtime != "" {
					b.WriteString(fmt.Sprintf(", modified: %s", e.BinaryMtime))
				}
				b.WriteString(")")
			} else {
				b.WriteString(" (DOES NOT EXIST)")
			}
			b.WriteString("\n")
		}
		if e.FileMtime != "" {
			b.WriteString(fmt.Sprintf("   Cron file modified: %s\n", e.FileMtime))
		}
		b.WriteString("\n")
	}

	return b.String()
}

// extractBinaryPath gets the first token of a command, stripping env prefixes and shell redirects.
func extractBinaryPath(command string) string {
	cmd := strings.TrimSpace(command)
	// Skip env variable assignments at the start
	for {
		if idx := strings.Index(cmd, "="); idx > 0 && idx < strings.Index(cmd+" ", " ") {
			cmd = strings.TrimSpace(cmd[strings.Index(cmd, " ")+1:])
			continue
		}
		break
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	bin := fields[0]
	// Strip common wrappers
	for _, wrapper := range []string{"/usr/bin/env", "/bin/env", "env"} {
		if bin == wrapper && len(fields) > 1 {
			return fields[1]
		}
	}
	return bin
}

func dirToSchedule(dir string) string {
	switch {
	case strings.HasSuffix(dir, "hourly"):
		return "@hourly"
	case strings.HasSuffix(dir, "daily"):
		return "@daily"
	case strings.HasSuffix(dir, "weekly"):
		return "@weekly"
	case strings.HasSuffix(dir, "monthly"):
		return "@monthly"
	}
	return dir
}

func humanizeCronSchedule(schedule string) string {
	switch schedule {
	case "* * * * *":
		return "every minute"
	case "*/5 * * * *":
		return "every 5 minutes"
	case "*/15 * * * *":
		return "every 15 minutes"
	case "0 * * * *":
		return "every hour"
	case "0 0 * * *":
		return "daily at midnight"
	case "0 0 * * 0":
		return "weekly on Sunday"
	case "0 0 1 * *":
		return "monthly on the 1st"
	case "@reboot":
		return "at boot"
	case "@hourly":
		return "every hour"
	case "@daily", "@midnight":
		return "daily at midnight"
	case "@weekly":
		return "weekly"
	case "@monthly":
		return "monthly"
	case "@yearly", "@annually":
		return "yearly"
	}
	return schedule
}

// helpers

func getShellUsers(runner Runner) []string {
	stdout, _, _, _ := runner.ExecuteTrusted("cat", []string{"/etc/passwd"})
	var users []string
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 7 {
			continue
		}
		shell := fields[6]
		if strings.Contains(shell, "nologin") || strings.Contains(shell, "/false") || shell == "/bin/sync" {
			continue
		}
		users = append(users, fields[0])
	}
	return users
}

func getFileMtime(runner Runner, path string) string {
	stdout, _, _, _ := runner.ExecuteTrusted("stat", []string{"-c", "%y", path})
	return strings.TrimSpace(stdout)
}

func splitNonEmpty(s string) []string {
	var result []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}
