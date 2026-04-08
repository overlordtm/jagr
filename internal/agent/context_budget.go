package agent

import (
	"fmt"
	"strings"
	"time"
)

// TruncateToolOutput truncates tool output to 40 lines (20 head, 20 tail)
// keeping head and tail lines for context and prompting how to paginate the rest.
func TruncateToolOutput(toolName string, toolCallID string, output string) string {
	lines := strings.Split(output, "\n")
	if len(lines) <= 40 {
		return output
	}

	headCount := 20
	tailCount := 20

	var b strings.Builder
	for i := 0; i < headCount && i < len(lines); i++ {
		b.WriteString(lines[i])
		b.WriteString("\n")
	}
	b.WriteString(fmt.Sprintf("\n--- TRUNCATED: showing %d of %d lines. The complete result is cached. To read more lines, call 'read_cached_output' tool with tool_call_id='%s' and appropriate start_line and max_lines. ---\n\n",
		headCount+tailCount, len(lines), toolCallID))

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
