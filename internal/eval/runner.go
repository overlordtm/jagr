package eval

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/overlordtm/jagr/internal/gateway/models"
)

// RunnerConfig holds runtime parameters for executing an eval run.
type RunnerConfig struct {
	// GatewayBin is the path to the jagr-gateway binary.
	GatewayBin string
	// AgentBin is the path to the jagr-agent binary.
	AgentBin string
	// BaseGatewayConfig is the path to the base gateway.yaml to merge variants into.
	BaseGatewayConfig string
	// APIKEY is the agent API key configured in the gateway.
	APIKey string
	// Target specifies the target mode: "local", "ssh://user@host", etc.
	Target string
	// AgentHostname is the hostname the agent will register as (used to identify its session).
	AgentHostname string
	// DBPath is the path to the gateway SQLite database.
	DBPath string
	// SessionTimeout is how long to wait for an agent session to complete.
	SessionTimeout time.Duration
	// DryRun prints what would be executed without running anything.
	DryRun bool
}

// Runner orchestrates multiple agent runs with different variant configurations.
type Runner struct {
	cfg RunnerConfig
	db  *sql.DB
}

// NewRunner creates a Runner. db must be open and writable.
func NewRunner(cfg RunnerConfig, db *sql.DB) *Runner {
	return &Runner{cfg: cfg, db: db}
}

// resolveAPIKey returns r.cfg.APIKey if set, otherwise reads agent_api_key from the
// base gateway config file. Returns an error if neither is available.
func (r *Runner) resolveAPIKey() (string, error) {
	if r.cfg.APIKey != "" {
		return r.cfg.APIKey, nil
	}
	data, err := os.ReadFile(r.cfg.BaseGatewayConfig)
	if err != nil {
		return "", fmt.Errorf("--api-key not set and cannot read gateway config to resolve it: %w", err)
	}
	var cfg models.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("parse gateway config for api-key: %w", err)
	}
	if cfg.AgentAPIKey == "" {
		return "", fmt.Errorf("--api-key not set and agent_api_key is empty in %s", r.cfg.BaseGatewayConfig)
	}
	return cfg.AgentAPIKey, nil
}

// Run executes all variants in the eval config and returns an EvalRun with scored results.
func (r *Runner) Run(ctx context.Context, evalCfg *EvalConfig, gt *GroundTruth, evalRunID string) (*EvalRun, error) {
	apiKey, err := r.resolveAPIKey()
	if err != nil {
		return nil, err
	}

	configYAML, _ := yaml.Marshal(evalCfg)
	gtYAML, _ := yaml.Marshal(gt)

	if !r.cfg.DryRun {
		if err := CreateEvalRun(r.db, evalRunID, evalCfg.Name, string(configYAML), string(gtYAML)); err != nil {
			return nil, fmt.Errorf("create eval run: %w", err)
		}
	}

	run := &EvalRun{
		ID:        evalRunID,
		Name:      evalCfg.Name,
		StartedAt: time.Now(),
	}

	repeat := evalCfg.Repeat
	if repeat <= 0 {
		repeat = 1
	}

	for _, variant := range evalCfg.Variants {
		for rep := 1; rep <= repeat; rep++ {
			if ctx.Err() != nil {
				return run, ctx.Err()
			}
			result, err := r.runVariant(ctx, variant, gt, evalRunID, rep, apiKey)
			if err != nil {
				return run, fmt.Errorf("variant %s repeat %d: %w", variant.ID, rep, err)
			}
			run.Results = append(run.Results, *result)
		}
	}

	run.CompletedAt = time.Now()
	if !r.cfg.DryRun {
		_ = CompleteEvalRun(r.db, evalRunID)
	}
	return run, nil
}

