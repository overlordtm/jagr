package agent

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// DefaultHTTPTimeout is the default timeout for HTTP requests to the gateway.
const DefaultHTTPTimeout = 120 * time.Second

// maxGatewayRetries is the number of retry attempts for transient gateway errors (5xx, network).
const maxGatewayRetries = 3

// maxRateLimitRetries is the number of retry attempts for rate limit (429) errors.
// Higher than maxGatewayRetries because rate limits are expected with concurrent agents.
const maxRateLimitRetries = 10

// rateLimitBaseBackoff is the base backoff duration for rate limit retries.
const rateLimitBaseBackoff = 5 * time.Second

// GatewayClient handles all HTTP communication with the JAGR gateway,
// including retry logic, rate limiting, and token accounting.
// All AiAgents share a single GatewayClient instance.
type GatewayClient struct {
	gatewayURL string
	apiKey     string
	hostname   string
	httpClient *http.Client
	logger     *zap.Logger

	// Token accounting
	totalTokensIn  int
	totalTokensOut int

	// Cached config from gateway
	profiles            map[string]AgentProfile
	defaultMaxIter      int
	modelsContextWindow map[string]int
	defaultModel        string

	mu sync.Mutex
}

// NewGatewayClient creates a new GatewayClient for communicating with the gateway.
func NewGatewayClient(gatewayURL, apiKey, hostname, defaultModel string, logger *zap.Logger, tlsSkipVerify bool, httpTimeout time.Duration) *GatewayClient {
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

	return &GatewayClient{
		gatewayURL:          gatewayURL,
		apiKey:              apiKey,
		hostname:            hostname,
		defaultModel:        defaultModel,
		httpClient:          httpClient,
		logger:              logger,
		profiles:            make(map[string]AgentProfile),
		modelsContextWindow: make(map[string]int),
	}
}

// doRequest performs an HTTP request with retry and backoff for transient errors.
func (gc *GatewayClient) doRequest(req *http.Request, reqBody []byte) (*http.Response, error) {
	var lastErr error
	rateLimitAttempts := 0

	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			gc.logger.Warn("Retrying gateway request",
				zap.Int("attempt", attempt),
				zap.Error(lastErr))
		}

		retryReq, err := http.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), strings.NewReader(string(reqBody)))
		if err != nil {
			return nil, err
		}
		retryReq.Header = req.Header

		resp, err := gc.httpClient.Do(retryReq)
		if err != nil {
			if attempt >= maxGatewayRetries {
				return nil, fmt.Errorf("gateway request failed after %d retries: %w", attempt, err)
			}
			lastErr = err
			backoff := time.Duration(1<<attempt) * time.Second
			time.Sleep(backoff)
			continue
		}

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

		if resp.StatusCode == http.StatusTooManyRequests {
			bodyBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			var errResp struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(bodyBytes, &errResp) == nil && errResp.Error.Code == "token_budget_exceeded" {
				return nil, fmt.Errorf("token budget exceeded: %s", errResp.Error.Message)
			}

			rateLimitAttempts++
			if rateLimitAttempts > maxRateLimitRetries {
				return nil, fmt.Errorf("rate limited after %d retries: %s", rateLimitAttempts, string(bodyBytes))
			}
			lastErr = fmt.Errorf("rate limited: %s", string(bodyBytes))
			backoff := rateLimitBaseBackoff * time.Duration(rateLimitAttempts)
			gc.logger.Warn("Rate limited by gateway, backing off",
				zap.Int("rate_limit_attempt", rateLimitAttempts),
				zap.Duration("backoff", backoff))
			time.Sleep(backoff)
			continue
		}

		return resp, nil
	}
}

