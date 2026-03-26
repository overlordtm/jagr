package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// AiAgent runs a think→act→observe loop, communicating with the LLM via GatewayClient.
// It is the only component that talks to the LLM API.
type AiAgent struct {
	harness        *JagrHarness
	name           string
	role           string
	systemPrompt   string
	objective      string
	context        *ContextMagic
	iterations     int
	maxIter        int // 0 means resolve from profile/harness defaults
	concluded      bool
	tools          []Tool
	toolbox        *ToolBox
	gateway        *GatewayClient
	findingsStore  *FindingsStore
	investigatorWg   sync.WaitGroup
	delegatedTargets map[string]bool
	lastPromptTokens int
}

// NewAiAgent creates a new AiAgent. For phase agents, name should equal role.
// For investigators, the harness generates a unique name.
func NewAiAgent(harness *JagrHarness, name, role, systemPrompt, objective string, initialTools []Tool) *AiAgent {
	harness.gateway.RegisterProfile(role)
	return &AiAgent{
		harness:          harness,
		name:             name,
		role:             role,
		systemPrompt:     systemPrompt,
		objective:        objective,
		tools:            initialTools,
		toolbox:          NewToolBox(harness.cleanRoom, harness.maxToolFailures, harness.logger),
		gateway:          harness.gateway,
		findingsStore:    harness.findingsStore,
		context:          NewContextMagic(nil),
		delegatedTargets: make(map[string]bool),
	}
}

// Run executes the agent's think→act→observe loop until concluded or max iterations reached.
func (a *AiAgent) Run() error {
	a.harness.logger.Info("Starting AiAgent", zap.String("name", a.name), zap.String("role", a.role))
	defer a.investigatorWg.Wait()

	prompt := a.systemPrompt + "\n\n## Available Tools\n" + a.formatToolsForPrompt()
	a.context.Append(Message{Role: "system", Content: prompt})
	a.context.Append(Message{Role: "user", Content: a.objective})

	iterLimit := a.maxIter
	if iterLimit == 0 {
		if profile, ok := a.gateway.GetProfile(a.role); ok && profile.MaxIterations > 0 {
			iterLimit = profile.MaxIterations
		} else if defaultMax := a.gateway.GetDefaultMaxIter(); defaultMax > 0 {
			iterLimit = defaultMax
		} else {
			iterLimit = a.harness.maxIter
		}
	}
	for a.iterations < iterLimit {
		if a.needsCompaction() {
			if err := a.compactHistory(); err != nil {
				a.harness.logger.Error("History compaction failed", zap.String("name", a.name), zap.Error(err))
			}
		}

		a.harness.logger.Info("AiAgent Thinking", zap.String("name", a.name), zap.Int("iteration", a.iterations+1))

		toolCalls, err := a.think()
		if err != nil {
			return fmt.Errorf("thinking failed: %w", err)
		}

		if a.concluded {
			return nil
		}

		for _, tc := range toolCalls {
			a.harness.logger.Info("AiAgent Tool call", zap.String("name", a.name), zap.String("tool", tc.Function.Name))
		}

		// Interactive mode
		if a.harness.mode == "interactive" {
			if approved, hint := a.harness.promptOperator(toolCalls); !approved {
				if hint != "" {
					a.context.Append(Message{Role: "user", Content: hint})
				}
				continue
			}
		}

		results, err := a.act(toolCalls)
		if err != nil {
			return fmt.Errorf("action failed: %w", err)
		}

		if err := a.observe(results); err != nil {
			return fmt.Errorf("observation failed: %w", err)
		}

		if a.concluded {
			return nil
		}
	}

	a.harness.logger.Warn("AiAgent Max iterations reached", zap.String("name", a.name))
	a.concluded = true

	// Submit a finding so partial work is not lost
	finding := Finding{
		Type:       "incomplete_investigation",
		Severity:   "info",
		Observable: fmt.Sprintf("agent:%s", a.name),
		Analysis:   fmt.Sprintf("Agent %q (role: %s) reached maximum iterations (%d) before completing its investigation. Manual review is required to finish this analysis.", a.name, a.role, iterLimit),
		Evidence:   a.gatherPartialEvidence(),
	}
	stored, _ := a.findingsStore.Add(finding, a.name)
	a.gateway.SubmitFinding(stored)

	a.harness.logger.Info("Partial finding submitted for max-iteration agent",
		zap.String("id", stored.ID),
		zap.String("name", a.name))

	if err := a.harness.logEvent("agent_conclude", map[string]any{
		"name":       a.name,
		"role":       a.role,
		"summary":    "Maximum iterations reached — partial finding submitted, needs manual investigation",
		"finding_id": stored.ID,
	}); err != nil {
		a.harness.logger.Error("Failed to log agent conclude event", zap.Error(err))
	}
	return nil
}

