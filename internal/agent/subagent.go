package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"go.uber.org/zap"
)

type SubAgent struct {
	parent            *Agent
	role              string
	systemPrompt      string
	objective         string
	conversation      []Message
	iterations        int
	maxIter           int // 0 means use parent's maxIter
	concluded         bool
	tools             []Tool
	toolFailureCounts map[string]int
	investigatorWg    sync.WaitGroup
	delegatedTargets  map[string]bool
	lastPromptTokens  int
}

func NewSubAgent(parent *Agent, role, systemPrompt, objective string, initialTools []Tool) *SubAgent {
	parent.RegisterProfile(role)
	return &SubAgent{
		parent:            parent,
		role:              role,
		systemPrompt:      systemPrompt,
		objective:         objective,
		tools:             initialTools,
		toolFailureCounts: make(map[string]int),
		delegatedTargets:  make(map[string]bool),
	}
}

func (sa *SubAgent) Run() error {
	sa.parent.logger.Info("Starting SubAgent", zap.String("role", sa.role))
	defer sa.investigatorWg.Wait()

	prompt := sa.systemPrompt + "\n\n## Available Tools\n" + sa.formatToolsForPrompt()
	sa.conversation = append(sa.conversation, Message{Role: "system", Content: prompt})
	sa.conversation = append(sa.conversation, Message{Role: "user", Content: sa.objective})

	iterLimit := sa.maxIter
	if iterLimit == 0 {
		if profile, ok := sa.parent.GetProfile(sa.role); ok && profile.MaxIterations > 0 {
			iterLimit = profile.MaxIterations
		} else if sa.parent.defaultMaxIter > 0 {
			iterLimit = sa.parent.defaultMaxIter
		} else {
			iterLimit = sa.parent.maxIter
		}
	}
	for sa.iterations < iterLimit {
		if sa.needsCompaction() {
			if err := sa.compactHistory(); err != nil {
				sa.parent.logger.Error("History compaction failed", zap.String("role", sa.role), zap.Error(err))
			}
		}

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

	sa.parent.logger.Warn("SubAgent Max iterations reached", zap.String("role", sa.role))
	sa.concluded = true

	// Submit a finding so partial work is not lost
	sa.parent.mu.Lock()
	finding := Finding{
		ID:         fmt.Sprintf("finding-%d", len(sa.parent.findings)+1),
		Type:       "incomplete_investigation",
		Severity:   "info",
		Observable: fmt.Sprintf("subagent:%s", sa.role),
		Analysis:   fmt.Sprintf("SubAgent %q reached maximum iterations (%d) before completing its investigation. Manual review is required to finish this analysis.", sa.role, iterLimit),
		Evidence:   sa.gatherPartialEvidence(),
		Status:     "preliminary",
	}
	sa.parent.findings = append(sa.parent.findings, finding)
	sa.parent.mu.Unlock()

	// Submit immediately to gateway
	sa.parent.submitSingleFindingToGateway(finding)

	sa.parent.logger.Info("Partial finding submitted for max-iteration subagent",
		zap.String("id", finding.ID),
		zap.String("role", sa.role))

	if err := sa.parent.logEvent("subagent_conclude", map[string]any{
		"role":       sa.role,
		"summary":    "Maximum iterations reached — partial finding submitted, needs manual investigation",
		"finding_id": finding.ID,
	}); err != nil {
		sa.parent.logger.Error("Failed to log subagent conclude event", zap.Error(err))
	}
	return nil
}

func (sa *SubAgent) gatherPartialEvidence() []string {
	var evidence []string
	for _, msg := range sa.conversation {
		if msg.Role == "tool" && !strings.Contains(msg.Content, "Error:") && msg.Content != "" {
			snippet := msg.Content
			if len(snippet) > 200 {
				snippet = snippet[:200] + "..."
			}
			evidence = append(evidence, fmt.Sprintf("[%s] %s", msg.Name, snippet))
		}
	}
	return evidence
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

	messages := make([]map[string]any, len(sa.conversation))
	for i, m := range sa.conversation {
		msg := map[string]any{
			"role":    m.Role,
			"content": m.Content,
		}
		if len(m.ToolCalls) > 0 {
			msg["tool_calls"] = m.ToolCalls
		}
		if m.ToolCallID != "" {
			msg["tool_call_id"] = m.ToolCallID
		}
		messages[i] = msg
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
	
	profile, hasProfile := sa.parent.GetProfile(sa.role)
	if hasProfile {
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
	if len(tools) > 0 {
		reqBody["tools"] = tools
	}

	reqBytes, _ := json.Marshal(reqBody)

	reply, err := sa.doThinkRequest(reqBytes)
	if err != nil {
		// If the error looks like a context-length overflow, try compacting and retrying once.
		if strings.Contains(err.Error(), "context length") || strings.Contains(err.Error(), "maximum context") {
			sa.parent.logger.Warn("Context length exceeded, attempting compaction before retry", zap.String("role", sa.role))
			if compactErr := sa.compactHistory(); compactErr != nil {
				return nil, fmt.Errorf("context overflow and compaction failed: %w (original: %v)", compactErr, err)
			}
			// Rebuild messages from compacted conversation and retry
			messages = make([]map[string]any, len(sa.conversation))
			for i, m := range sa.conversation {
				msg := map[string]any{
					"role":    m.Role,
					"content": m.Content,
				}
				if len(m.ToolCalls) > 0 {
					msg["tool_calls"] = m.ToolCalls
				}
				if m.ToolCallID != "" {
					msg["tool_call_id"] = m.ToolCallID
				}
				messages[i] = msg
			}
			reqBody["messages"] = messages
			reqBytes, _ = json.Marshal(reqBody)
			reply, err = sa.doThinkRequest(reqBytes)
			if err != nil {
				return nil, fmt.Errorf("gateway request failed after compaction: %w", err)
			}
		} else {
			return nil, fmt.Errorf("gateway request failed: %w", err)
		}
	}

	if usage, ok := reply["usage"].(map[string]any); ok {
		sa.parent.mu.Lock()
		if promptTokens, ok := usage["prompt_tokens"].(float64); ok {
			sa.parent.totalTokensIn += int(promptTokens)
			sa.lastPromptTokens = int(promptTokens)
		}
		if completionTokens, ok := usage["completion_tokens"].(float64); ok {
			sa.parent.totalTokensOut += int(completionTokens)
		}
		sa.parent.mu.Unlock()
	}

	if choices, ok := reply["choices"].([]any); ok && len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		if msg, ok := choice["message"].(map[string]any); ok {
			content, _ := msg["content"].(string)

			if rawToolCalls, ok := msg["tool_calls"].([]any); ok {
				var parsedCalls []ToolCall
				for _, tc := range rawToolCalls {
					if tcMap, ok := tc.(map[string]any); ok {
						parsedCalls = append(parsedCalls, ToolCall{
							ID:   tcMap["id"].(string),
							Type: tcMap["type"].(string),
							Function: Function{
								Name:      tcMap["function"].(map[string]any)["name"].(string),
								Arguments: tcMap["function"].(map[string]any)["arguments"].(string),
							},
						})
					}
				}
				// Store assistant message WITH tool_calls so the conversation
				// has proper tool call/result pairing (required by Anthropic).
				sa.conversation = append(sa.conversation, Message{
					Role:      "assistant",
					Content:   content,
					ToolCalls: parsedCalls,
				})
				return parsedCalls, nil
			}

			sa.conversation = append(sa.conversation, Message{
				Role:    "assistant",
				Content: content,
			})

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

func (sa *SubAgent) doThinkRequest(reqBytes []byte) (map[string]any, error) {
	req, _ := http.NewRequest("POST", sa.parent.gatewayURL+"/v1/chat/completions", strings.NewReader(string(reqBytes)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sa.parent.apiKey)
	req.Header.Set("X-Hostname", sa.parent.hostname())
	req.Header.Set("X-Sub-Agent-Role", sa.role)

	resp, err := sa.parent.doGatewayRequest(req, reqBytes)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gateway returned status %d: %s", resp.StatusCode, string(body))
	}

	var reply map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return reply, nil
}

func (sa *SubAgent) estimateTokens() int {
	total := 0
	for _, m := range sa.conversation {
		// Rough estimate: ~4 chars per token
		total += len(m.Content) / 4
		for _, tc := range m.ToolCalls {
			total += len(tc.Function.Arguments) / 4
		}
	}
	return total
}

func (sa *SubAgent) getMaxContext() int {
	sa.parent.mu.Lock()
	defer sa.parent.mu.Unlock()
	model := sa.parent.model
	if profile, hasProfile := sa.parent.profiles[sa.role]; hasProfile && profile.Model != "" {
		model = profile.Model
	}
	if ctx, ok := sa.parent.modelsContextWindow[model]; ok {
		return ctx
	}
	return 0
}

func (sa *SubAgent) needsCompaction() bool {
	maxContext := sa.getMaxContext()
	if maxContext == 0 {
		return false
	}

	threshold := int(float64(maxContext) * 0.8)

	// Check both the last known prompt token count AND a rough estimate
	// of the current conversation size. Tool results can add massive
	// amounts of text between iterations, making lastPromptTokens stale.
	if sa.lastPromptTokens > threshold {
		return true
	}

	return sa.estimateTokens() > threshold
}

func (sa *SubAgent) compactHistory() error {
	sa.parent.logger.Info("Context window nearing limit, compacting history", zap.String("role", sa.role), zap.Int("lastPromptTokens", sa.lastPromptTokens))

	if len(sa.conversation) <= 5 {
		return nil // Not enough messages to compact effectively
	}

	// Keep the system prompt, user objective, and the last 3 messages.
	// Compact everything in between.
	keepFront := 2
	keepBack := 3
	compactLen := len(sa.conversation) - keepFront - keepBack

	if compactLen <= 0 {
		return nil
	}

	messagesToCompact := sa.conversation[keepFront : keepFront+compactLen]
	compactBytes, _ := json.Marshal(messagesToCompact)

	summaryReq := map[string]any{
		"model": "summarize",
		"messages": []map[string]string{
			{"role": "system", "content": "You are a specialized agent for summarizing conversational histories. Provide a concise but comprehensive summary of the provided events and tool results, ensuring all important facts, discovered files, paths, findings, errors, and progress state remain intact."},
			{"role": "user", "content": string(compactBytes)},
		},
	}

	reqBytes, _ := json.Marshal(summaryReq)
	req, _ := http.NewRequest("POST", sa.parent.gatewayURL+"/v1/chat/completions", strings.NewReader(string(reqBytes)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sa.parent.apiKey)
	req.Header.Set("X-Hostname", sa.parent.hostname())
	req.Header.Set("X-Sub-Agent-Role", sa.role+"-compactor")

	resp, err := sa.parent.doGatewayRequest(req, reqBytes)
	if err != nil {
		return fmt.Errorf("gateway request for summary failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway returned unexpected status %d for summary", resp.StatusCode)
	}

	var reply map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return fmt.Errorf("failed to decode summary response: %w", err)
	}

	var summary string
	if choices, ok := reply["choices"].([]any); ok && len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		if msg, ok := choice["message"].(map[string]any); ok {
			summary, _ = msg["content"].(string)
		}
	}

	if summary == "" {
		return fmt.Errorf("empty summary returned")
	}

	// Re-construct conversation
	var newConv []Message
	newConv = append(newConv, sa.conversation[:keepFront]...)
	newConv = append(newConv, Message{
		Role:    "system",
		Content: "Prior history was compacted to prevent context overflow. Here is the summary of prior events:\n\n" + summary,
	})
	newConv = append(newConv, sa.conversation[len(sa.conversation)-keepBack:]...)

	sa.conversation = newConv
	sa.lastPromptTokens = 0 // reset so we don't compact again immediately loop
	sa.parent.logger.Info("History compaction successful", zap.Int("new_conversation_length", len(sa.conversation)))

	return nil
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

		// Deduplicate: don't delegate the same target twice
		if sa.delegatedTargets[target] {
			return ToolResult{
				ToolID:  tc.ID,
				Name:    tc.Function.Name,
				Content: fmt.Sprintf("Investigation of %s already delegated. Continue with other targets or call conclude.", target),
			}, nil
		}
		sa.delegatedTargets[target] = true

		sa.parent.logger.Info("Delegating investigation", zap.String("target", target))
		sa.investigatorWg.Add(1)
		go func() {
			defer sa.investigatorWg.Done()
			if err := sa.parent.runInvestigator(target, contextStr); err != nil {
				sa.parent.logger.Error("Investigator failed", zap.String("target", target), zap.Error(err))
			}
		}()

		return ToolResult{
			ToolID:  tc.ID,
			Name:    tc.Function.Name,
			Content: fmt.Sprintf("Delegated investigation of %s to an Investigator Agent.", target),
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
			Role:       "tool",
			Content:    result.Content,
			Name:       result.Name,
			ToolCallID: result.ToolID,
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
