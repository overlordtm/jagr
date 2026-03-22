package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"go.uber.org/zap"
)

type SubAgent struct {
	parent            *Agent
	role              string
	systemPrompt      string
	objective         string
	conversation      []Message
	iterations        int
	concluded         bool
	tools             []Tool
	toolFailureCounts map[string]int
}

func NewSubAgent(parent *Agent, role, systemPrompt, objective string, initialTools []Tool) *SubAgent {
	return &SubAgent{
		parent:            parent,
		role:              role,
		systemPrompt:      systemPrompt,
		objective:         objective,
		tools:             initialTools,
		toolFailureCounts: make(map[string]int),
	}
}

func (sa *SubAgent) Run() error {
	sa.parent.logger.Info("Starting SubAgent", zap.String("role", sa.role))

	prompt := sa.systemPrompt + "\n\n## Available Tools\n" + sa.formatToolsForPrompt()
	sa.conversation = append(sa.conversation, Message{Role: "system", Content: prompt})
	sa.conversation = append(sa.conversation, Message{Role: "user", Content: sa.objective})

	for sa.iterations < sa.parent.maxIter {
		sa.parent.logger.Info("SubAgent Thinking", zap.String("role", sa.role), zap.Int("iteration", sa.iterations+1))

		toolCalls, err := sa.think()
		if err != nil {
			return fmt.Errorf("thinking failed: %w", err)
		}

		if sa.concluded {
			return nil
		}

		for _, tc := range toolCalls {
			sa.parent.logger.Info("SubAgent Tool call", zap.String("role", sa.role), zap.String("tool", tc.Function.Name))
		}

		// Interactive mode
		if sa.parent.mode == "interactive" {
			if approved, hint := sa.parent.promptOperator(toolCalls); !approved {
				if hint != "" {
					sa.conversation = append(sa.conversation, Message{Role: "user", Content: hint})
				}
				continue
			}
		}

		results, err := sa.act(toolCalls)
		if err != nil {
			return fmt.Errorf("action failed: %w", err)
		}

		if err := sa.observe(results); err != nil {
			return fmt.Errorf("observation failed: %w", err)
		}

		if sa.concluded {
			return nil
		}
	}

	sa.parent.logger.Info("SubAgent Max iterations reached", zap.String("role", sa.role))
	sa.concluded = true
	if err := sa.parent.logEvent("subagent_conclude", map[string]any{"role": sa.role, "summary": "Maximum iterations reached"}); err != nil {
		sa.parent.logger.Error("Failed to log subagent conclude event", zap.Error(err))
	}
	return nil
}

func (sa *SubAgent) formatToolsForPrompt() string {
	var toolsStr []string
	for _, tool := range sa.tools {
		toolsStr = append(toolsStr, fmt.Sprintf("- %s: %s", tool.Name, tool.Description))
	}
	return strings.Join(toolsStr, "\n")
}

func (sa *SubAgent) think() ([]ToolCall, error) {
	sa.iterations++

	messages := make([]map[string]string, len(sa.conversation))
	for i, m := range sa.conversation {
		messages[i] = map[string]string{
			"role":    m.Role,
			"content": m.Content,
		}
	}

	tools := make([]map[string]any, 0)
	for _, t := range sa.tools {
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			},
		})
	}

	reqBody := map[string]any{
		"model":    sa.parent.model,
		"messages": messages,
	}
	
	if sa.parent.profiles != nil {
		if profile, ok := sa.parent.profiles[sa.role]; ok {
			if profile.Model != "" {
				reqBody["model"] = profile.Model
			}
			if profile.Temperature > 0 {
				reqBody["temperature"] = profile.Temperature
			}
			if profile.TopP > 0 {
				reqBody["top_p"] = profile.TopP
			}
			if profile.TopK > 0 {
				reqBody["top_k"] = profile.TopK
			}
		}
	}
	if len(tools) > 0 {
		reqBody["tools"] = tools
	}

	reqBytes, _ := json.Marshal(reqBody)

	req, _ := http.NewRequest("POST", sa.parent.gatewayURL+"/v1/chat/completions", strings.NewReader(string(reqBytes)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sa.parent.apiKey)
	req.Header.Set("X-Hostname", sa.parent.hostname())

	resp, err := sa.parent.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gateway request failed: %w", err)
	}
	defer resp.Body.Close()

	var reply map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if usage, ok := reply["usage"].(map[string]any); ok {
		if promptTokens, ok := usage["prompt_tokens"].(float64); ok {
			sa.parent.totalTokensIn += int(promptTokens)
		}
		if completionTokens, ok := usage["completion_tokens"].(float64); ok {
			sa.parent.totalTokensOut += int(completionTokens)
		}
	}

	if choices, ok := reply["choices"].([]any); ok && len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		if msg, ok := choice["message"].(map[string]any); ok {
			content, _ := msg["content"].(string)
			sa.conversation = append(sa.conversation, Message{
				Role:    "assistant",
				Content: content,
			})

			if toolCalls, ok := msg["tool_calls"].([]any); ok {
				var result []ToolCall
				for _, tc := range toolCalls {
					if tcMap, ok := tc.(map[string]any); ok {
						result = append(result, ToolCall{
							ID:   tcMap["id"].(string),
							Type: tcMap["type"].(string),
							Function: Function{
								Name:      tcMap["function"].(map[string]any)["name"].(string),
								Arguments: tcMap["function"].(map[string]any)["arguments"].(string),
							},
						})
					}
				}
				return result, nil
			}

			if content != "" {
				sa.parent.logger.Info("LLM responded without tool calls, concluding SubAgent", zap.String("role", sa.role))
				sa.concluded = true
				if err := sa.parent.logEvent("subagent_conclude", map[string]any{"role": sa.role, "summary": content}); err != nil {
					sa.parent.logger.Error("Failed to log subagent conclude event", zap.Error(err))
				}
				return nil, nil
			}
		}
	}

	return nil, fmt.Errorf("no tool calls in response")
}