// runVariant runs one agent session for the given variant and returns a scored VariantResult.
func (r *Runner) runVariant(ctx context.Context, variant Variant, gt *GroundTruth, evalRunID string, rep int, apiKey string) (*VariantResult, error) {
	fmt.Printf("  [%s] repeat %d/%d — starting\n", variant.ID, rep, 1)

	if r.cfg.DryRun {
		fmt.Printf("  [%s] DRY RUN — would spawn gateway+agent with agents: %v\n", variant.ID, agentProfileSummary(variant.Agents))
		return &VariantResult{VariantID: variant.ID, RepeatNum: rep}, nil
	}

	// Build variant gateway config.
	variantConfig, err := r.buildVariantConfig(variant)
	if err != nil {
		return nil, fmt.Errorf("build variant config: %w", err)
	}

	// Write temp config file.
	tmpDir, err := os.MkdirTemp("", "jagr-eval-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "gateway.yaml")
	configData, err := yaml.Marshal(variantConfig)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(configPath, configData, 0600); err != nil {
		return nil, err
	}

	// Pick a free port for the variant gateway.
	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("find free port: %w", err)
	}
	variantConfig.Server.Listen = fmt.Sprintf("127.0.0.1:%d", port)
	// Rewrite with correct listen addr.
	configData, _ = yaml.Marshal(variantConfig)
	if err := os.WriteFile(configPath, configData, 0600); err != nil {
		return nil, err
	}

	gatewayURL := fmt.Sprintf("https://127.0.0.1:%d", port)

	// Start gateway subprocess.
	gatewayCmd := exec.CommandContext(ctx, r.cfg.GatewayBin, "--config="+configPath, "--log-format=console")
	gatewayCmd.Stdout = os.Stderr // gateway logs to stderr of eval tool
	gatewayCmd.Stderr = os.Stderr
	if err := gatewayCmd.Start(); err != nil {
		return nil, fmt.Errorf("start gateway: %w", err)
	}
	defer func() {
		_ = gatewayCmd.Process.Kill()
		_ = gatewayCmd.Wait()
	}()

	// Wait for gateway to be ready.
	if err := waitForHTTP(ctx, gatewayURL+"/v1/models", 15*time.Second); err != nil {
		return nil, fmt.Errorf("gateway did not start: %w", err)
	}

	// Record start time so we can identify the new session.
	startedAt := time.Now()

	// Build agent args.
	agentArgs := []string{
		"--gateway-url=" + gatewayURL,
		"--api-key=" + apiKey,
		"--tls-skip-verify", // eval gateway uses a self-signed cert
	}
	if r.cfg.Target != "" && r.cfg.Target != "local" {
		agentArgs = append(agentArgs, "--remote="+r.cfg.Target)
	}
	if r.cfg.AgentHostname != "" {
		agentArgs = append(agentArgs, "--hostname="+r.cfg.AgentHostname)
	}

	// Run agent subprocess.
	agentCmd := exec.CommandContext(ctx, r.cfg.AgentBin, agentArgs...)
	agentCmd.Stdout = os.Stderr
	agentCmd.Stderr = os.Stderr
	if err := agentCmd.Run(); err != nil {
		// Non-zero exit is not necessarily fatal — the agent may have reported findings.
		fmt.Printf("  [%s] agent exited: %v\n", variant.ID, err)
	}

	// Find the session that was created.
	hostname := r.cfg.AgentHostname
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	sessionID, err := pollForSession(r.db, hostname, startedAt, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("find session: %w", err)
	}

	// Tag the session with eval metadata.
	if err := TagSession(r.db, evalRunID, sessionID, variant.ID, rep); err != nil {
		return nil, fmt.Errorf("tag session: %w", err)
	}

	// Wait for session to close.
	timeout := r.cfg.SessionTimeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	completedAt, err := WaitForSessionClose(r.db, sessionID, timeout)
	if err != nil {
		fmt.Printf("  [%s] warning: %v\n", variant.ID, err)
		completedAt = time.Now()
	}

	// Collect metrics.
	metrics, err := GetSessionMetrics(r.db, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get metrics: %w", err)
	}
	findings, err := GetSessionFindings(r.db, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get findings: %w", err)
	}
	metrics.FindingCount = len(findings)

	// Score findings.
	score := Score(findings, *gt)

	// Capture and clear the system_overview memo so it doesn't leak into the next variant.
	sysOverview, err := FetchSystemOverviewMemo(r.db, sessionID)
	if err != nil {
		fmt.Printf("  [%s] warning: fetch system_overview memo: %v\n", variant.ID, err)
	}
	if err := DeleteSystemOverviewMemos(r.db, sessionID); err != nil {
		fmt.Printf("  [%s] warning: delete system_overview memos: %v\n", variant.ID, err)
	}

	// Persist score.
	scoreJSON, _ := json.Marshal(score)
	_ = SaveScore(r.db, EvalScore{
		EvalRunID: evalRunID,
		SessionID: sessionID,
		VariantID: variant.ID,
		RepeatNum: rep,
		Recall:    score.Recall,
		Precision: score.Precision,
		F1:        score.F1,
		FPRate:    score.FPRate,
		ScoreJSON: string(scoreJSON),
	})
	_ = SaveSystemOverviewMemo(r.db, evalRunID, sessionID, variant.ID, rep, sysOverview)

	fmt.Printf("  [%s] done — F1=%.2f recall=%.2f precision=%.2f cost=$%.4f\n",
		variant.ID, score.F1, score.Recall, score.Precision, metrics.TotalCostUSD)

	return &VariantResult{
		VariantID:      variant.ID,
		Description:    variant.Description,
		RepeatNum:      rep,
		SessionID:      sessionID,
		StartedAt:      startedAt,
		CompletedAt:    completedAt,
		Metrics:        metrics,
		Score:          score,
		SystemOverview: sysOverview,
	}, nil
}

