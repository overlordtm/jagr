package agent

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// Agent represents the JAGR agent instance
type Agent struct {
	gatewayURL string
	apiKey     string
	mode       string
	maxIter    int
	model      string
	objective  string
	outputDir  string
	logger     *zap.Logger
	cleanRoom  *CleanRoom
	httpClient *http.Client

	conversation   []Message
	iterations     int
	totalTokensIn  int
	totalTokensOut int
	findings       []Finding
	startTime      time.Time
	concluded      bool
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

type Finding struct {
	ID         string   `json:"id"`
	Type       string   `json:"type"`
	Severity   string   `json:"severity"`
	Observable string   `json:"observable"`
	Analysis   string   `json:"analysis"`
	Evidence   []string `json:"evidence"`
}

type FindingSummary struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Info     int `json:"info"`
}

func NewAgent(gatewayURL, apiKey, mode string, maxIter int, model, objective, outputDir string, logger *zap.Logger, cleanRoom *CleanRoom, tlsSkipVerify bool) (*Agent, error) {
	httpClient := &http.Client{Timeout: 60 * time.Second}
	if tlsSkipVerify {
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
		logger.Warn("TLS certificate verification disabled")
	}

	return &Agent{
		gatewayURL: gatewayURL,
		apiKey:     apiKey,
		mode:       mode,
		maxIter:    maxIter,
		model:      model,
		objective:  objective,
		outputDir:  outputDir,
		logger:     logger,
		cleanRoom:  cleanRoom,
		httpClient: httpClient,
		startTime:  time.Now(),
	}, nil
}

