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
	gatewayURL      string
	apiKey          string
	mode            string
	maxIter         int
	maxToolFailures int
	model           string
	objective       string
	outputDir       string
	logger          *zap.Logger
	cleanRoom       *CleanRoom
	httpClient      *http.Client

	conversation     []Message
	iterations       int
	totalTokensIn    int
	totalTokensOut   int
	findings         []Finding
	startTime        time.Time
	concluded        bool
	toolFailureCounts map[string]int
	profiles          map[string]AgentProfile
}

type AgentProfile struct {
	Model       string  `json:"model"`
	Temperature float32 `json:"temperature"`
	TopP        float32 `json:"top_p"`
	TopK        int     `json:"top_k"`
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

// DefaultHTTPTimeout is the default timeout for HTTP requests to the gateway.
const DefaultHTTPTimeout = 60 * time.Second

func NewAgent(gatewayURL, apiKey, mode string, maxIter, maxToolFailures int, model, objective, outputDir string, logger *zap.Logger, cleanRoom *CleanRoom, tlsSkipVerify bool, httpTimeout time.Duration) (*Agent, error) {
	if httpTimeout == 0 {
		httpTimeout = DefaultHTTPTimeout
	}
	httpClient := &http.Client{Timeout: httpTimeout}
	if tlsSkipVerify {
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
		logger.Warn("TLS certificate verification disabled")
	}

	return &Agent{
		gatewayURL:        gatewayURL,
		apiKey:            apiKey,
		mode:              mode,
		maxIter:           maxIter,
		maxToolFailures:   maxToolFailures,
		model:             model,
		objective:         objective,
		outputDir:         outputDir,
		logger:            logger,
		cleanRoom:         cleanRoom,
		httpClient:        httpClient,
		startTime:         time.Now(),
		toolFailureCounts: make(map[string]int),
	}, nil
}

func (a *Agent) Run() error {
	if err := a.logEvent("init", map[string]any{
		"workspace": a.cleanRoom.WorkDir,
		"hostname":  a.hostname(),
	}); err != nil {
		a.logger.Error("Failed to log init event", zap.Error(err))
	}

	a.fetchProfiles()

	heartbeatDone := make(chan struct{})
	go a.heartbeatLoop(heartbeatDone)
	defer close(heartbeatDone)
	defer a.closeSession()

	hostContext := a.collectHostContext()

	phases := []string{
		"UserAccess",
		"Persistence",
		"Network",
		"Filesystem",
		"LogAnalysis",
	}

	for _, p := range phases {
		a.logger.Info("Starting Phase", zap.String("phase", p))
		objective := fmt.Sprintf("## Target Host Context\n\n%s\n\nBegin your investigation phase.", hostContext)
		
		role := "phase_" + p
		prompt, err := GetPrompt(role, map[string]interface{}{})
		if err != nil {
			a.logger.Error("Failed to load prompt template", zap.Error(err))
			continue
		}

		agent := NewSubAgent(a, role, prompt, objective, GetToolsForRole(role))
		if err := agent.Run(); err != nil {
			a.logger.Error("Phase agent failed", zap.String("phase", p), zap.Error(err))
		}
	}

	// Reporter Agent
	a.logger.Info("Starting Reporter Agent")
	findingsJson, _ := json.MarshalIndent(a.findings, "", "  ")
	reportPath := filepath.Join(a.outputDir, "report.md")
	reporterObjective := fmt.Sprintf("Synthesize these findings into a detailed markdown report.\nWrite the final report to exactly this path: %s\n\nFindings:\n%s", reportPath, string(findingsJson))
	reporterPrompt, _ := GetPrompt("reporter", nil)
	reporterAgent := NewSubAgent(a, "reporter", reporterPrompt, reporterObjective, GetToolsForRole("reporter"))
	if err := reporterAgent.Run(); err != nil {
		a.logger.Error("Reporter agent failed", zap.Error(err))
	}

	a.generateReports("Multi-agent architecture concluded")

	return nil
}

func (a *Agent) runInvestigator(target, contextStr string) error {
	objective := fmt.Sprintf("Analyze target: %s\nContext: %s", target, contextStr)
	prompt, _ := GetPrompt("investigator", nil)
	investigator := NewSubAgent(a, "investigator", prompt, objective, GetToolsForRole("investigator"))
	return investigator.Run()
}

func (a *Agent) fetchProfiles() {
	req, err := http.NewRequest("GET", a.gatewayURL+"/v1/agent/config", nil)
	if err != nil {
		a.logger.Debug("Failed to create profile fetch request", zap.Error(err))
		return
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("X-Hostname", a.hostname())

	resp, err := a.httpClient.Do(req)
	if err != nil {
		a.logger.Debug("Failed to fetch agent profiles", zap.Error(err))
		return
	}
	defer resp.Body.Close()

	var profiles map[string]AgentProfile
	if err := json.NewDecoder(resp.Body).Decode(&profiles); err == nil {
		if a.profiles == nil {
			a.profiles = make(map[string]AgentProfile)
		}
		for k, v := range profiles {
			a.profiles[k] = v
		}
		a.logger.Info("Loaded agent profiles from gateway", zap.Int("count", len(a.profiles)))
	} else {
		a.logger.Debug("Failed to decode agent profiles", zap.Error(err))
	}
}

// heartbeatLoop sends periodic heartbeats to the gateway every 10 seconds.
// It stops when the done channel is closed.
func (a *Agent) heartbeatLoop(done <-chan struct{}) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			a.sendHeartbeat()
		}
	}
}