// ChatCompletion sends a chat completion request to the gateway and returns the decoded response.
func (gc *GatewayClient) ChatCompletion(reqBytes []byte, role string) (map[string]any, error) {
	req, _ := http.NewRequest("POST", gc.gatewayURL+"/v1/chat/completions", strings.NewReader(string(reqBytes)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+gc.apiKey)
	req.Header.Set("X-Hostname", gc.hostname)
	req.Header.Set("X-Sub-Agent-Role", role)

	resp, err := gc.doRequest(req, reqBytes)
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

// FetchProfiles loads agent profiles and default max iterations from the gateway.
func (gc *GatewayClient) FetchProfiles() {
	req, err := http.NewRequest("GET", gc.gatewayURL+"/v1/agent/config", nil)
	if err != nil {
		gc.logger.Debug("Failed to create profile fetch request", zap.Error(err))
		return
	}
	req.Header.Set("Authorization", "Bearer "+gc.apiKey)
	req.Header.Set("X-Hostname", gc.hostname)

	resp, err := gc.httpClient.Do(req)
	if err != nil {
		gc.logger.Debug("Failed to fetch agent profiles", zap.Error(err))
		return
	}
	defer resp.Body.Close()

	var configResp struct {
		Agents               map[string]AgentProfile `json:"agents"`
		DefaultMaxIterations int                     `json:"default_max_iterations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&configResp); err == nil {
		gc.mu.Lock()
		for k, v := range configResp.Agents {
			gc.profiles[k] = v
		}
		if configResp.DefaultMaxIterations > 0 {
			gc.defaultMaxIter = configResp.DefaultMaxIterations
		}
		count := len(gc.profiles)
		gc.mu.Unlock()
		gc.logger.Info("Loaded agent profiles from gateway", zap.Int("count", count), zap.Int("default_max_iterations", configResp.DefaultMaxIterations))
	} else {
		gc.logger.Debug("Failed to decode agent profiles", zap.Error(err))
	}
}

// FetchModelsContextWindows loads model context window sizes from the gateway.
func (gc *GatewayClient) FetchModelsContextWindows() {
	req, err := http.NewRequest("GET", gc.gatewayURL+"/v1/models", nil)
	if err != nil {
		gc.logger.Debug("Failed to create models fetch request", zap.Error(err))
		return
	}
	req.Header.Set("Authorization", "Bearer "+gc.apiKey)
	req.Header.Set("X-Hostname", gc.hostname)

	resp, err := gc.httpClient.Do(req)
	if err != nil {
		gc.logger.Debug("Failed to fetch models", zap.Error(err))
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
		gc.mu.Lock()
		for _, m := range modelsResp.Data {
			if m.MaxContextWindow > 0 {
				gc.modelsContextWindow[m.ID] = m.MaxContextWindow
			}
		}
		gc.mu.Unlock()
		gc.logger.Info("Loaded model context windows from gateway")
	} else {
		gc.logger.Debug("Failed to decode models", zap.Error(err))
	}
}

// SendHeartbeat sends a keep-alive heartbeat to the gateway.
func (gc *GatewayClient) SendHeartbeat() {
	req, err := http.NewRequest("POST", gc.gatewayURL+"/v1/heartbeat", nil)
	if err != nil {
		gc.logger.Debug("Failed to create heartbeat request", zap.Error(err))
		return
	}
	req.Header.Set("Authorization", "Bearer "+gc.apiKey)
	req.Header.Set("X-Hostname", gc.hostname)

	resp, err := gc.httpClient.Do(req)
	if err != nil {
		gc.logger.Debug("Heartbeat failed", zap.Error(err))
		return
	}
	resp.Body.Close()
}

// CloseSession closes the current session on the gateway, optionally with an error message.
func (gc *GatewayClient) CloseSession(errMsg string) {
	var body io.Reader
	if errMsg != "" {
		bodyBytes, _ := json.Marshal(map[string]string{"error": errMsg})
		body = strings.NewReader(string(bodyBytes))
	}

	req, err := http.NewRequest("POST", gc.gatewayURL+"/v1/sessions/close", body)
	if err != nil {
		gc.logger.Error("Failed to create close session request", zap.Error(err))
		return
	}
	req.Header.Set("Authorization", "Bearer "+gc.apiKey)
	req.Header.Set("X-Hostname", gc.hostname)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := gc.httpClient.Do(req)
	if err != nil {
		gc.logger.Error("Failed to close session", zap.Error(err))
		return
	}
	resp.Body.Close()

	if errMsg != "" {
		gc.logger.Error("Session closed with error on gateway", zap.String("error", errMsg))
	} else {
		gc.logger.Info("Session closed on gateway")
	}
}

// SubmitFinding sends a single finding to the gateway.
func (gc *GatewayClient) SubmitFinding(f Finding) {
	payload := map[string]any{"findings": []Finding{f}}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", gc.gatewayURL+"/v1/findings", strings.NewReader(string(body)))
	if err != nil {
		gc.logger.Error("Failed to create finding request", zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+gc.apiKey)
	req.Header.Set("X-Hostname", gc.hostname)

	resp, err := gc.httpClient.Do(req)
	if err != nil {
		gc.logger.Error("Failed to submit finding to gateway", zap.Error(err))
		return
	}
	resp.Body.Close()
	gc.logger.Info("Finding submitted to gateway", zap.String("id", f.ID), zap.String("status", f.Status))
}

// UpdateFindingStatuses sends bulk finding status updates to the gateway.
func (gc *GatewayClient) UpdateFindingStatuses(updates []StatusUpdate) {
	if len(updates) == 0 {
		return
	}

	payload := map[string]any{"findings": updates}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("PATCH", gc.gatewayURL+"/v1/findings/status", strings.NewReader(string(body)))
	if err != nil {
		gc.logger.Error("Failed to create findings status update request", zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+gc.apiKey)
	req.Header.Set("X-Hostname", gc.hostname)

	resp, err := gc.httpClient.Do(req)
	if err != nil {
		gc.logger.Error("Failed to update finding statuses at gateway", zap.Error(err))
		return
	}
	resp.Body.Close()
	gc.logger.Info("Finding statuses updated at gateway", zap.Int("count", len(updates)))
}

// SubmitReport sends the final markdown report to the gateway.
func (gc *GatewayClient) SubmitReport(content string) {
	payload := map[string]string{"content": content}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", gc.gatewayURL+"/v1/report", strings.NewReader(string(body)))
	if err != nil {
		gc.logger.Error("Failed to create report request", zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+gc.apiKey)
	req.Header.Set("X-Hostname", gc.hostname)

	resp, err := gc.httpClient.Do(req)
	if err != nil {
		gc.logger.Error("Failed to submit report to gateway", zap.Error(err))
		return
	}
	resp.Body.Close()
	gc.logger.Info("Report submitted to gateway")
}

// SubmitAgentSettings sends collected agent profiles back to the gateway.
func (gc *GatewayClient) SubmitAgentSettings() {
	gc.mu.Lock()
	if len(gc.profiles) == 0 {
		gc.mu.Unlock()
		return
	}

	payload := map[string]any{"agents": gc.profiles}
	body, _ := json.Marshal(payload)
	count := len(gc.profiles)
	gc.mu.Unlock()

	req, err := http.NewRequest("POST", gc.gatewayURL+"/v1/agent-settings", strings.NewReader(string(body)))
	if err != nil {
		gc.logger.Error("Failed to create agent settings request", zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+gc.apiKey)
	req.Header.Set("X-Hostname", gc.hostname)

	resp, err := gc.httpClient.Do(req)
	if err != nil {
		gc.logger.Error("Failed to submit agent settings to gateway", zap.Error(err))
		return
	}
	resp.Body.Close()
	gc.logger.Info("Agent settings submitted to gateway", zap.Int("count", count))
}

// AddTokenUsage adds token usage from a completion response (thread-safe).
func (gc *GatewayClient) AddTokenUsage(promptTokens, completionTokens int) {
	gc.mu.Lock()
	gc.totalTokensIn += promptTokens
	gc.totalTokensOut += completionTokens
	gc.mu.Unlock()
}

// GetTokenUsage returns total input and output token counts.
func (gc *GatewayClient) GetTokenUsage() (int, int) {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	return gc.totalTokensIn, gc.totalTokensOut
}

// RegisterProfile registers a default profile for a role if one doesn't exist.
func (gc *GatewayClient) RegisterProfile(role string) {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	if _, exists := gc.profiles[role]; !exists {
		gc.profiles[role] = AgentProfile{
			Model: gc.defaultModel,
		}
	}
}

// GetProfile returns the profile for a given role.
func (gc *GatewayClient) GetProfile(role string) (AgentProfile, bool) {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	p, ok := gc.profiles[role]
	return p, ok
}

// GetMaxContext returns the context window size for a model, or 0 if unknown.
func (gc *GatewayClient) GetMaxContext(model string) int {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	return gc.modelsContextWindow[model]
}

// GetDefaultMaxIter returns the default max iterations from the gateway config.
func (gc *GatewayClient) GetDefaultMaxIter() int {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	return gc.defaultMaxIter
}

// DefaultModel returns the default model name.
func (gc *GatewayClient) DefaultModel() string {
	return gc.defaultModel
}

// Hostname returns the configured hostname.
func (gc *GatewayClient) Hostname() string {
	return gc.hostname
}