func (a *Agent) Run() error {
	if err := a.logEvent("init", map[string]any{
		"workspace": a.cleanRoom.WorkDir,
		"hostname":  a.hostname(),
	}); err != nil {
		a.logger.Error("Failed to log init event", zap.Error(err))
	}

	systemPrompt := a.buildSystemPrompt()
	a.conversation = append(a.conversation, Message{Role: "system", Content: systemPrompt})

	for a.iterations < a.maxIter {
		toolCalls, err := a.think()
		if err != nil {
			return fmt.Errorf("thinking failed: %w", err)
		}

		if a.concluded {
			return nil
		}

		// Interactive mode: present proposed actions and wait for approval
		if a.mode == "interactive" {
			if approved, hint := a.promptOperator(toolCalls); !approved {
				if hint != "" {
					a.conversation = append(a.conversation, Message{Role: "user", Content: hint})
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

	a.logger.Info("Max iterations reached, concluding")
	return a.conclude("Maximum iterations reached")
}

// promptOperator presents the proposed tool calls to the operator and waits for approval.
// Returns (approved, hint). If not approved and hint is non-empty, the hint is injected
// as a user message into the conversation.
func (a *Agent) promptOperator(toolCalls []ToolCall) (bool, string) {
	fmt.Println("\n--- Proposed Actions ---")
	for i, tc := range toolCalls {
		fmt.Printf("[%d] %s(%s)\n", i+1, tc.Function.Name, tc.Function.Arguments)
	}
	fmt.Print("\nApprove? [y]es / [n]o / [h]int: ")

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return false, ""
	}

	input := strings.TrimSpace(strings.ToLower(scanner.Text()))
	switch {
	case input == "y" || input == "yes" || input == "":
		return true, ""
	case input == "n" || input == "no":
		return false, ""
	default:
		// Treat any other input as a hint
		return false, input
	}
}

func (a *Agent) hostname() string {
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "unknown"
}

func (a *Agent) buildSystemPrompt() string {
	return `You are Jagr, an autonomous security engineer conducting a defensive security audit
of a Linux system in a cybersecurity exercise environment. You are running as root on
the target host. Your goal is to identify security issues, misconfigurations,
indicators of compromise, backdoors, and persistence mechanisms.

## Your Environment

You operate inside a "Clean Room" — a trusted execution environment extracted to a
RAM-backed directory. All commands you execute run through trusted BusyBox binaries
with a sanitized environment. The host system may be compromised, so you must never
trust host binaries outside the Clean Room.

You communicate with a gateway server that provides your intelligence. Exercise
documentation (network maps, system manuals, baseline configs) is available via the
query_knowledge_base tool.

## Investigation Methodology

Follow this structured approach. You may deviate when findings warrant deeper
investigation, but always return to the methodology.

### Phase 1: Situational Awareness (already completed)
Host context has been provided to you. Review it before proceeding.

### Phase 2: User & Access Audit
- Enumerate all user accounts (/etc/passwd), focusing on UID 0 accounts, users with
  shells, and recently created accounts
- Check /etc/shadow for accounts without passwords or with suspicious hashes
- Review /etc/sudoers and /etc/sudoers.d/ for overly permissive rules
- Check SSH authorized_keys for all users, especially root
- Review /etc/group for unexpected group memberships

### Phase 3: Persistence Mechanisms
- Check all cron locations: /etc/crontab, /etc/cron.d/, /etc/cron.{hourly,daily,weekly,monthly},
  and per-user crontabs (crontab -l for each user)
- Review systemd units: look for unusual .service, .timer files in /etc/systemd/system/
  and /lib/systemd/system/
- Check init scripts in /etc/init.d/
- Examine /etc/rc.local and /etc/profile.d/
- Review shell profiles: /etc/bash.bashrc, ~/.bashrc, ~/.profile, ~/.bash_profile
  for all users with shells
- Check /etc/ld.so.preload for LD_PRELOAD persistence

### Phase 4: Process & Network Analysis
- Examine running processes, look for suspicious parents, unusual binaries,
  processes running from /tmp or /dev/shm
- Start pspy for at least 120 seconds to catch scheduled tasks
- Review listening ports and active connections
- Look for unexpected outbound connections, especially to non-standard ports
- Check iptables/nftables rules for unusual NAT or forwarding rules

### Phase 5: Filesystem Analysis
- Search for recently modified files: find / -mtime -7 -type f (focus on
  /etc, /usr, /var, /root, /home)
- Look for hidden files and directories in unusual locations (/var, /tmp, /dev/shm, /opt)
- Check for SUID/SGID binaries and compare against expected set
- Look for world-writable files in sensitive locations
- Check /tmp, /var/tmp, /dev/shm for suspicious files

### Phase 6: Log Analysis
- Review auth logs (/var/log/auth.log or /var/log/secure) for brute force,
  successful logins from unexpected sources, privilege escalation
- Check syslog/journal for unusual service starts, crashes, or errors
- Look for log tampering: gaps in timestamps, truncated files, cleared logs

### Phase 7: Advanced Checks (if warranted by earlier findings)
- Run LinPEAS for comprehensive privilege escalation checks
- Check for kernel modules (lsmod), compare against expected modules
- Review /etc/hosts for DNS hijacking
- Check for container escapes if running in a containerized environment
- Investigate any anomalies found in earlier phases

## Available Tools
` + a.formatToolsForPrompt() + `

## Rules

1. NEVER run LinPEAS first. Do manual investigation first (Phases 2-6). LinPEAS
   is a supplement in Phase 7, not a replacement for manual analysis.
2. Submit findings as you discover them via submit_finding. Do not accumulate
   findings and submit at the end.
3. For each finding, provide: what you found, why it is a security issue, the
   evidence (exact file content or command output), and MITRE ATT&CK technique ID
   if applicable.
4. Classify severity accurately:
   - critical: active backdoor, reverse shell, rootkit, credential theft
   - high: persistence mechanism, privilege escalation vector, unauthorized access
   - medium: misconfiguration that could enable escalation, weak permissions
   - low: informational security hardening recommendations
   - info: observations that provide context but are not issues
5. If you find something suspicious, investigate deeper before concluding. Follow
   the trail: a suspicious cron job might point to a dropped binary, which might
   point to a C2 channel.
6. When output is large, use head/tail/grep to focus on relevant sections rather
   than reading everything.
7. Do not modify the target system. You are investigating, not remediating.
8. Use query_knowledge_base when you encounter unfamiliar services, need to verify
   expected configurations, or want to understand the network topology.
9. Set your confidence level honestly. If you are uncertain whether something is
   malicious or legitimate, say so and explain your reasoning.
10. When you have completed all phases and followed up on all leads, call conclude
    with a summary of your findings.`
}

func (a *Agent) formatToolsForPrompt() string {
	var tools []string
	for _, tool := range GetAvailableTools() {
		tools = append(tools, fmt.Sprintf("- %s: %s", tool.Name, tool.Description))
	}
	return strings.Join(tools, "\n")
}

func (a *Agent) think() ([]ToolCall, error) {
	a.iterations++

	// Build request body manually
	messages := make([]map[string]string, len(a.conversation))
	for i, m := range a.conversation {
		messages[i] = map[string]string{
			"role":    m.Role,
			"content": m.Content,
		}
	}

	tools := make([]map[string]any, 0)
	for _, t := range GetAvailableTools() {
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
			},
		})
	}

	reqBody := map[string]any{
		"model":    a.model,
		"messages": messages,
		"tools":    tools,
	}

	reqBytes, _ := json.Marshal(reqBody)

	// Call gateway
	req, _ := http.NewRequest("POST", a.gatewayURL+"/v1/chat/completions", strings.NewReader(string(reqBytes)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("X-Hostname", a.hostname())

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gateway request failed: %w", err)
	}
	defer resp.Body.Close()

	var reply map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Track token usage
	if usage, ok := reply["usage"].(map[string]any); ok {
		if promptTokens, ok := usage["prompt_tokens"].(float64); ok {
			a.totalTokensIn += int(promptTokens)
		}
		if completionTokens, ok := usage["completion_tokens"].(float64); ok {
			a.totalTokensOut += int(completionTokens)
		}
	}

	if choices, ok := reply["choices"].([]any); ok && len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		if msg, ok := choice["message"].(map[string]any); ok {
			content, _ := msg["content"].(string)
			a.conversation = append(a.conversation, Message{
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

			// No tool calls — LLM wants to finish without calling conclude
			if content != "" {
				a.logger.Info("LLM responded without tool calls, concluding")
				a.concluded = true
				a.conclude(content)
				return nil, nil
			}
		}
	}

	return nil, fmt.Errorf("no tool calls in response")
}

func (a *Agent) act(toolCalls []ToolCall) ([]ToolResult, error) {
	var results []ToolResult

	for _, tc := range toolCalls {
		result, err := a.executeTool(tc)
		if err != nil {
			results = append(results, ToolResult{
				ToolID:  tc.ID,
				Name:    tc.Function.Name,
				Content: fmt.Sprintf("Error: %v", err),
				IsError: true,
			})
		} else {
			results = append(results, result)
		}
	}

	return results, nil
}

func (a *Agent) executeTool(tc ToolCall) (ToolResult, error) {
	args, err := ParseToolArguments(tc.Function.Arguments)
	if err != nil {
		return ToolResult{}, fmt.Errorf("failed to parse args: %w", err)
	}

	switch tc.Function.Name {
	case "execute_trusted":
		return a.execExecuteTrusted(tc, args)
	case "read_file":
		return a.execReadFile(tc, args)
	case "write_file":
		return a.execWriteFile(tc, args)
	case "get_system_env":
		return a.execGetSystemEnv(tc, args)
	case "run_linpeas_sh":
		return a.execLinpeasSh(tc, args)
	case "run_linpeas_static":
		return a.execLinpeasStatic(tc, args)
	case "run_pspy":
		return a.execPspy(tc, args)
	case "list_dir":
		return a.execListDir(tc, args)
	case "search_files":
		return a.execSearchFiles(tc, args)
	case "get_network_info":
		return a.execGetNetworkInfo(tc, args)
	case "submit_finding":
		return a.execSubmitFinding(tc, args)
	case "conclude":
		return a.execConclude(tc, args)
	default:
		return ToolResult{}, fmt.Errorf("unknown tool: %s", tc.Function.Name)
	}
}

func (a *Agent) execExecuteTrusted(tc ToolCall, args map[string]any) (ToolResult, error) {
	command, _ := args["command"].(string)
	if command == "" {
		return ToolResult{}, fmt.Errorf("command is required")
	}

	var cmdArgs []string
	if rawArgs, ok := args["args"].([]any); ok {
		for _, arg := range rawArgs {
			if s, ok := arg.(string); ok {
				cmdArgs = append(cmdArgs, s)
			}
		}
	}

	stdout, stderr, exitCode, err := a.cleanRoom.ExecuteTrusted(command, cmdArgs)
	return ToolResult{
		ToolID:   tc.ID,
		Name:     tc.Function.Name,
		Content:  stdout + "\n" + stderr,
		ExitCode: exitCode,
	}, err
}

func (a *Agent) execReadFile(tc ToolCall, args map[string]any) (ToolResult, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return ToolResult{}, fmt.Errorf("path is required")
	}

	maxLines := 1000
	if ml, ok := args["max_lines"].(int); ok {
		maxLines = ml
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return ToolResult{}, err
	}

	content := FormatToolOutput(string(data), "read_file", maxLines)
	return ToolResult{
		ToolID:  tc.ID,
		Name:    tc.Function.Name,
		Content: content,
	}, nil
}

func (a *Agent) execWriteFile(tc ToolCall, args map[string]any) (ToolResult, error) {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)

	if path == "" {
		return ToolResult{}, fmt.Errorf("path is required")
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return ToolResult{}, err
	}

	return ToolResult{
		ToolID:  tc.ID,
		Name:    tc.Function.Name,
		Content: fmt.Sprintf("Written %d bytes to %s", len(content), path),
	}, nil
}

