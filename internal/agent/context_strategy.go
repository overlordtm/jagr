package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ContextStrategy determines how raw conversation history is transformed
// into the messages slice sent to the LLM.
type ContextStrategy interface {
	// Select receives the full raw history and returns the messages
	// that should be sent to the LLM for the next request.
	Select(history []Message) []Message
	// Compact performs an in-place reduction of the history
	Compact(history []Message) ([]Message, error)
}

// FullHistoryStrategy returns the entire conversation history unchanged.
type FullHistoryStrategy struct{}

func (s *FullHistoryStrategy) Select(history []Message) []Message {
	return history
}

func (s *FullHistoryStrategy) Compact(history []Message) ([]Message, error) {
	return history, nil
}

// Summarizer is a function that takes a slice of messages and returns a
// concise summary string. It is typically backed by an LLM call through
// the gateway.
type Summarizer func(messages []Message) (string, error)

// RollingWindowStrategy keeps the last m messages in raw form and the last t
// tool-call pairs (assistant message with ToolCalls + its tool-result messages).
// Older messages outside both windows are truncated into bullet points locally.
// When explicitly compacted, an LLM call summarizes the old items permanently.
type RollingWindowStrategy struct {
	// MaxRawMessages is the number of most-recent messages to keep verbatim.
	MaxRawMessages int
	// MaxToolCalls is the number of most-recent tool-call pairs to keep verbatim.
	MaxToolCalls int
	// Summarize is called during Compact to produce an LLM summary of compressed messages.
	Summarize Summarizer
}

func (s *RollingWindowStrategy) Select(history []Message) []Message {
	n := len(history)
	if n == 0 {
		return nil
	}

	// Build a set of indices that are protected (kept raw).
	protected := make([]bool, n)

	// --- protect last m messages ---
	rawStart := n - s.MaxRawMessages
	if rawStart < 0 {
		rawStart = 0
	}
	for i := rawStart; i < n; i++ {
		protected[i] = true
	}

	// --- protect last t tool-call pairs ---
	// A "tool-call pair" is an assistant message that has ToolCalls, plus the
	// immediately-following tool-result messages whose ToolCallID matches.
	kept := 0
	for i := n - 1; i >= 0 && kept < s.MaxToolCalls; i-- {
		if history[i].Role == "assistant" && len(history[i].ToolCalls) > 0 {
			ids := make(map[string]bool, len(history[i].ToolCalls))
			for _, tc := range history[i].ToolCalls {
				ids[tc.ID] = true
			}
			protected[i] = true
			for j := i + 1; j < n; j++ {
				if history[j].Role == "tool" && ids[history[j].ToolCallID] {
					protected[j] = true
				} else if history[j].Role != "tool" {
					break
				}
			}
			kept++
		}
	}

	// Always protect the first message (system prompt).
	if n > 0 {
		protected[0] = true
	}

	// Always protect the first user message (the objective) to ensure the API requirement
	// (at least one user message) is met, and to preserve the agent's primary goal.
	for i := 0; i < n; i++ {
		if history[i].Role == "user" {
			protected[i] = true
			break
		}
	}

	// Collect messages that need compression.
	var toCompress []Message
	for i, msg := range history {
		if !protected[i] {
			toCompress = append(toCompress, msg)
		}
	}

	// If nothing to compress, return as-is.
	if len(toCompress) == 0 {
		return history
	}

	// Summarize the compressed block locally to save output tokens.
	summary := fallbackCompress(toCompress)

	// --- build output ---
	var result []Message
	compressed := false
	for i, msg := range history {
		if protected[i] {
			result = append(result, msg)
		} else if !compressed {
			// Insert the summary system message in place of the first
			// compressed message to preserve conversation order.
			result = append(result, Message{
				Role:    "user",
				Content: "[System Note] Prior conversation history was summarized locally. Summary:\n\n" + summary + "\n\nNote: You may have written memos. Use read_memos(scope=\"agent\") to recall detailed findings.",
			})
			compressed = true
		}
		// Subsequent compressed messages are simply dropped.
	}

	return result
}

