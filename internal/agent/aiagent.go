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
	harness          *JagrHarness
	name             string
	role             string
	systemPrompt     string
	objective        string
	skills           []Skill
	llmContext       *ContextMagic
	iterations       int
	maxIter          int // 0 means resolve from profile/harness defaults
	concluded        bool
	tools            []Tool
	toolbox          *ToolBox
	gateway          *GatewayClient
	findingsStore    *FindingsStore
	investigatorWg   sync.WaitGroup
	delegatedTargets map[string]bool
	paramsCache      map[string]ToolResult
	idCache          map[string]ToolResult
}

// NewAiAgent creates a new AiAgent. For phase agents, name should equal role.
// For investigators, the harness generates a unique name.
func NewAiAgent(harness *JagrHarness, name, role, systemPrompt, objective string, initialTools []Tool) *AiAgent {
	harness.gateway.RegisterProfile(role)

	strategy := &RollingWindowStrategy{
		MaxRawMessages: 15,
		MaxToolCalls:   10,
		Summarize:      NewGatewaySummarizer(harness.gateway, role, name),
	}
	// ctx := NewContextMagic(nil)
	ctx := NewContextMagic(strategy)

	return &AiAgent{
		harness:          harness,
		name:             name,
		role:             role,
		systemPrompt:     systemPrompt,
		objective:        objective,
		skills:           harness.gateway.GetSkills(),
		tools:            initialTools,
		toolbox:          NewToolBox(harness.cleanRoom, harness.maxToolFailures, harness.logger),
		gateway:          harness.gateway,
		findingsStore:    harness.findingsStore,
		llmContext:       ctx,
		delegatedTargets: make(map[string]bool),
		paramsCache:      make(map[string]ToolResult),
		idCache:          make(map[string]ToolResult),
	}
}

