package agent

import (
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

type pendingInvestigation struct {
	target     string
	contextStr string
}

// JagrHarness orchestrates the multi-phase security investigation.
// It spawns and manages AiAgents, collects findings, and generates reports.
type JagrHarness struct {
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

	pendingMu           sync.Mutex
	pendingInvestigations []pendingInvestigation
	investigatedTargets   map[string]bool
}

// NewJagrHarness creates a new JagrHarness for orchestrating an investigation.
func NewJagrHarness(mode string, maxIter, maxToolFailures int, model, objective, outputDir string, logger *zap.Logger, gateway *GatewayClient, cleanRoom *CleanRoom) *JagrHarness {
	return &JagrHarness{
		maxIter:             maxIter,
		maxToolFailures:     maxToolFailures,
		model:               model,
		objective:           objective,
		outputDir:           outputDir,
		logger:              logger,
		gateway:             gateway,
		cleanRoom:           cleanRoom,
		findingsStore:       NewFindingsStore(),
		startTime:           time.Now(),
		investigatedTargets: make(map[string]bool),
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
	h.gateway.FetchSkills()

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

	systemOverview := h.runSystemOverview(hostContext)

	phases := []string{
		"UserAccess",
		"Persistence",
		"Network",
		"Filesystem",
		"LogAnalysis",
		"ConfigAudit",
		"ThreatIntel",
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

		var objBuilder strings.Builder

		if systemOverview != "" {
			objBuilder.WriteString("\n\n## System Overview\n\n")
			objBuilder.WriteString(systemOverview)
		} else {
			objBuilder.WriteString("## Target Host Context\n\n")
			objBuilder.WriteString(hostContext)
		}
		objBuilder.WriteString("\n\nBegin your investigation phase.")
		objective := objBuilder.String()
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

	// All phase agents have concluded — now run deduplicated investigators.
	h.runQueuedInvestigations()

	// If ALL phases failed, treat as fatal
	if len(phaseErrors) == len(phases) {
		fatalErr = fmt.Errorf("all phases failed: %s", strings.Join(phaseErrors, "; "))
		return fatalErr
	}

	// Validate and filter findings before the reporter sees them.
	// This ensures jagr's own artifacts (busybox symlinks, workdir paths) are
	// marked invalid and excluded from the report narrative.
	validateUpdates := h.findingsStore.Validate()
	selfUpdates := h.findingsStore.FilterSelfArtifacts(h.cleanRoom.WorkDir)
	h.gateway.UpdateFindingStatuses(append(validateUpdates, selfUpdates...))

	// Reporter Agent — receives only reportable (non-invalid) findings
	h.logger.Info("Starting Reporter Agent")
	findings := h.findingsStore.GetReportable()
	findingsJson, _ := json.MarshalIndent(findings, "", "  ")
	reportPath := filepath.Join(h.outputDir, "report.md")
	reporterObjective := fmt.Sprintf("Synthesize these findings into a detailed markdown report.\nWrite the final report to exactly this path: %s\n\nFindings:\n%s", reportPath, string(findingsJson))
	reporterPrompt, _ := GetPrompt("reporter", nil)
	reporterAgent := NewAiAgent(h, "reporter", "reporter", reporterPrompt, reporterObjective, GetToolsForRole("reporter"))
	if err := reporterAgent.Run(); err != nil {
		h.logger.Error("Reporter agent failed", zap.Error(err))
	}

	h.generateReports("Multi-agent architecture concluded")

	// Submit report to gateway
	h.submitReportToGateway(reportPath)

	// Submit agent settings to gateway
	h.gateway.SubmitAgentSettings()

	return nil
}

// defaultInvestigatorMaxIter is the fallback cap for investigator agents.
const defaultInvestigatorMaxIter = 50

// runSystemOverview ensures a system_overview memo exists for the host.
// It first checks for an existing memo; if none is found, it launches a
// dedicated system_overview agent to profile the host and write one.
// Returns the overview content, or empty string on failure.
func (h *JagrHarness) runSystemOverview(hostContext string) string {
	const memoType = "system_overview"

	content, found, err := h.gateway.FetchMemoByType(memoType, h.gateway.Hostname())
	if err != nil {
		h.logger.Warn("Failed to fetch system_overview memo", zap.Error(err))
	}
	if found {
		h.logger.Info("System overview memo found, loading into context")
		return content
	}

	h.logger.Info("No system_overview memo found, launching system_overview agent")
	prompt, err := GetPrompt("system_overview", nil)
	if err != nil {
		h.logger.Error("Failed to load system_overview prompt", zap.Error(err))
		return ""
	}
	objective := fmt.Sprintf("## Target Host Context\n\n%s\n\nDetermine the purpose of this system and identify all network-exposed services. Write your findings as a memo with type 'system_overview'.", hostContext)
	agent := NewAiAgent(h, "system_overview", "system_overview", prompt, objective, GetToolsForRole("system_overview"))
	if profile, ok := h.gateway.GetProfile("system_overview"); !ok || profile.MaxIterations == 0 {
		if h.gateway.GetDefaultMaxIter() == 0 {
			agent.maxIter = 10
		}
	}
	if err := agent.Run(); err != nil {
		h.logger.Error("system_overview agent failed", zap.Error(err))
		return ""
	}

	content, found, err = h.gateway.FetchMemoByType(memoType, h.gateway.Hostname())
	if err != nil {
		h.logger.Warn("Failed to fetch system_overview memo after agent run", zap.Error(err))
		return ""
	}
	if !found {
		h.logger.Warn("system_overview agent completed but memo not found")
		return ""
	}
	return content
}

// queueInvestigation enqueues a target for investigation after all phases complete.
// It is safe to call from multiple goroutines. Duplicate targets are silently dropped.
// Returns true if the target was newly enqueued, false if it was a duplicate or self-target.
func (h *JagrHarness) queueInvestigation(target, contextStr string) bool {
	if h.isSelfTarget(target) {
		h.logger.Info("Skipping self-investigation target", zap.String("target", target))
		return false
	}

	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()

	if h.investigatedTargets[target] {
		h.logger.Info("Duplicate investigation target skipped", zap.String("target", target))
		return false
	}
	h.investigatedTargets[target] = true
	h.pendingInvestigations = append(h.pendingInvestigations, pendingInvestigation{target: target, contextStr: contextStr})
	h.logger.Info("Queued investigation target", zap.String("target", target))
	return true
}

// runQueuedInvestigations drains the pending queue and runs all investigators in parallel.
// Must be called only after all phase agents have completed.
func (h *JagrHarness) runQueuedInvestigations() {
	h.pendingMu.Lock()
	targets := h.pendingInvestigations
	h.pendingInvestigations = nil
	h.pendingMu.Unlock()

	if len(targets) == 0 {
		return
	}

	h.logger.Info("Running queued investigations", zap.Int("count", len(targets)))
	var wg sync.WaitGroup
	for _, inv := range targets {
		inv := inv
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("investigator-%d", atomic.AddInt64(&h.investigatorCounter, 1))
			objective := fmt.Sprintf("Analyze target: %s\nContext: %s", inv.target, inv.contextStr)
			prompt, _ := GetPrompt("investigator", nil)
			investigator := NewAiAgent(h, name, "investigator", prompt, objective, GetToolsForRole("investigator"))
			if profile, ok := h.gateway.GetProfile("investigator"); !ok || profile.MaxIterations == 0 {
				if h.gateway.GetDefaultMaxIter() == 0 {
					investigator.maxIter = defaultInvestigatorMaxIter
				}
			}
			if err := investigator.Run(); err != nil {
				h.logger.Error("Investigator failed", zap.String("target", inv.target), zap.Error(err))
			}
		}()
	}
	wg.Wait()
}

// isSelfTarget returns true if the target path refers to the agent's own
// binary or any file inside the clean room work directory.
func (h *JagrHarness) isSelfTarget(target string) bool {
	// Check against clean room directory
	if h.cleanRoom != nil && h.cleanRoom.WorkDir != "" {
		if strings.HasPrefix(target, h.cleanRoom.WorkDir) {
			return true
		}
	}

	// Check against our own executable path
	if selfPath, err := os.Executable(); err == nil {
		selfPath, _ = filepath.EvalSymlinks(selfPath)
		resolved, _ := filepath.EvalSymlinks(target)
		if selfPath == resolved {
			return true
		}
	}

	return false
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
	if out, _, _, err := h.cleanRoom.ExecuteTrusted("netstat", []string{"-tuln"}); err == nil && out != "" {
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
