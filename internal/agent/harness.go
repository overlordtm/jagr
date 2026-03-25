package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// JagrHarness orchestrates the multi-phase security investigation.
// It spawns and manages AiAgents, collects findings, and generates reports.
type JagrHarness struct {
	mode            string
	maxIter         int
	maxToolFailures int
	model           string
	objective       string
	outputDir       string
	logger          *zap.Logger
	startTime       time.Time
	concluded       bool

	gateway       *GatewayClient
	cleanRoom     *CleanRoom
	findingsStore *FindingsStore

	investigatorCounter int64 // atomic counter for unique investigator names
}

// NewJagrHarness creates a new JagrHarness for orchestrating an investigation.
func NewJagrHarness(mode string, maxIter, maxToolFailures int, model, objective, outputDir string, logger *zap.Logger, gateway *GatewayClient, cleanRoom *CleanRoom) *JagrHarness {
	return &JagrHarness{
		mode:            mode,
		maxIter:         maxIter,
		maxToolFailures: maxToolFailures,
		model:           model,
		objective:       objective,
		outputDir:       outputDir,
		logger:          logger,
		gateway:         gateway,
		cleanRoom:       cleanRoom,
		findingsStore:   NewFindingsStore(),
		startTime:       time.Now(),
	}
}

// Run executes the full investigation lifecycle: fetch config, run phase agents
// in parallel, run a reporter agent, then generate and submit reports.
func (h *JagrHarness) Run() error {
	if err := h.logEvent("init", map[string]any{
		"workspace": h.cleanRoom.WorkDir,
		"hostname":  h.gateway.Hostname(),
	}); err != nil {
		h.logger.Error("Failed to log init event", zap.Error(err))
	}

	h.gateway.FetchProfiles()
	h.gateway.FetchModelsContextWindows()

	heartbeatDone := make(chan struct{})
	go h.heartbeatLoop(heartbeatDone)
	defer close(heartbeatDone)

	var fatalErr error
	defer func() {
		if fatalErr != nil {
			h.gateway.CloseSession(fatalErr.Error())
		} else {
			h.gateway.CloseSession("")
		}
	}()

	hostContext := h.collectHostContext()

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
			h.logger.Error("Failed to load prompt template", zap.Error(err))
			continue
		}

		objective := fmt.Sprintf("## Target Host Context\n\n%s\n\nBegin your investigation phase.", hostContext)
		agent := NewAiAgent(h, role, role, prompt, objective, GetToolsForRole(role))

		wg.Add(1)
		go func(phase string) {
			defer wg.Done()
			h.logger.Info("Starting Phase", zap.String("phase", phase))
			if err := agent.Run(); err != nil {
				h.logger.Error("Phase agent failed", zap.String("phase", phase), zap.Error(err))
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
	h.logger.Info("Starting Reporter Agent")
	findings := h.findingsStore.GetAll()
	findingsJson, _ := json.MarshalIndent(findings, "", "  ")
	reportPath := filepath.Join(h.outputDir, "report.md")
	reporterObjective := fmt.Sprintf("Synthesize these findings into a detailed markdown report.\nWrite the final report to exactly this path: %s\n\nFindings:\n%s", reportPath, string(findingsJson))
	reporterPrompt, _ := GetPrompt("reporter", nil)
	reporterAgent := NewAiAgent(h, "reporter", "reporter", reporterPrompt, reporterObjective, GetToolsForRole("reporter"))
	if err := reporterAgent.Run(); err != nil {
		h.logger.Error("Reporter agent failed", zap.Error(err))
	}

	h.generateReports("Multi-agent architecture concluded")

	// Validate findings and update statuses at gateway
	updates := h.findingsStore.Validate()
	h.gateway.UpdateFindingStatuses(updates)

	// Submit report to gateway
	h.submitReportToGateway(reportPath)

	// Submit agent settings to gateway
	h.gateway.SubmitAgentSettings()

	return nil
}

// defaultInvestigatorMaxIter is the fallback cap for investigator agents.
const defaultInvestigatorMaxIter = 10

func (h *JagrHarness) runInvestigator(target, contextStr string) error {
	name := fmt.Sprintf("investigator-%d", atomic.AddInt64(&h.investigatorCounter, 1))
	objective := fmt.Sprintf("Analyze target: %s\nContext: %s", target, contextStr)
	prompt, _ := GetPrompt("investigator", nil)
	investigator := NewAiAgent(h, name, "investigator", prompt, objective, GetToolsForRole("investigator"))
	investigator.maxIter = defaultInvestigatorMaxIter
	return investigator.Run()
}

func (h *JagrHarness) heartbeatLoop(done <-chan struct{}) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			h.gateway.SendHeartbeat()
		}
	}
}