// buildVariantConfig loads the base gateway config and merges in the variant's agent profiles.
func (r *Runner) buildVariantConfig(variant Variant) (*models.Config, error) {
	data, err := os.ReadFile(r.cfg.BaseGatewayConfig)
	if err != nil {
		return nil, fmt.Errorf("read base config: %w", err)
	}
	var cfg models.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse base config: %w", err)
	}

	if cfg.Agents == nil {
		cfg.Agents = make(map[string]models.AgentProfile)
	}
	for role, profile := range variant.Agents {
		cfg.Agents[role] = profile
	}

	// All variants share the same DB as the eval runner so that the runner's
	// session queries (FindSessionForAgent, GetSessionMetrics, etc.) hit the
	// same tables the gateway writes to.
	cfg.Database.Path = RawPath(r.cfg.DBPath)
	// Disable dashboard for eval runs.
	cfg.Dashboard.Enabled = false

	return &cfg, nil
}

// pollForSession waits for a new session for hostname to appear in the DB.
func pollForSession(db *sql.DB, hostname string, after time.Time, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		id, err := FindSessionForAgent(db, hostname, after)
		if err != nil {
			return "", err
		}
		if id != "" {
			return id, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "", fmt.Errorf("timed out waiting for session for hostname %q", hostname)
}

// waitForHTTP polls the given URL until it returns HTTP 2xx or the timeout expires.
func waitForHTTP(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, err := net.DialTimeout("tcp", urlHost(url), time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("HTTP server at %s did not become ready within %s", url, timeout)
}

// freePort finds an available TCP port on localhost.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// urlHost extracts host:port from a URL like "http://127.0.0.1:9000".
func urlHost(u string) string {
	u = trimPrefix(u, "http://")
	u = trimPrefix(u, "https://")
	// Strip path.
	for i, c := range u {
		if c == '/' {
			return u[:i]
		}
	}
	return u
}

func trimPrefix(s, prefix string) string {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}

func agentProfileSummary(agents map[string]models.AgentProfile) string {
	out := ""
	for role, p := range agents {
		out += fmt.Sprintf("%s{model=%s,temp=%.2f} ", role, p.Model, p.Temperature)
	}
	return out
}