// Run executes the agent's think→act→observe loop until concluded or max iterations reached.
func (a *AiAgent) Run() error {
	a.harness.logger.Info("Starting AiAgent", zap.String("name", a.name), zap.String("role", a.role))
	defer a.investigatorWg.Wait()

	prompt := a.systemPrompt + "\n\n## Available Tools\n" + a.formatToolsForPrompt()
	if skillsSection := a.formatSkillsForPrompt(); skillsSection != "" {
		prompt += "\n\n## Skills\nThe following skills provide additional procedures and instructions you should follow when relevant:\n" + skillsSection
	}
	a.llmContext.Append(Message{Role: "system", Content: prompt})
	a.llmContext.Append(Message{Role: "user", Content: a.objective})

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
		// Run batch context compression every 5 iterations if history is large
		if a.iterations > 0 && a.iterations%5 == 0 && len(a.llmContext.RawHistory()) > 20 {
			a.harness.logger.Info("Compacting agent context", zap.String("name", a.name))
			if err := a.llmContext.Compact(); err != nil {
				a.harness.logger.Warn("Context compaction failed", zap.Error(err))
			}

			// Inject a progress checkpoint after compaction so the agent
			// knows what it has already done and how much budget remains.
			a.injectProgressCheckpoint(iterLimit)
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

// injectProgressCheckpoint appends a system message after context compaction
// so the agent always knows its iteration budget and what findings it has
// already submitted. This prevents the amnesia loop where the agent
// re-investigates artifacts it already analyzed before compaction.
func (a *AiAgent) injectProgressCheckpoint(iterLimit int) {
	remaining := iterLimit - a.iterations
	var b strings.Builder
	fmt.Fprintf(&b, "## Progress Checkpoint\n")
	fmt.Fprintf(&b, "- Iteration %d of %d (%d remaining). If remaining <= 3, submit findings NOW and call conclude.\n", a.iterations, iterLimit, remaining)

	submitted := a.findingsStore.GetByAgent(a.name)
	if len(submitted) > 0 {
		b.WriteString("- Findings already submitted (do NOT re-investigate these):\n")
		for _, f := range submitted {
			fmt.Fprintf(&b, "  - [%s] %s: %s\n", f.Severity, f.Observable, f.Type)
		}
	} else {
		b.WriteString("- No findings submitted yet. Gather IOCs and submit a finding soon.\n")
	}

	if len(a.delegatedTargets) > 0 {
		b.WriteString("- Targets already delegated (do NOT re-delegate):\n")
		for t := range a.delegatedTargets {
			fmt.Fprintf(&b, "  - %s\n", t)
		}
	}

	a.llmContext.Append(Message{Role: "user", Content: "[System Note]\n" + b.String()})
}

func (a *AiAgent) gatherPartialEvidence() []string {
	var evidence []string
	for _, msg := range a.llmContext.RawHistory() {
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

func (a *AiAgent) formatSkillsForPrompt() string {
	if len(a.skills) == 0 {
		return ""
	}
	var parts []string
	for _, s := range a.skills {
		header := fmt.Sprintf("### %s", s.Name)
		if s.Description != "" {
			header += fmt.Sprintf(" — %s", s.Description)
		}
		parts = append(parts, header+"\n"+s.Content)
	}
	return strings.Join(parts, "\n\n")
}

func (a *AiAgent) think() ([]ToolCall, error) {
	a.iterations++

	contextMessages := a.llmContext.Messages()
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

	reply, err := a.gateway.ChatCompletion(reqBytes, a.role, a.name)
	if err != nil {
		return nil, fmt.Errorf("gateway request failed: %w", err)
	}

	if usage, ok := reply["usage"].(map[string]any); ok {
		var promptTokens, completionTokens int
		if pt, ok := usage["prompt_tokens"].(float64); ok {
			promptTokens = int(pt)
		}
		if ct, ok := usage["completion_tokens"].(float64); ok {
			completionTokens = int(ct)
		}
		a.gateway.AddTokenUsage(promptTokens, completionTokens)
	}

	if choices, ok := reply["choices"].([]any); ok && len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		finishReason, _ := choice["finish_reason"].(string)
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
				a.llmContext.Append(Message{
					Role:      "assistant",
					Content:   content,
					ToolCalls: parsedCalls,
				})
				return parsedCalls, nil
			}

			a.llmContext.Append(Message{
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

			if finishReason == "stop" {
				a.harness.logger.Warn("LLM stopped with no content or tool calls, concluding agent", zap.String("name", a.name))
				a.concluded = true
				return nil, nil
			}
		}
	}

	replyJSON, _ := json.Marshal(reply)
	a.harness.logger.Error("No tool calls in response", zap.String("name", a.name), zap.String("reply", string(replyJSON)))
	return nil, fmt.Errorf("no tool calls in response")
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
		finding := Finding{
			Type:       stringArg(args, "type"),
			Severity:   stringArg(args, "severity"),
			Observable: stringArg(args, "observable"),
			Analysis:   stringArg(args, "analysis"),
			Evidence:   stringSliceArg(args, "evidence"),
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

	case "read_cached_output":
		return a.executeReadCachedOutput(tc)

	default:
		// Check param cache first for toolbox executed tools
		cacheKey := tc.Function.Name + "|" + tc.Function.Arguments
		if cachedResult, hit := a.paramsCache[cacheKey]; hit {
			// Found in cache, just update the ToolID to the current tool call and return immediately
			cachedResult.ToolID = tc.ID
			a.idCache[tc.ID] = cachedResult
			return cachedResult, nil
		}

		res, err := a.toolbox.ExecuteTool(tc)
		if err == nil && !res.IsError {
			a.paramsCache[cacheKey] = res
			a.idCache[tc.ID] = res
		}
		return res, err
	}
}

func (a *AiAgent) executeReadCachedOutput(tc ToolCall) (ToolResult, error) {
	args, err := ParseToolArguments(tc.Function.Arguments)
	if err != nil {
		return ToolResult{}, fmt.Errorf("failed to parse args: %w", err)
	}

	toolCallID, _ := args["tool_call_id"].(string)
	startLine := 0
	if sl, ok := args["start_line"].(float64); ok {
		startLine = int(sl)
	}
	maxLines := 100
	if ml, ok := args["max_lines"].(float64); ok {
		maxLines = int(ml)
	}

	cached, found := a.idCache[toolCallID]
	if !found {
		return ToolResult{
			ToolID:  tc.ID,
			Name:    tc.Function.Name,
			Content: fmt.Sprintf("Error: tool result for id %s not found in cache", toolCallID),
			IsError: true,
		}, nil
	}

	lines := strings.Split(cached.Content, "\n")
	if startLine < 0 || startLine >= len(lines) {
		return ToolResult{
			ToolID:  tc.ID,
			Name:    tc.Function.Name,
			Content: fmt.Sprintf("Error: start_line %d out of bounds (total lines: %d)", startLine, len(lines)),
			IsError: true,
		}, nil
	}

	endLine := startLine + maxLines
	if endLine > len(lines) {
		endLine = len(lines)
	}

	snippet := strings.Join(lines[startLine:endLine], "\n")
	content := fmt.Sprintf("--- Cached Output (Lines %d to %d of %d) ---\n%s", startLine, endLine-1, len(lines), snippet)
	
	return ToolResult{
		ToolID:  tc.ID,
		Name:    tc.Function.Name,
		Content: content,
	}, nil
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

	// Force memo_type for system_overview agents so the harness can find it later.
	if a.role == "system_overview" {
		memoType = "system_overview"
	}

	resp, err := a.gateway.WriteMemo(scope, content, memoType, a.gateway.Hostname(), a.name)
	if err != nil {
		return ToolResult{}, err
	}

	// Auto-conclude system_overview agents after a successful write — their job is done.
	if a.role == "system_overview" {
		a.concluded = true
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
		content := result.Content
		if result.Name != "read_cached_output" {
			content = TruncateToolOutput(result.Name, result.ToolID, result.Content)
		}
		content = WrapToolResult(result.Name, content, result.Duration)

		a.llmContext.Append(Message{
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