func (a *Agent) sendHeartbeat() {
	req, err := http.NewRequest("POST", a.gatewayURL+"/v1/heartbeat", nil)
	if err != nil {
		a.logger.Debug("Failed to create heartbeat request", zap.Error(err))
		return
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("X-Hostname", a.hostname())

	resp, err := a.httpClient.Do(req)
	if err != nil {
		a.logger.Debug("Heartbeat failed", zap.Error(err))
		return
	}
	resp.Body.Close()
}

func (a *Agent) closeSession() {
	req, err := http.NewRequest("POST", a.gatewayURL+"/v1/sessions/close", nil)
	if err != nil {
		a.logger.Error("Failed to create close session request", zap.Error(err))
		return
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("X-Hostname", a.hostname())

	resp, err := a.httpClient.Do(req)
	if err != nil {
		a.logger.Error("Failed to close session", zap.Error(err))
		return
	}
	resp.Body.Close()
	a.logger.Info("Session closed on gateway")
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

func (a *Agent) collectHostContext() string {
	var sections []string

	// Hostname & kernel
	if out, _, _, err := a.cleanRoom.ExecuteTrusted("uname", []string{"-a"}); err == nil && out != "" {
		sections = append(sections, "### Kernel\n"+strings.TrimSpace(out))
	}

	// Distro info
	if out, _, _, err := a.cleanRoom.ExecuteTrusted("cat", []string{"/etc/os-release"}); err == nil && out != "" {
		sections = append(sections, "### Distribution\n"+strings.TrimSpace(out))
	}

	// Uptime
	if out, _, _, err := a.cleanRoom.ExecuteTrusted("uptime", nil); err == nil && out != "" {
		sections = append(sections, "### Uptime\n"+strings.TrimSpace(out))
	}

	// Network interfaces & IPs
	if out, _, _, err := a.cleanRoom.ExecuteTrusted("ip", []string{"a"}); err == nil && out != "" {
		sections = append(sections, "### Network Interfaces\n"+strings.TrimSpace(out))
	}

	// Routes
	if out, _, _, err := a.cleanRoom.ExecuteTrusted("ip", []string{"r"}); err == nil && out != "" {
		sections = append(sections, "### Routes\n"+strings.TrimSpace(out))
	}

	// Listening ports
	if out, _, _, err := a.cleanRoom.ExecuteTrusted("ss", []string{"-tuln"}); err == nil && out != "" {
		sections = append(sections, "### Listening Ports\n"+strings.TrimSpace(out))
	}

	// Logged in users
	if out, _, _, err := a.cleanRoom.ExecuteTrusted("who", nil); err == nil && out != "" {
		sections = append(sections, "### Logged In Users\n"+strings.TrimSpace(out))
	}

	if len(sections) == 0 {
		return "Host context collection failed — proceed with manual discovery."
	}

	return strings.Join(sections, "\n\n")
}



func (a *Agent) executeTool(tc ToolCall) (ToolResult, error) {
	args, err := ParseToolArguments(tc.Function.Arguments)
	if err != nil {
		return ToolResult{}, fmt.Errorf("failed to parse args: %w", err)
	}

	a.logger.Debug("Executing tool", zap.String("tool", tc.Function.Name), zap.String("args", tc.Function.Arguments))

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

	// Handle LLM sending full command string (e.g. "cat /etc/passwd") as the command name
	if len(cmdArgs) == 0 && strings.Contains(command, " ") {
		parts := strings.Fields(command)
		command = parts[0]
		cmdArgs = parts[1:]
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