func (s *RollingWindowStrategy) Compact(history []Message) ([]Message, error) {
	n := len(history)
	if n == 0 {
		return history, nil
	}

	// Re-run the exact same protection logic to capture the exact slice we need to permanently summarize
	protected := make([]bool, n)
	rawStart := n - s.MaxRawMessages
	if rawStart < 0 {
		rawStart = 0
	}
	for i := rawStart; i < n; i++ {
		protected[i] = true
	}

	kept := 0
	for i := n - 1; i >= 0 && kept < s.MaxToolCalls; i-- {
		if history[i].Role == "assistant" && len(history[i].ToolCalls) > 0 {
			ids := make(map[string]bool, len(history[i].ToolCalls))
			for _, tc := range history[i].ToolCalls {
				ids[tc.ID] = true
			}
			protected[i] = true
			for j := i + 1; j < n; j++ {
				if history[j].Role == "tool" && ids[history[j].ToolCallID] {
					protected[j] = true
				} else if history[j].Role != "tool" {
					break
				}
			}
			kept++
		}
	}

	if n > 0 {
		protected[0] = true
	}
	for i := 0; i < n; i++ {
		if history[i].Role == "user" {
			protected[i] = true
			break
		}
	}

	var toCompress []Message
	for i, msg := range history {
		if !protected[i] {
			toCompress = append(toCompress, msg)
		}
	}

	if len(toCompress) == 0 {
		return history, nil
	}

	summary, err := s.Summarize(toCompress)
	if err != nil || summary == "" {
		return nil, fmt.Errorf("summarization failed: %w", err)
	}

	var result []Message
	compressed := false
	for i, msg := range history {
		if protected[i] {
			result = append(result, msg)
		} else if !compressed {
			result = append(result, Message{
				Role:    "user",
				Content: "[System Note] Prior conversation history was summarized to save context. Summary:\n\n" + summary + "\n\nNote: You may have written memos. Use read_memos(scope=\"agent\") to recall detailed findings.",
			})
			compressed = true
		}
	}

	return result, nil
}

// fallbackCompress produces a bullet-point summary when the LLM summarizer
// is unavailable or fails.
func fallbackCompress(msgs []Message) string {
	var b strings.Builder
	for _, msg := range msgs {
		b.WriteString("• ")
		b.WriteString(compressLine(msg))
		b.WriteByte('\n')
	}
	return b.String()
}

// NewGatewaySummarizer returns a Summarizer backed by the gateway's chat
// completion endpoint. It uses the "summarize" model and identifies itself
// with the given role and agent name.
func NewGatewaySummarizer(gw *GatewayClient, role, name string) Summarizer {
	return func(messages []Message) (string, error) {
		msgBytes, _ := json.Marshal(messages)

		req := map[string]any{
			"model": "summarize",
			"messages": []map[string]string{
				{"role": "system", "content": "You are a specialized agent for summarizing conversational histories. Provide a concise but comprehensive summary of the provided events and tool results, ensuring all important facts, discovered files, paths, findings, errors, and progress state remain intact."},
				{"role": "user", "content": string(msgBytes)},
			},
		}

		reqBytes, _ := json.Marshal(req)
		reply, err := gw.ChatCompletion(reqBytes, role+"-compactor", name)
		if err != nil {
			return "", fmt.Errorf("gateway summarization failed: %w", err)
		}

		var summary string
		if choices, ok := reply["choices"].([]any); ok && len(choices) > 0 {
			choice, _ := choices[0].(map[string]any)
			if msg, ok := choice["message"].(map[string]any); ok {
				summary, _ = msg["content"].(string)
			}
		}

		if summary == "" {
			return "", fmt.Errorf("empty summary returned from gateway")
		}
		return summary, nil
	}
}

// compressLine produces a one-line summary of a message for fallback compression.
func compressLine(msg Message) string {
	content := msg.Content
	if len(content) > 200 {
		content = content[:200] + "…"
	}
	switch {
	case msg.Role == "assistant" && len(msg.ToolCalls) > 0:
		names := make([]string, len(msg.ToolCalls))
		for i, tc := range msg.ToolCalls {
			names[i] = tc.Function.Name
		}
		return "assistant called tool(s): " + strings.Join(names, ", ")
	case msg.Role == "tool":
		return "tool(" + msg.ToolCallID + "): " + content
	default:
		return msg.Role + ": " + content
	}
}