func (a *Agent) execGetSystemEnv(tc ToolCall, args map[string]any) (ToolResult, error) {
	pid := 1
	if p, ok := args["pid"].(int); ok {
		pid = p
	}

	environPath := fmt.Sprintf("/proc/%d/environ", pid)
	data, err := os.ReadFile(environPath)
	if err != nil {
		return ToolResult{}, err
	}

	envVars := strings.Split(string(data), "\x00")
	var result []string
	for _, v := range envVars {
		if v != "" {
			result = append(result, v)
		}
	}

	content := strings.Join(result, "\n")
	if len(content) > 5000 {
		content = content[:5000] + "\n... [truncated]"
	}

	return ToolResult{
		ToolID:  tc.ID,
		Name:    tc.Function.Name,
		Content: content,
	}, nil
}

func (a *Agent) execLinpeasSh(tc ToolCall, args map[string]any) (ToolResult, error) {
	flags := "-a"
	if f, ok := args["flags"].(string); ok {
		flags = f
	}

	scriptPath := a.cleanRoom.GetToolPath("linpeas")
	if scriptPath == "" {
		return ToolResult{}, fmt.Errorf("linpeas script not found")
	}

	stdout, stderr, exitCode, err := a.cleanRoom.ExecuteTrustedLong(scriptPath, []string{flags})
	content := stdout + "\n" + stderr
	content = FilterLinPEASOutput(content)

	return ToolResult{
		ToolID:   tc.ID,
		Name:     tc.Function.Name,
		Content:  content,
		ExitCode: exitCode,
	}, err
}