func (a *AiAgent) gatherPartialEvidence() []string {
	var evidence []string
	for _, msg := range a.context.RawHistory() {
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

func (a *AiAgent) formatToolsForPrompt() string {
	var toolsStr []string
	for _, tool := range a.tools {
		toolsStr = append(toolsStr, fmt.Sprintf("- %s: %s", tool.Name, tool.Description))
	}
	return strings.Join(toolsStr, "\n")
}

func (a *AiAgent) think() ([]ToolCall, error) {
	a.iterations++

	contextMessages := a.context.Messages()
	messages := make([]map[string]any, len(contextMessages))
	for i, m := range contextMessages {
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
	for _, t := range a.tools {
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
		"model":    a.gateway.DefaultModel(),
		"messages": messages,
	}

	profile, hasProfile := a.gateway.GetProfile(a.role)
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

	reply, err := a.gateway.ChatCompletion(reqBytes, a.role)
	if err != nil {
		// If the error looks like a context-length overflow, try compacting and retrying once.
		if strings.Contains(err.Error(), "context length") || strings.Contains(err.Error(), "maximum context") {
			a.harness.logger.Warn("Context length exceeded, attempting compaction before retry", zap.String("name", a.name))
			if compactErr := a.compactHistory(); compactErr != nil {
				return nil, fmt.Errorf("context overflow and compaction failed: %w (original: %v)", compactErr, err)
			}
			// Rebuild messages from compacted conversation and retry
			contextMessages = a.context.Messages()
			messages = make([]map[string]any, len(contextMessages))
			for i, m := range contextMessages {
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
			reply, err = a.gateway.ChatCompletion(reqBytes, a.role)
			if err != nil {
				return nil, fmt.Errorf("gateway request failed after compaction: %w", err)
			}
		} else {
			return nil, fmt.Errorf("gateway request failed: %w", err)
		}
	}

	if usage, ok := reply["usage"].(map[string]any); ok {
		var promptTokens, completionTokens int
		if pt, ok := usage["prompt_tokens"].(float64); ok {
			promptTokens = int(pt)
			a.lastPromptTokens = promptTokens
		}
		if ct, ok := usage["completion_tokens"].(float64); ok {
			completionTokens = int(ct)
		}
		a.gateway.AddTokenUsage(promptTokens, completionTokens)
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
				a.context.Append(Message{
					Role:      "assistant",
					Content:   content,
					ToolCalls: parsedCalls,
				})
				return parsedCalls, nil
			}

			a.context.Append(Message{
				Role:    "assistant",
				Content: content,
			})

			if content != "" {
				a.harness.logger.Info("LLM responded without tool calls, concluding agent", zap.String("name", a.name))
				a.concluded = true
				if err := a.harness.logEvent("agent_conclude", map[string]any{"name": a.name, "role": a.role, "summary": content}); err != nil {
					a.harness.logger.Error("Failed to log agent conclude event", zap.Error(err))
				}
				return nil, nil
			}
		}
	}

	return nil, fmt.Errorf("no tool calls in response")
}

func (a *AiAgent) estimateTokens() int {
	total := 0
	for _, m := range a.context.RawHistory() {
		total += len(m.Content) / 4
		for _, tc := range m.ToolCalls {
			total += len(tc.Function.Arguments) / 4
		}
	}
	return total
}

func (a *AiAgent) getMaxContext() int {
	model := a.gateway.DefaultModel()
	if profile, ok := a.gateway.GetProfile(a.role); ok && profile.Model != "" {
		model = profile.Model
	}
	return a.gateway.GetMaxContext(model)
}

func (a *AiAgent) needsCompaction() bool {
	maxContext := a.getMaxContext()
	if maxContext == 0 {
		return false
	}

	threshold := int(float64(maxContext) * 0.8)

	if a.lastPromptTokens > threshold {
		return true
	}

	return a.estimateTokens() > threshold
}

func (a *AiAgent) compactHistory() error {
	a.harness.logger.Info("Context window nearing limit, compacting history", zap.String("name", a.name), zap.Int("lastPromptTokens", a.lastPromptTokens))

	history := a.context.RawHistory()
	if len(history) <= 5 {
		return nil
	}

	keepFront := 2
	keepBack := 3
	compactLen := len(history) - keepFront - keepBack

	if compactLen <= 0 {
		return nil
	}

	messagesToCompact := history[keepFront : keepFront+compactLen]
	compactBytes, _ := json.Marshal(messagesToCompact)

	summaryReq := map[string]any{
		"model": "summarize",
		"messages": []map[string]string{
			{"role": "system", "content": "You are a specialized agent for summarizing conversational histories. Provide a concise but comprehensive summary of the provided events and tool results, ensuring all important facts, discovered files, paths, findings, errors, and progress state remain intact."},
			{"role": "user", "content": string(compactBytes)},
		},
	}

	reqBytes, _ := json.Marshal(summaryReq)
	reply, err := a.gateway.ChatCompletion(reqBytes, a.role+"-compactor")
	if err != nil {
		return fmt.Errorf("gateway request for summary failed: %w", err)
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

	var newConv []Message
	newConv = append(newConv, history[:keepFront]...)
	newConv = append(newConv, Message{
		Role:    "system",
		Content: "Prior history was compacted to prevent context overflow. Here is the summary of prior events:\n\n" + summary + "\n\nNote: You may have written memos. Use read_memos(scope=\"agent\") to recall detailed findings.",
	})
	newConv = append(newConv, history[len(history)-keepBack:]...)

	a.context.SetHistory(newConv)
	a.lastPromptTokens = 0
	a.harness.logger.Info("History compaction successful", zap.Int("new_conversation_length", a.context.Len()))

	return nil
}

func (a *AiAgent) act(toolCalls []ToolCall) ([]ToolResult, error) {
	var results []ToolResult

	for _, tc := range toolCalls {
		start := time.Now()
		result, err := a.executeTool(tc)
		elapsed := time.Since(start)

		if err != nil {
			count := a.toolbox.IncrementFailure(tc.Function.Name)
			a.harness.logger.Warn("Tool call failed",
				zap.String("name", a.name),
				zap.String("tool", tc.Function.Name),
				zap.Int("consecutive_failures", count),
				zap.Int("max_failures", a.toolbox.maxFailures),
				zap.Error(err))

			if a.toolbox.IsCircuitBroken(tc.Function.Name) {
				return nil, fmt.Errorf("circuit breaker: tool %q failed %d consecutive times, aborting agent", tc.Function.Name, count)
			}

			results = append(results, ToolResult{
				ToolID:   tc.ID,
				Name:     tc.Function.Name,
				Content:  fmt.Sprintf("Error: %v", err),
				IsError:  true,
				Duration: elapsed,
			})
		} else {
			result.Duration = elapsed
			a.toolbox.ResetFailure(tc.Function.Name)
			results = append(results, result)
		}
	}

	return results, nil
}

func (a *AiAgent) executeTool(tc ToolCall) (ToolResult, error) {
	switch tc.Function.Name {
	case "delegate_investigation":
		args, err := ParseToolArguments(tc.Function.Arguments)
		if err != nil {
			return ToolResult{}, fmt.Errorf("failed to parse args: %w", err)
		}
		target, _ := args["target"].(string)
		contextStr, _ := args["context"].(string)

		if a.delegatedTargets[target] {
			return ToolResult{
				ToolID:  tc.ID,
				Name:    tc.Function.Name,
				Content: fmt.Sprintf("Investigation of %s already delegated. Continue with other targets or call conclude.", target),
			}, nil
		}
		a.delegatedTargets[target] = true

		a.harness.logger.Info("Delegating investigation", zap.String("target", target))
		a.investigatorWg.Add(1)
		go func() {
			defer a.investigatorWg.Done()
			if err := a.harness.runInvestigator(target, contextStr); err != nil {
				a.harness.logger.Error("Investigator failed", zap.String("target", target), zap.Error(err))
			}
		}()

		return ToolResult{
			ToolID:  tc.ID,
			Name:    tc.Function.Name,
			Content: fmt.Sprintf("Delegated investigation of %s to an Investigator Agent.", target),
		}, nil

	case "conclude":
		args, err := ParseToolArguments(tc.Function.Arguments)
		if err != nil {
			return ToolResult{}, fmt.Errorf("failed to parse args: %w", err)
		}
		summary, _ := args["summary"].(string)
		if summary == "" {
			summary = "Investigation phase complete"
		}
		a.concluded = true
		if err := a.harness.logEvent("agent_conclude", map[string]any{"name": a.name, "role": a.role, "summary": summary}); err != nil {
			a.harness.logger.Error("Failed to log agent conclude event", zap.Error(err))
		}
		return ToolResult{
			ToolID:  tc.ID,
			Name:    tc.Function.Name,
			Content: summary,
		}, nil

	case "submit_finding":
		args, err := ParseToolArguments(tc.Function.Arguments)
		if err != nil {
			return ToolResult{}, fmt.Errorf("failed to parse args: %w", err)
		}
		findingBytes, _ := json.Marshal(args["finding"])
		var finding Finding
		if err := json.Unmarshal(findingBytes, &finding); err != nil {
			return ToolResult{}, err
		}

		stored, isDuplicate := a.findingsStore.Add(finding, a.name)
		if isDuplicate {
			return ToolResult{
				ToolID:  tc.ID,
				Name:    tc.Function.Name,
				Content: fmt.Sprintf("Finding for %q already submitted (id: %s). Do not resubmit. Call conclude to finish your investigation.", finding.Observable, stored.ID),
			}, nil
		}

		a.gateway.SubmitFinding(stored)

		a.harness.logger.Info("Finding submitted",
			zap.String("id", stored.ID),
			zap.String("type", stored.Type),
			zap.String("severity", stored.Severity),
			zap.String("agent", a.name))

		return ToolResult{
			ToolID:  tc.ID,
			Name:    tc.Function.Name,
			Content: fmt.Sprintf("Finding %s submitted successfully. If you have no more findings, call conclude.", stored.ID),
		}, nil

	case "query_knowledge_base":
		return a.executeQueryKnowledgeBase(tc)

	case "write_memo":
		return a.executeWriteMemo(tc)

	case "read_memos":
		return a.executeReadMemos(tc)

	default:
		return a.toolbox.ExecuteTool(tc)
	}
}

func (a *AiAgent) executeQueryKnowledgeBase(tc ToolCall) (ToolResult, error) {
	args, err := ParseToolArguments(tc.Function.Arguments)
	if err != nil {
		return ToolResult{}, fmt.Errorf("failed to parse args: %w", err)
	}
	query, _ := args["query"].(string)
	collection, _ := args["collection"].(string)
	topK := 5
	if tk, ok := args["top_k"].(float64); ok && tk > 0 {
		topK = int(tk)
	}

	resp, err := a.gateway.QueryKnowledgeBase(query, collection, topK)
	if err != nil {
		return ToolResult{}, err
	}

	return ToolResult{
		ToolID:  tc.ID,
		Name:    tc.Function.Name,
		Content: resp,
	}, nil
}

func (a *AiAgent) executeWriteMemo(tc ToolCall) (ToolResult, error) {
	args, err := ParseToolArguments(tc.Function.Arguments)
	if err != nil {
		return ToolResult{}, fmt.Errorf("failed to parse args: %w", err)
	}
	scope, _ := args["scope"].(string)
	content, _ := args["content"].(string)
	memoType, _ := args["memo_type"].(string)
	if memoType == "" {
		memoType = "observation"
	}

	resp, err := a.gateway.WriteMemo(scope, content, memoType, "")
	if err != nil {
		return ToolResult{}, err
	}

	return ToolResult{
		ToolID:  tc.ID,
		Name:    tc.Function.Name,
		Content: fmt.Sprintf("Memo written (scope: %s). %s", scope, resp),
	}, nil
}

func (a *AiAgent) executeReadMemos(tc ToolCall) (ToolResult, error) {
	args, err := ParseToolArguments(tc.Function.Arguments)
	if err != nil {
		return ToolResult{}, fmt.Errorf("failed to parse args: %w", err)
	}
	scope, _ := args["scope"].(string)

	var since string
	if sinceMin, ok := args["since_minutes"].(float64); ok && sinceMin > 0 {
		since = time.Now().Add(-time.Duration(sinceMin) * time.Minute).UTC().Format(time.RFC3339)
	}

	resp, err := a.gateway.ReadMemos(scope, "", since, "", 50)
	if err != nil {
		return ToolResult{}, err
	}

	return ToolResult{
		ToolID:  tc.ID,
		Name:    tc.Function.Name,
		Content: resp,
	}, nil
}

func (a *AiAgent) observe(results []ToolResult) error {
	for _, result := range results {
		// Apply truncation and wrapping to tool output
		content := TruncateToolOutput(result.Name, result.Content)
		content = WrapToolResult(result.Name, content, result.Duration)

		a.context.Append(Message{
			Role:       "tool",
			Content:    content,
			Name:       result.Name,
			ToolCallID: result.ToolID,
		})

		if err := a.harness.logEvent("tool_result", map[string]any{
			"name":      a.name,
			"role":      a.role,
			"tool":      result.Name,
			"content":   result.Content,
			"exit_code": result.ExitCode,
		}); err != nil {
			a.harness.logger.Error("Failed to log tool result", zap.Error(err))
		}

		if result.IsError {
			a.harness.logger.Error("Tool execution error", zap.String("name", a.name), zap.String("tool", result.Name))
		}
	}

	return nil
}
