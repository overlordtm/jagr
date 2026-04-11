package agent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const (
	gemmaToolCallOpen      = "<|tool_call>"
	gemmaToolCallClose     = "<tool_call|>"
	gemmaToolResponseOpen  = "<|tool_response>"
	gemmaToolResponseClose = "<tool_response|>"
	gemmaStringDelim       = "<|\"|>"
)

// formatGemmaToolResponse wraps a tool result in Gemma 4's native token format.
func formatGemmaToolResponse(name, content string) string {
	return gemmaToolResponseOpen +
		"response:" + name +
		"{result:" + gemmaStringDelim + content + gemmaStringDelim + "}" +
		gemmaToolResponseClose
}

// parseGemmaToolCalls extracts tool calls from Gemma 4's native content format.
//
// Gemma 4 embeds tool calls in the content field using special tokens rather than
// the OpenAI tool_calls field:
//
//	<|tool_call>call:function_name{key:<|"|>value<|"|>}<tool_call|>
//
// String values use <|"|> as delimiter. Numeric and boolean values are bare.
// Multiple tool calls may appear in a single response.
func parseGemmaToolCalls(content string) []ToolCall {
	var calls []ToolCall
	remaining := content

	for idx := 0; ; idx++ {
		start := strings.Index(remaining, gemmaToolCallOpen)
		if start == -1 {
			break
		}
		afterOpen := remaining[start+len(gemmaToolCallOpen):]
		end := strings.Index(afterOpen, gemmaToolCallClose)
		if end == -1 {
			break
		}
		block := afterOpen[:end]
		remaining = afterOpen[end+len(gemmaToolCallClose):]

		tc, err := parseGemmaBlock(block, idx)
		if err != nil {
			continue
		}
		calls = append(calls, tc)
	}
	return calls
}

func parseGemmaBlock(block string, idx int) (ToolCall, error) {
	const prefix = "call:"
	if !strings.HasPrefix(block, prefix) {
		return ToolCall{}, fmt.Errorf("unexpected block format: %q", block)
	}
	block = block[len(prefix):]

	braceIdx := strings.Index(block, "{")
	if braceIdx == -1 {
		return ToolCall{}, fmt.Errorf("missing arguments in block: %q", block)
	}

	funcName := strings.TrimSpace(block[:braceIdx])
	argsStr := block[braceIdx:]

	p := &gemmaParser{s: argsStr}
	args, err := p.object()
	if err != nil {
		return ToolCall{}, fmt.Errorf("parse args for %q: %w", funcName, err)
	}

	argsJSON, err := json.Marshal(args)
	if err != nil {
		return ToolCall{}, fmt.Errorf("marshal args for %q: %w", funcName, err)
	}

	return ToolCall{
		ID:   fmt.Sprintf("gemma_call_%d", idx),
		Type: "function",
		Function: Function{
			Name:      funcName,
			Arguments: string(argsJSON),
		},
	}, nil
}

type gemmaParser struct {
	s   string
	pos int
}

func (p *gemmaParser) peek() byte {
	if p.pos >= len(p.s) {
		return 0
	}
	return p.s[p.pos]
}

func (p *gemmaParser) hasPrefix(prefix string) bool {
	return strings.HasPrefix(p.s[p.pos:], prefix)
}

func (p *gemmaParser) ws() {
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			break
		}
		p.pos++
	}
}

func (p *gemmaParser) object() (map[string]any, error) {
	if p.peek() != '{' {
		return nil, fmt.Errorf("expected '{' at pos %d", p.pos)
	}
	p.pos++
	result := make(map[string]any)

	for {
		p.ws()
		if p.pos >= len(p.s) {
			return nil, fmt.Errorf("unterminated object")
		}
		if p.peek() == '}' {
			p.pos++
			return result, nil
		}

		key, err := p.key()
		if err != nil {
			return nil, err
		}
		p.ws()
		if p.peek() != ':' {
			return nil, fmt.Errorf("expected ':' after key %q at pos %d", key, p.pos)
		}
		p.pos++

		val, err := p.value()
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", key, err)
		}
		result[key] = val

		p.ws()
		if p.peek() == ',' {
			p.pos++
		}
	}
}

func (p *gemmaParser) array() ([]any, error) {
	if p.peek() != '[' {
		return nil, fmt.Errorf("expected '[' at pos %d", p.pos)
	}
	p.pos++
	var result []any

	for {
		p.ws()
		if p.pos >= len(p.s) {
			return nil, fmt.Errorf("unterminated array")
		}
		if p.peek() == ']' {
			p.pos++
			return result, nil
		}

		val, err := p.value()
		if err != nil {
			return nil, err
		}
		result = append(result, val)

		p.ws()
		if p.peek() == ',' {
			p.pos++
		}
	}
}

func (p *gemmaParser) value() (any, error) {
	p.ws()
	if p.pos >= len(p.s) {
		return nil, fmt.Errorf("unexpected end of input")
	}
	if p.hasPrefix(gemmaStringDelim) {
		return p.gemmaString()
	}
	if p.peek() == '{' {
		return p.object()
	}
	if p.peek() == '[' {
		return p.array()
	}
	return p.bare()
}

func (p *gemmaParser) gemmaString() (string, error) {
	p.pos += len(gemmaStringDelim)
	end := strings.Index(p.s[p.pos:], gemmaStringDelim)
	if end == -1 {
		return "", fmt.Errorf("unterminated string at pos %d", p.pos)
	}
	str := p.s[p.pos : p.pos+end]
	p.pos += end + len(gemmaStringDelim)
	return str, nil
}

func (p *gemmaParser) key() (string, error) {
	start := p.pos
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c == ':' || c == '}' || c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			break
		}
		p.pos++
	}
	if p.pos == start {
		return "", fmt.Errorf("empty key at pos %d", p.pos)
	}
	return p.s[start:p.pos], nil
}

func (p *gemmaParser) bare() (any, error) {
	start := p.pos
	for p.pos < len(p.s) {
		if p.hasPrefix(gemmaStringDelim) {
			break
		}
		c := p.s[p.pos]
		if c == ',' || c == '}' || c == ']' || c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			break
		}
		p.pos++
	}
	raw := p.s[start:p.pos]
	if raw == "" {
		return nil, fmt.Errorf("empty value at pos %d", p.pos)
	}
	switch raw {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	}
	if i, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return i, nil
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil {
		return f, nil
	}
	return raw, nil
}
