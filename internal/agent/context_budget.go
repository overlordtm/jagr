package agent

import (
	"fmt"
	"strings"
	"time"
)

// Tool output truncation limits (lines).
var toolMaxLines = map[string]int{
	"run_linpeas_sh":     150,
	"run_linpeas_static": 150,
	"run_pspy":           100,
	"read_file":          100,
	"check_cron":         200,
	"check_users":        200,
	"check_systemd":      200,
	"check_suid":         200,
	"check_modules":      200,
	"check_listeners":    200,
}

const defaultToolMaxLines = 200

// TruncateToolOutput truncates tool output to a per-tool line limit,
// keeping head and tail lines for context.
func TruncateToolOutput(toolName string, output string) string {
	maxLines := defaultToolMaxLines
	if ml, ok := toolMaxLines[toolName]; ok {
		maxLines = ml
	}

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
	b.WriteString(fmt.Sprintf("\n--- TRUNCATED: showing %d of %d lines. Last %d lines below. Use execute_trusted with head/tail/grep to see specific ranges. ---\n\n",
		maxLines, len(lines), tailCount))

	start := len(lines) - tailCount
	if start < 0 {
		start = 0
	}
	for i := start; i < len(lines); i++ {
		b.WriteString(lines[i])
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// WrapToolResult prepends a metadata header to tool output for structure.
func WrapToolResult(toolName string, result string, duration time.Duration) string {
	lineCount := strings.Count(result, "\n") + 1
	tokenEst := len(result) / 4

	header := fmt.Sprintf("[Tool: %s | %d lines | ~%d tokens | %dms]\n",
		toolName, lineCount, tokenEst, duration.Milliseconds())

	return header + result
}

// CalculateOutputBudget determines how many tokens a tool result should use
// based on remaining context capacity.
func CalculateOutputBudget(currentTokens int, maxContextTokens int) int {
	remaining := maxContextTokens - currentTokens
	// Reserve 4K for LLM reasoning + next few turns
	available := remaining - 4000
	if available < 500 {
		return 500
	}
	if available > 8000 {
		return 8000
	}
	return available
}