func (h *JagrHarness) collectHostContext() string {
	var sections []string

	if out, _, _, err := h.cleanRoom.ExecuteTrusted("uname", []string{"-a"}); err == nil && out != "" {
		sections = append(sections, "### Kernel\n"+strings.TrimSpace(out))
	}
	if out, _, _, err := h.cleanRoom.ExecuteTrusted("cat", []string{"/etc/os-release"}); err == nil && out != "" {
		sections = append(sections, "### Distribution\n"+strings.TrimSpace(out))
	}
	if out, _, _, err := h.cleanRoom.ExecuteTrusted("uptime", nil); err == nil && out != "" {
		sections = append(sections, "### Uptime\n"+strings.TrimSpace(out))
	}
	if out, _, _, err := h.cleanRoom.ExecuteTrusted("ip", []string{"a"}); err == nil && out != "" {
		sections = append(sections, "### Network Interfaces\n"+strings.TrimSpace(out))
	}
	if out, _, _, err := h.cleanRoom.ExecuteTrusted("ip", []string{"r"}); err == nil && out != "" {
		sections = append(sections, "### Routes\n"+strings.TrimSpace(out))
	}
	if out, _, _, err := h.cleanRoom.ExecuteTrusted("ss", []string{"-tuln"}); err == nil && out != "" {
		sections = append(sections, "### Listening Ports\n"+strings.TrimSpace(out))
	}
	if out, _, _, err := h.cleanRoom.ExecuteTrusted("who", nil); err == nil && out != "" {
		sections = append(sections, "### Logged In Users\n"+strings.TrimSpace(out))
	}

	if len(sections) == 0 {
		return "Host context collection failed — proceed with manual discovery."
	}

	return strings.Join(sections, "\n\n")
}

// promptOperator presents proposed tool calls to the operator and waits for approval.
func (h *JagrHarness) promptOperator(toolCalls []ToolCall) (bool, string) {
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
		return false, input
	}
}

func (h *JagrHarness) generateReports(summary string) error {
	tokensIn, tokensOut := h.gateway.GetTokenUsage()
	report := Report{
		Metadata: ReportMetadata{
			Project:     "jagr",
			Version:     "2.0",
			AgentID:     fmt.Sprintf("agent-%s-%s", h.gateway.Hostname(), h.startTime.Format("20060102")),
			Hostname:    h.gateway.Hostname(),
			StartTime:   h.startTime,
			EndTime:     time.Now(),
			Mode:        h.mode,
			Model:       h.model,
			TotalTokens: tokensIn + tokensOut,
		},
		Findings: h.findingsStore.GetAll(),
		Summary:  h.findingsStore.GetSummary(),
	}

	ocsfPath := filepath.Join(h.outputDir, "findings.json")
	if err := saveJSON(ocsfPath, report); err != nil {
		return err
	}

	return nil
}

func (h *JagrHarness) generateMarkdownReport(report Report) string {
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

func (h *JagrHarness) logEvent(eventType string, data map[string]any) error {
	event := Event{
		Timestamp: time.Now().UTC(),
		Type:      eventType,
		Data:      data,
	}

	eventPath := filepath.Join(h.outputDir, "jagr-events.jsonl")
	f, err := os.OpenFile(eventPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	eventJSON, _ := json.Marshal(event)
	_, err = f.WriteString(string(eventJSON) + "\n")
	return err
}

func (h *JagrHarness) submitReportToGateway(reportPath string) {
	content, err := os.ReadFile(reportPath)
	if err != nil {
		h.logger.Error("Failed to read report for gateway submission", zap.Error(err))
		return
	}
	h.gateway.SubmitReport(string(content))
}

func saveJSON(path string, data any) error {
	jsonBytes, _ := json.MarshalIndent(data, "", "  ")
	return os.WriteFile(path, jsonBytes, 0644)
}