func (a *Agent) execLinpeasStatic(tc ToolCall, args map[string]any) (ToolResult, error) {
	flags := "-a"
	if f, ok := args["flags"].(string); ok {
		flags = f
	}

	staticPath := a.cleanRoom.GetToolPath("linpeas_static")
	if staticPath == "" {
		return a.execLinpeasSh(tc, args)
	}

	stdout, stderr, exitCode, err := a.cleanRoom.ExecuteTrustedLong(staticPath, []string{flags})
	content := stdout + "\n" + stderr
	content = FilterLinPEASOutput(content)

	return ToolResult{
		ToolID:   tc.ID,
		Name:     tc.Function.Name,
		Content:  content,
		ExitCode: exitCode,
	}, err
}

func (a *Agent) execPspy(tc ToolCall, args map[string]any) (ToolResult, error) {
	duration := 30
	if d, ok := args["duration_seconds"].(int); ok {
		duration = d
	}

	pspyPath := a.cleanRoom.GetToolPath("pspy")
	if pspyPath == "" {
		return ToolResult{}, fmt.Errorf("pspy not found")
	}

	stdout, stderr, exitCode, err := a.cleanRoom.ExecuteTrustedLong(pspyPath, []string{"-d", fmt.Sprintf("%d", duration)})
	content := stdout + "\n" + stderr
	content = DeduplicatepspyEvents(content)

	return ToolResult{
		ToolID:   tc.ID,
		Name:     tc.Function.Name,
		Content:  content,
		ExitCode: exitCode,
	}, err
}

func (a *Agent) execListDir(tc ToolCall, args map[string]any) (ToolResult, error) {
	path, _ := args["path"].(string)
	if path == "" {
		path = "/"
	}

	recursive := false
	if r, ok := args["recursive"].(bool); ok {
		recursive = r
	}

	argsSlice := []string{"-la", path}
	if recursive {
		argsSlice = []string{"-laR", path}
	}

	stdout, stderr, exitCode, err := a.cleanRoom.ExecuteTrusted("ls", argsSlice)
	content := stdout + "\n" + stderr

	return ToolResult{
		ToolID:   tc.ID,
		Name:     tc.Function.Name,
		Content:  content,
		ExitCode: exitCode,
	}, err
}

