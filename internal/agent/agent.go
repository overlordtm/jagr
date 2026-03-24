package agent

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	toolFailureCounts   map[string]int
	profiles            map[string]AgentProfile
	defaultMaxIter      int // from gateway config, 0 means not set
	modelsContextWindow map[string]int
	mu                  sync.Mutex // protects profiles, findings, totalTokensIn, totalTokensOut, modelsContextWindow
}

type AgentProfile struct {
	Model         string  `json:"model"`
	Temperature   float32 `json:"temperature"`
	TopP          float32 `json:"top_p"`
	TopK          int     `json:"top_k"`
	MaxIterations int     `json:"max_iterations"`
}

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type Finding struct {
	ID         string   `json:"id"`
	Type       string   `json:"type"`
	Severity   string   `json:"severity"`
	Observable string   `json:"observable"`
	Analysis   string   `json:"analysis"`
	Evidence   []string `json:"evidence"`
	Status     string   `json:"status"`
}

type FindingSummary struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Info     int `json:"info"`
}

// DefaultHTTPTimeout is the default timeout for HTTP requests to the gateway.
const DefaultHTTPTimeout = 120 * time.Second

// maxGatewayRetries is the number of retry attempts for transient gateway errors (5xx, network).
const maxGatewayRetries = 3

// maxRateLimitRetries is the number of retry attempts for rate limit (429) errors.
// Higher than maxGatewayRetries because rate limits are expected with concurrent agents.
const maxRateLimitRetries = 10

// rateLimitBaseBackoff is the base backoff duration for rate limit retries.
const rateLimitBaseBackoff = 5 * time.Second

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
		startTime:           time.Now(),
		toolFailureCounts:   make(map[string]int),
		profiles:            make(map[string]AgentProfile),
		modelsContextWindow: make(map[string]int),
	}, nil
}

func (a *Agent) RegisterProfile(role string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.profiles == nil {
		a.profiles = make(map[string]AgentProfile)
	}
	if _, exists := a.profiles[role]; !exists {
		a.profiles[role] = AgentProfile{
			Model: a.model,
		}
	}
}

func (a *Agent) GetProfile(role string) (AgentProfile, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.profiles == nil {
		return AgentProfile{}, false
	}
	p, ok := a.profiles[role]
	return p, ok
}

