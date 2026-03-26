// Package enrichment provides Go-side parsers that structure raw system data
// and annotate it with factual metadata. Enrichment tools present ALL entries
// and NEVER classify anything as suspicious — that judgment is left to the LLM.
package enrichment

import (
	"fmt"
	"strings"
)

// Runner abstracts the command execution interface needed by enrichment parsers.
// This is satisfied by *agent.CleanRoom.
type Runner interface {
	ExecuteTrusted(command string, args []string) (stdout, stderr string, exitCode int, err error)
}

// truncateOutput ensures output does not exceed maxLines, keeping head and tail.
func truncateOutput(output string, maxLines int) string {
	lines := strings.Split(output, "\n")
	if len(lines) <= maxLines {
		return output
	}
	headCount := maxLines - 10
	if headCount < 10 {
		headCount = 10
	}
	tailCount := 10

	var b strings.Builder
	for i := 0; i < headCount && i < len(lines); i++ {
		b.WriteString(lines[i])
		b.WriteString("\n")
	}
	b.WriteString(fmt.Sprintf("\n--- TRUNCATED: showing %d of %d lines. Last %d lines below. ---\n\n",
		maxLines, len(lines), tailCount))
	start := len(lines) - tailCount
	if start < 0 {
		start = 0
	}
	for i := start; i < len(lines); i++ {
		b.WriteString(lines[i])
		b.WriteString("\n")
	}
	return b.String()
}
