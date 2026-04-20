package gateway

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/overlordtm/jagr/internal/gateway/models"
)

var reGatewayToolCallBlock = regexp.MustCompile(`(?s)<tool_call>\s*(\{.*?\})\s*</tool_call>`)

// extractTextToolCalls parses tool calls embedded in message content as
// <tool_call>{"name": "...", "arguments": {...}}</tool_call> blocks.
// Models like Qwen3 use this format instead of the structured tool_calls field.
func extractTextToolCalls(content string) []models.ToolCall {
	matches := reGatewayToolCallBlock.FindAllStringSubmatch(content, -1)
	var calls []models.ToolCall
	for i, m := range matches {
		var payload struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(m[1]), &payload); err != nil || payload.Name == "" {
			continue
		}
		args := string(payload.Arguments)
		if args == "" || args == "null" {
			args = "{}"
		}
		calls = append(calls, models.ToolCall{
			ID:   fmt.Sprintf("call_%d", i),
			Type: "function",
			Function: models.Function{
				Name:      payload.Name,
				Arguments: args,
			},
		})
	}
	return calls
}