func (a *Agent) Run() error {
	if err := a.logEvent("init", map[string]any{
		"workspace": a.cleanRoom.WorkDir,
		"hostname":  a.hostname(),
	}); err != nil {
		a.logger.Error("Failed to log init event", zap.Error(err))
	}

	a.fetchProfiles()
	a.fetchModelsContextWindows()

	heartbeatDone := make(chan struct{})
	go a.heartbeatLoop(heartbeatDone)
	defer close(heartbeatDone)

	var fatalErr error
	defer func() {
		if fatalErr != nil {
			a.closeSessionWithError(fatalErr.Error())
		} else {
			a.closeSession()
		}
	}()

	hostContext := a.collectHostContext()

	phases := []string{
		"UserAccess",
		"Persistence",
		"Network",
		"Filesystem",
		"LogAnalysis",
	}

	var wg sync.WaitGroup
	var phaseErrors []string
	var phaseErrMu sync.Mutex
	for _, p := range phases {
		role := "phase_" + p
		prompt, err := GetPrompt(role, map[string]interface{}{})
		if err != nil {
			a.logger.Error("Failed to load prompt template", zap.Error(err))
			continue
		}

		objective := fmt.Sprintf("## Target Host Context\n\n%s\n\nBegin your investigation phase.", hostContext)
		agent := NewSubAgent(a, role, prompt, objective, GetToolsForRole(role))

		wg.Add(1)
		go func(phase string) {
			defer wg.Done()
			a.logger.Info("Starting Phase", zap.String("phase", phase))
			if err := agent.Run(); err != nil {
				a.logger.Error("Phase agent failed", zap.String("phase", phase), zap.Error(err))
				phaseErrMu.Lock()
				phaseErrors = append(phaseErrors, fmt.Sprintf("%s: %s", phase, err.Error()))
				phaseErrMu.Unlock()
			}
		}(p)
	}
	wg.Wait()

	// If ALL phases failed, treat as fatal
	if len(phaseErrors) == len(phases) {
		fatalErr = fmt.Errorf("all phases failed: %s", strings.Join(phaseErrors, "; "))
		return fatalErr
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

	// Validate findings and update statuses at gateway
	a.validateAndUpdateFindings()

	// Submit report to gateway
	a.submitReportToGateway(reportPath)

	// Submit agent settings to gateway
	a.submitAgentSettingsToGateway()

	return nil
}

// defaultInvestigatorMaxIter is the fallback cap for investigator sub-agents
// when no max_iterations is configured in the profile.
const defaultInvestigatorMaxIter = 10

func (a *Agent) runInvestigator(target, contextStr string) error {
	objective := fmt.Sprintf("Analyze target: %s\nContext: %s", target, contextStr)
	prompt, _ := GetPrompt("investigator", nil)
	investigator := NewSubAgent(a, "investigator", prompt, objective, GetToolsForRole("investigator"))
	investigator.maxIter = defaultInvestigatorMaxIter
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

	var configResp struct {
		Agents               map[string]AgentProfile `json:"agents"`
		DefaultMaxIterations int                     `json:"default_max_iterations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&configResp); err == nil {
		a.mu.Lock()
		if a.profiles == nil {
			a.profiles = make(map[string]AgentProfile)
		}
		for k, v := range configResp.Agents {
			a.profiles[k] = v
		}
		if configResp.DefaultMaxIterations > 0 {
			a.defaultMaxIter = configResp.DefaultMaxIterations
		}
		count := len(a.profiles)
		a.mu.Unlock()
		a.logger.Info("Loaded agent profiles from gateway", zap.Int("count", count), zap.Int("default_max_iterations", configResp.DefaultMaxIterations))
	} else {
		a.logger.Debug("Failed to decode agent profiles", zap.Error(err))
	}
}

func (a *Agent) fetchModelsContextWindows() {
	req, err := http.NewRequest("GET", a.gatewayURL+"/v1/models", nil)
	if err != nil {
		a.logger.Debug("Failed to create models fetch request", zap.Error(err))
		return
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("X-Hostname", a.hostname())

	resp, err := a.httpClient.Do(req)
	if err != nil {
		a.logger.Debug("Failed to fetch models", zap.Error(err))
		return
	}
	defer resp.Body.Close()

	var modelsResp struct {
		Data []struct {
			ID               string `json:"id"`
			MaxContextWindow int    `json:"max_context_window"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&modelsResp); err == nil {
		a.mu.Lock()
		for _, m := range modelsResp.Data {
			if m.MaxContextWindow > 0 {
				a.modelsContextWindow[m.ID] = m.MaxContextWindow
			}
		}
		a.mu.Unlock()
		a.logger.Info("Loaded model context windows from gateway")
	} else {
		a.logger.Debug("Failed to decode models", zap.Error(err))
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
	a.closeSessionWithError("")
}

func (a *Agent) closeSessionWithError(errMsg string) {
	var body io.Reader
	if errMsg != "" {
		bodyBytes, _ := json.Marshal(map[string]string{"error": errMsg})
		body = strings.NewReader(string(bodyBytes))
	}

	req, err := http.NewRequest("POST", a.gatewayURL+"/v1/sessions/close", body)
	if err != nil {
		a.logger.Error("Failed to create close session request", zap.Error(err))
		return
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("X-Hostname", a.hostname())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		a.logger.Error("Failed to close session", zap.Error(err))
		return
	}
	resp.Body.Close()

	if errMsg != "" {
		a.logger.Error("Session closed with error on gateway", zap.String("error", errMsg))
	} else {
		a.logger.Info("Session closed on gateway")
	}
}

// doGatewayRequest performs an HTTP request to the gateway with retry and backoff
// for transient errors (5xx, network errors, rate limits). Terminal errors like
// token budget exceeded are returned immediately without retry.
func (a *Agent) doGatewayRequest(req *http.Request, reqBody []byte) (*http.Response, error) {
	var lastErr error
	rateLimitAttempts := 0

	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			a.logger.Warn("Retrying gateway request",
				zap.Int("attempt", attempt),
				zap.Error(lastErr))
		}

		// Clone the request with a fresh body for each attempt
		retryReq, err := http.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), strings.NewReader(string(reqBody)))
		if err != nil {
			return nil, err
		}
		retryReq.Header = req.Header

		resp, err := a.httpClient.Do(retryReq)
		if err != nil {
			if attempt >= maxGatewayRetries {
				return nil, fmt.Errorf("gateway request failed after %d retries: %w", attempt, err)
			}
			lastErr = err
			backoff := time.Duration(1<<attempt) * time.Second // 1s, 2s, 4s
			time.Sleep(backoff)
			continue
		}

		// 5xx: server error, retry with exponential backoff
		if resp.StatusCode >= 500 {
			bodyBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if attempt >= maxGatewayRetries {
				return nil, fmt.Errorf("gateway returned status %d after %d retries: %s", resp.StatusCode, attempt, string(bodyBytes))
			}
			lastErr = fmt.Errorf("gateway returned status %d: %s", resp.StatusCode, string(bodyBytes))
			backoff := time.Duration(1<<attempt) * time.Second
			time.Sleep(backoff)
			continue
		}

		// 429: parse error code to distinguish rate limit from terminal errors
		if resp.StatusCode == http.StatusTooManyRequests {
			bodyBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			// Parse the error response to check the code
			var errResp struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(bodyBytes, &errResp) == nil && errResp.Error.Code == "token_budget_exceeded" {
				// Terminal: token budget is exhausted, retrying won't help
				return nil, fmt.Errorf("token budget exceeded: %s", errResp.Error.Message)
			}

			// Rate limit: retry with longer backoff + jitter
			rateLimitAttempts++
			if rateLimitAttempts > maxRateLimitRetries {
				return nil, fmt.Errorf("rate limited after %d retries: %s", rateLimitAttempts, string(bodyBytes))
			}
			lastErr = fmt.Errorf("rate limited: %s", string(bodyBytes))
			backoff := rateLimitBaseBackoff * time.Duration(rateLimitAttempts)
			a.logger.Warn("Rate limited by gateway, backing off",
				zap.Int("rate_limit_attempt", rateLimitAttempts),
				zap.Duration("backoff", backoff))
			time.Sleep(backoff)
			continue
		}

		return resp, nil
	}
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

	a.mu.Lock()
	// Deduplicate by observable
	for _, existing := range a.findings {
		if existing.Observable == finding.Observable {
			a.mu.Unlock()
			return ToolResult{
				ToolID:  tc.ID,
				Name:    tc.Function.Name,
				Content: fmt.Sprintf("Finding for %q already submitted (id: %s). Do not resubmit. Call conclude to finish your investigation.", finding.Observable, existing.ID),
			}, nil
		}
	}
	// Auto-assign ID if empty
	if finding.ID == "" {
		finding.ID = fmt.Sprintf("finding-%d", len(a.findings)+1)
	}
	finding.Status = "preliminary"
	a.findings = append(a.findings, finding)
	a.mu.Unlock()

	// Submit immediately to gateway as preliminary
	a.submitSingleFindingToGateway(finding)

	a.logger.Info("Finding submitted",
		zap.String("id", finding.ID),
		zap.String("type", finding.Type),
		zap.String("severity", finding.Severity))

	return ToolResult{
		ToolID:  tc.ID,
		Name:    tc.Function.Name,
		Content: fmt.Sprintf("Finding %s submitted successfully. If you have no more findings, call conclude.", finding.ID),
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

func (a *Agent) submitSingleFindingToGateway(f Finding) {
	payload := map[string]any{"findings": []Finding{f}}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", a.gatewayURL+"/v1/findings", strings.NewReader(string(body)))
	if err != nil {
		a.logger.Error("Failed to create finding request", zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("X-Hostname", a.hostname())

	resp, err := a.httpClient.Do(req)
	if err != nil {
		a.logger.Error("Failed to submit finding to gateway", zap.Error(err))
		return
	}
	resp.Body.Close()
	a.logger.Info("Finding submitted to gateway", zap.String("id", f.ID), zap.String("status", f.Status))
}

func (a *Agent) validateAndUpdateFindings() {
	if len(a.findings) == 0 {
		return
	}

	// Deduplicate by observable — first occurrence wins, later ones marked duplicate
	seen := map[string]string{} // observable -> finding ID of first occurrence
	type statusUpdate struct {
		FindingID string `json:"finding_id"`
		Status    string `json:"status"`
	}
	var updates []statusUpdate

	for i := range a.findings {
		f := &a.findings[i]
		if firstID, exists := seen[f.Observable]; exists {
			f.Status = "duplicate"
			a.logger.Info("Finding marked as duplicate",
				zap.String("id", f.ID),
				zap.String("duplicate_of", firstID))
		} else if f.Type == "incomplete_investigation" && f.Severity == "info" {
			f.Status = "invalid"
		} else {
			f.Status = "valid"
		}
		seen[f.Observable] = f.ID
		updates = append(updates, statusUpdate{FindingID: f.ID, Status: f.Status})
	}

	// Send bulk status update to gateway
	payload := map[string]any{"findings": updates}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("PATCH", a.gatewayURL+"/v1/findings/status", strings.NewReader(string(body)))
	if err != nil {
		a.logger.Error("Failed to create findings status update request", zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("X-Hostname", a.hostname())

	resp, err := a.httpClient.Do(req)
	if err != nil {
		a.logger.Error("Failed to update finding statuses at gateway", zap.Error(err))
		return
	}
	resp.Body.Close()
	a.logger.Info("Finding statuses updated at gateway", zap.Int("count", len(updates)))
}

func (a *Agent) submitReportToGateway(reportPath string) {
	content, err := os.ReadFile(reportPath)
	if err != nil {
		a.logger.Error("Failed to read report for gateway submission", zap.Error(err))
		return
	}

	payload := map[string]string{"content": string(content)}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", a.gatewayURL+"/v1/report", strings.NewReader(string(body)))
	if err != nil {
		a.logger.Error("Failed to create report request", zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("X-Hostname", a.hostname())

	resp, err := a.httpClient.Do(req)
	if err != nil {
		a.logger.Error("Failed to submit report to gateway", zap.Error(err))
		return
	}
	resp.Body.Close()
	a.logger.Info("Report submitted to gateway")
}

func (a *Agent) submitAgentSettingsToGateway() {
	a.mu.Lock()
	if len(a.profiles) == 0 {
		a.mu.Unlock()
		return
	}

	payload := map[string]any{"agents": a.profiles}
	body, _ := json.Marshal(payload)
	count := len(a.profiles)
	a.mu.Unlock()

	req, err := http.NewRequest("POST", a.gatewayURL+"/v1/agent-settings", strings.NewReader(string(body)))
	if err != nil {
		a.logger.Error("Failed to create agent settings request", zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("X-Hostname", a.hostname())

	resp, err := a.httpClient.Do(req)
	if err != nil {
		a.logger.Error("Failed to submit agent settings to gateway", zap.Error(err))
		return
	}
	resp.Body.Close()
	a.logger.Info("Agent settings submitted to gateway", zap.Int("count", count))
}