func (a *Agent) execSearchFiles(tc ToolCall, args map[string]any) (ToolResult, error) {
	pattern, _ := args["pattern"].(string)
	path, _ := args["path"].(string)
	maxResults := 100
	if mr, ok := args["max_results"].(int); ok {
		maxResults = mr
	}

	if pattern == "" {
		return ToolResult{}, fmt.Errorf("pattern is required")
	}

	if path == "" {
		path = "/"
	}

	argsSlice := []string{"-r", "-n", "-m", fmt.Sprintf("%d", maxResults), pattern, path}

	stdout, stderr, exitCode, err := a.cleanRoom.ExecuteTrusted("grep", argsSlice)
	content := stdout + "\n" + stderr

	return ToolResult{
		ToolID:   tc.ID,
		Name:     tc.Function.Name,
		Content:  content,
		ExitCode: exitCode,
	}, err
}

func (a *Agent) execGetNetworkInfo(tc ToolCall, args map[string]any) (ToolResult, error) {
	var results []string

	stdout, _, _, _ := a.cleanRoom.ExecuteTrusted("ip", []string{"a"})
	results = append(results, "=== Interfaces ===")
	results = append(results, strings.Split(stdout, "\n")...)

	stdout, _, _, _ = a.cleanRoom.ExecuteTrusted("ip", []string{"r"})
	results = append(results, "\n=== Routes ===")
	results = append(results, strings.Split(stdout, "\n")...)

	stdout, _, _, _ = a.cleanRoom.ExecuteTrusted("ss", []string{"-tuln"})
	results = append(results, "\n=== Connections ===")
	results = append(results, strings.Split(stdout, "\n")...)

	content := strings.Join(results, "\n")

	return ToolResult{
		ToolID:  tc.ID,
		Name:    tc.Function.Name,
		Content: content,
	}, nil
}

func (a *Agent) execSubmitFinding(tc ToolCall, args map[string]any) (ToolResult, error) {
	findingBytes, _ := json.Marshal(args["finding"])

	var finding Finding
	if err := json.Unmarshal(findingBytes, &finding); err != nil {
		return ToolResult{}, err
	}

	a.findings = append(a.findings, finding)

	a.logger.Info("Finding submitted",
		zap.String("id", finding.ID),
		zap.String("type", finding.Type),
		zap.String("severity", finding.Severity))

	return ToolResult{
		ToolID:  tc.ID,
		Name:    tc.Function.Name,
		Content: fmt.Sprintf("Finding %s submitted", finding.ID),
	}, nil
}

func (a *Agent) execConclude(tc ToolCall, args map[string]any) (ToolResult, error) {
	summary, _ := args["summary"].(string)
	if summary == "" {
		summary = "Investigation complete"
	}

	a.concluded = true
	a.conclude(summary)

	return ToolResult{
		ToolID:  tc.ID,
		Name:    tc.Function.Name,
		Content: summary,
	}, nil
}

func (a *Agent) observe(results []ToolResult) error {
	for _, result := range results {
		a.conversation = append(a.conversation, Message{
			Role:    "tool",
			Content: result.Content,
			Name:    result.Name,
		})

		if err := a.logEvent("tool_result", map[string]any{
			"tool":      result.Name,
			"content":   result.Content,
			"exit_code": result.ExitCode,
		}); err != nil {
			a.logger.Error("Failed to log tool result", zap.Error(err))
		}

		if result.IsError {
			a.logger.Error("Tool execution error", zap.String("tool", result.Name))
		}
	}

	return nil
}

func (a *Agent) conclude(summary string) error {
	if err := a.logEvent("conclude", map[string]any{"summary": summary}); err != nil {
		a.logger.Error("Failed to log conclude event", zap.Error(err))
	}

	a.logger.Info("Investigation complete",
		zap.Int("iterations", a.iterations),
		zap.Int("findings", len(a.findings)))

	return a.generateReports(summary)
}

