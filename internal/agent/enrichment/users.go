package enrichment

import (
	"fmt"
	"strings"
)

// EnrichUsers parses /etc/passwd, group memberships, sudo rules, SSH keys,
// and password aging info for each user with a login shell.
func EnrichUsers(runner Runner) string {
	stdout, _, _, _ := runner.ExecuteTrusted("cat", []string{"/etc/passwd"})
	if stdout == "" {
		return "check_users: Failed to read /etc/passwd"
	}

	var loginUsers []passwdEntry
	nologinCount := 0

	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 7 {
			continue
		}
		shell := fields[6]
		if strings.Contains(shell, "nologin") || strings.Contains(shell, "/false") || shell == "/bin/sync" {
			nologinCount++
			continue
		}
		loginUsers = append(loginUsers, passwdEntry{
			Username: fields[0],
			UID:      fields[2],
			GID:      fields[3],
			Home:     fields[5],
			Shell:    shell,
		})
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Users with login shells (%d) + %d nologin/system accounts:\n\n", len(loginUsers), nologinCount))

	for i, u := range loginUsers {
		b.WriteString(fmt.Sprintf("%d. %s (uid=%s, gid=%s)\n", i+1, u.Username, u.UID, u.GID))
		b.WriteString(fmt.Sprintf("   Shell: %s | Home: %s", u.Shell, u.Home))

		// Check home dir exists
		_, _, exitCode, _ := runner.ExecuteTrusted("test", []string{"-d", u.Home})
		if exitCode != 0 {
			b.WriteString(" (HOME DOES NOT EXIST)")
		}
		b.WriteString("\n")

		// Groups
		stdout, _, _, _ := runner.ExecuteTrusted("id", []string{u.Username})
		if stdout != "" {
			b.WriteString(fmt.Sprintf("   Groups: %s\n", strings.TrimSpace(stdout)))
		}

		// Password info
		stdout, _, exitCode, _ = runner.ExecuteTrusted("chage", []string{"-l", u.Username})
		if exitCode == 0 && stdout != "" {
			for _, line := range strings.Split(stdout, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "Last password change") || strings.HasPrefix(line, "Password expires") || strings.HasPrefix(line, "Account expires") {
					b.WriteString(fmt.Sprintf("   %s\n", line))
				}
			}
		}

		// SSH authorized_keys
		authKeysPath := u.Home + "/.ssh/authorized_keys"
		stdout, _, exitCode, _ = runner.ExecuteTrusted("wc", []string{"-l", authKeysPath})
		if exitCode == 0 {
			count := strings.Fields(strings.TrimSpace(stdout))
			if len(count) > 0 {
				b.WriteString(fmt.Sprintf("   SSH authorized_keys: %s keys\n", count[0]))
			}
		}

		// Sudo rules (check sudoers.d)
		stdout, _, exitCode, _ = runner.ExecuteTrusted("grep", []string{"-r", u.Username, "/etc/sudoers", "/etc/sudoers.d/"})
		if exitCode == 0 && stdout != "" {
			for _, line := range splitNonEmpty(stdout) {
				if !strings.HasPrefix(strings.TrimSpace(line), "#") {
					b.WriteString(fmt.Sprintf("   Sudo: %s\n", strings.TrimSpace(line)))
				}
			}
		}

		b.WriteString("\n")
	}

	return b.String()
}

type passwdEntry struct {
	Username string
	UID      string
	GID      string
	Home     string
	Shell    string
}