func (sa *SubAgent) act(toolCalls []ToolCall) ([]ToolResult, error) {
	var results []ToolResult

	for _, tc := range toolCalls {
		result, err := sa.executeTool(tc)
		if err != nil {
			sa.toolFailureCounts[tc.Function.Name]++
			count := sa.toolFailureCounts[tc.Function.Name]
			sa.parent.logger.Warn("Tool call failed",
				zap.String("role", sa.role),
				zap.String("tool", tc.Function.Name),
				zap.Int("consecutive_failures", count),
				zap.Int("max_failures", sa.parent.maxToolFailures),
				zap.Error(err))

			if count >= sa.parent.maxToolFailures {
				return nil, fmt.Errorf("circuit breaker: tool %q failed %d consecutive times, aborting subagent", tc.Function.Name, count)
			}

			results = append(results, ToolResult{
				ToolID:  tc.ID,
				Name:    tc.Function.Name,
				Content: fmt.Sprintf("Error: %v", err),
				IsError: true,
			})
		} else {
			sa.toolFailureCounts[tc.Function.Name] = 0
			results = append(results, result)
		}
	}

	return results, nil
}

func (sa *SubAgent) executeTool(tc ToolCall) (ToolResult, error) {
	if tc.Function.Name == "delegate_investigation" {
		args, err := ParseToolArguments(tc.Function.Arguments)
		if err != nil {
			return ToolResult{}, fmt.Errorf("failed to parse args: %w", err)
		}
		target, _ := args["target"].(string)
		contextStr, _ := args["context"].(string)
		
		sa.parent.logger.Info("Delegating investigation", zap.String("target", target))
		err = sa.parent.runInvestigator(target, contextStr)
		
		content := fmt.Sprintf("Delegated investigation of %s to an Investigator Agent.", target)
		if err != nil {
			content = fmt.Sprintf("Failed to delegate investigation: %v", err)
		}
		
		return ToolResult{
			ToolID:  tc.ID,
			Name:    tc.Function.Name,
			Content: content,
		}, nil
	} else if tc.Function.Name == "conclude" {
		args, err := ParseToolArguments(tc.Function.Arguments)
		if err != nil {
			return ToolResult{}, fmt.Errorf("failed to parse args: %w", err)
		}
		summary, _ := args["summary"].(string)
		if summary == "" {
			summary = "Investigation phase complete"
		}
		sa.concluded = true
		if err := sa.parent.logEvent("subagent_conclude", map[string]any{"role": sa.role, "summary": summary}); err != nil {
			sa.parent.logger.Error("Failed to log subagent conclude event", zap.Error(err))
		}
		return ToolResult{
			ToolID:  tc.ID,
			Name:    tc.Function.Name,
			Content: summary,
		}, nil
	}
	
	return sa.parent.executeTool(tc)
}

func (sa *SubAgent) observe(results []ToolResult) error {
	for _, result := range results {
		sa.conversation = append(sa.conversation, Message{
			Role:    "tool",
			Content: result.Content,
			Name:    result.Name,
		})

		if err := sa.parent.logEvent("tool_result", map[string]any{
			"role":      sa.role,
			"tool":      result.Name,
			"content":   result.Content,
			"exit_code": result.ExitCode,
		}); err != nil {
			sa.parent.logger.Error("Failed to log tool result", zap.Error(err))
		}

		if result.IsError {
			sa.parent.logger.Error("Tool execution error", zap.String("role", sa.role), zap.String("tool", result.Name))
		}
	}

	return nil
}