func (a *Agent) generateReports(summary string) error {
	report := Report{
		Metadata: ReportMetadata{
			Project:     "jagr",
			Version:     "2.0",
			AgentID:     fmt.Sprintf("agent-%s-%s", a.hostname(), a.startTime.Format("20060102")),
			Hostname:    a.hostname(),
			StartTime:   a.startTime,
			EndTime:     time.Now(),
			Mode:        a.mode,
			Model:       a.model,
			Iterations:  a.iterations,
			TotalTokens: a.totalTokensIn + a.totalTokensOut,
		},
		Findings: a.findings,
		Summary:  a.buildSummary(),
	}

	ocsfPath := filepath.Join(a.outputDir, "findings.json")
	if err := saveJSON(ocsfPath, report); err != nil {
		return err
	}

	markdown := a.generateMarkdownReport(report)
	markdownPath := filepath.Join(a.outputDir, "report.md")
	if err := os.WriteFile(markdownPath, []byte(markdown), 0644); err != nil {
		return err
	}

	return nil
}

func (a *Agent) buildSummary() FindingSummary {
	counts := map[string]int{}
	for _, f := range a.findings {
		counts[f.Severity]++
	}
	return FindingSummary{
		Critical: counts["critical"],
		High:     counts["high"],
		Medium:   counts["medium"],
		Low:      counts["low"],
		Info:     counts["info"],
	}
}

func (a *Agent) generateMarkdownReport(report Report) string {
	var md []string

	md = append(md, "# Security Audit Report - JAGR")
	md = append(md, "\n## Summary")
	md = append(md, fmt.Sprintf("Total findings: %d", len(report.Findings)))
	md = append(md, fmt.Sprintf("- Critical: %d", report.Summary.Critical))
	md = append(md, fmt.Sprintf("- High: %d", report.Summary.High))
	md = append(md, fmt.Sprintf("- Medium: %d", report.Summary.Medium))
	md = append(md, fmt.Sprintf("- Low: %d", report.Summary.Low))
	md = append(md, fmt.Sprintf("- Info: %d", report.Summary.Info))

	md = append(md, "\n## Host Information")
	md = append(md, fmt.Sprintf("- Hostname: %s", report.Metadata.Hostname))

	md = append(md, "\n## Findings")
	for _, f := range report.Findings {
		md = append(md, fmt.Sprintf("\n### %s - %s", f.ID, f.Severity))
		md = append(md, fmt.Sprintf("**Type:** %s", f.Type))
		md = append(md, fmt.Sprintf("**Observable:** %s", f.Observable))
		md = append(md, fmt.Sprintf("**Analysis:** %s", f.Analysis))
		md = append(md, "\n**Evidence:**")
		for _, e := range f.Evidence {
			md = append(md, fmt.Sprintf("- %s", e))
		}
		md = append(md, "\n**Remediation:** (Review Required)")
	}

	md = append(md, "\n## Investigation Timeline")
	md = append(md, fmt.Sprintf("- Started: %s", report.Metadata.StartTime.Format(time.RFC3339)))
	md = append(md, fmt.Sprintf("- Completed: %s", report.Metadata.EndTime.Format(time.RFC3339)))

	md = append(md, "\n## LLM Reasoning Log")
	md = append(md, "See jagr-events.jsonl for complete event log.")

	return strings.Join(md, "\n")
}

func (a *Agent) logEvent(eventType string, data map[string]any) error {
	event := Event{
		Timestamp: time.Now().UTC(),
		Type:      eventType,
		Data:      data,
	}

	eventPath := filepath.Join(a.outputDir, "jagr-events.jsonl")
	f, err := os.OpenFile(eventPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	eventJSON, _ := json.Marshal(event)
	_, err = f.WriteString(string(eventJSON) + "\n")
	return err
}

type Event struct {
	Timestamp time.Time      `json:"ts"`
	Type      string         `json:"type"`
	Data      map[string]any `json:"data"`
}

type Report struct {
	Metadata ReportMetadata `json:"metadata"`
	Findings []Finding      `json:"findings"`
	Summary  FindingSummary `json:"summary"`
}

type ReportMetadata struct {
	Project     string    `json:"project"`
	Version     string    `json:"version"`
	AgentID     string    `json:"agent_id"`
	Hostname    string    `json:"hostname"`
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
	Mode        string    `json:"mode"`
	Model       string    `json:"model"`
	Iterations  int       `json:"iterations"`
	TotalTokens int       `json:"total_tokens,omitempty"`
}

func saveJSON(path string, data any) error {
	jsonBytes, _ := json.MarshalIndent(data, "", "  ")
	return os.WriteFile(path, jsonBytes, 0644)
}
