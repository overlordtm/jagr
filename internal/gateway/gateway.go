package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"go.uber.org/zap"

	"github.com/overlordtm/jagr/internal/gateway/dashboard"
	"github.com/overlordtm/jagr/internal/gateway/db"
	"github.com/overlordtm/jagr/internal/gateway/models"
	"github.com/overlordtm/jagr/internal/gateway/provider"
)

type Gateway struct {
	store       *db.Store
	log         *zap.Logger
	provider    provider.Provider
	config      *models.Config

	mu          sync.RWMutex
	// tracks per-session token usage for budget enforcement
	tokenCounts map[string]int
	rateLimiter *provider.RateLimiter
}

func NewGateway(config *models.Config, log *zap.Logger) (*Gateway, error) {
	store, err := db.NewStore(config.Database.Path, log)
	if err != nil {
		return nil, err
	}

	providerConfig := &config.Providers[0]
	p, err := provider.NewProvider(providerConfig, log, &config.Timeouts)
	if err != nil {
		return nil, err
	}

	return &Gateway{
		store:       store,
		log:         log,
		provider:    p,
		config:      config,
		tokenCounts: make(map[string]int),
		rateLimiter: provider.NewRateLimiter(config.RateLimit.RequestsPerMinute, config.RateLimit.MaxConcurrent),
	}, nil
}

func (g *Gateway) Start() error {
	router := mux.NewRouter()

	g.setupRoutes(router)

	// Start session reaper
	go g.sessionReaper()

	// Start dashboard if enabled
	if g.config.Dashboard.Enabled {
		g.startDashboard()
	}

	addr := g.config.Server.Listen

	tlsConfig, err := buildTLSConfig(&g.config.Server, g.log)
	if err != nil {
		return fmt.Errorf("failed to configure TLS: %w", err)
	}

	if tlsConfig != nil {
		server := &http.Server{
			Addr:      addr,
			Handler:   router,
			TLSConfig: tlsConfig,
		}
		g.log.Info("Starting gateway with TLS", zap.String("addr", addr))
		// Certificates are already in TLSConfig, so pass empty strings
		return server.ListenAndServeTLS("", "")
	}

	g.log.Info("Starting gateway without TLS", zap.String("addr", addr))
	return http.ListenAndServe(addr, router)
}

func (g *Gateway) startDashboard() {
	listen := g.config.Dashboard.Listen
	if listen == "" {
		listen = ":8080"
	}

	dash := dashboard.New(g.store, g.log.Named("dashboard"), &g.config.Dashboard, g.config)
	dashRouter := mux.NewRouter()
	dash.SetupRoutes(dashRouter)

	go func() {
		g.log.Info("Starting dashboard", zap.String("addr", listen))
		if err := http.ListenAndServe(listen, dashRouter); err != nil {
			g.log.Error("Dashboard server failed", zap.Error(err))
		}
	}()
}

func (g *Gateway) setupRoutes(router *mux.Router) {
	router.HandleFunc("/health", g.healthHandler).Methods("GET")
	router.HandleFunc("/v1/chat/completions", g.chatCompletionsHandler).Methods("POST")
	router.HandleFunc("/v1/models", g.modelsHandler).Methods("GET")
	router.HandleFunc("/v1/agent/config", g.agentConfigHandler).Methods("GET")

	router.HandleFunc("/v1/heartbeat", g.heartbeatHandler).Methods("POST")
	router.HandleFunc("/v1/sessions/close", g.closeSessionHandler).Methods("POST")
	router.HandleFunc("/v1/findings", g.submitFindingsHandler).Methods("POST")
	router.HandleFunc("/v1/findings/status", g.updateFindingStatusHandler).Methods("PATCH")
	router.HandleFunc("/v1/report", g.submitReportHandler).Methods("POST")
	router.HandleFunc("/v1/agent-settings", g.submitAgentSettingsHandler).Methods("POST")

	router.HandleFunc("/admin/agents", g.adminAgentsHandler).Methods("GET")
	router.HandleFunc("/admin/agents/{agent_id}/sessions", g.adminSessionsHandler).Methods("GET")
	router.HandleFunc("/admin/agents/{agent_id}/sessions/{session_id}/messages", g.adminMessagesHandler).Methods("GET")
	router.HandleFunc("/admin/agents/{agent_id}/sessions/{session_id}/events", g.adminEventsHandler).Methods("GET")
	router.HandleFunc("/admin/agents/{agent_id}/logs", g.adminLogsHandler).Methods("GET")
}

func (g *Gateway) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (g *Gateway) authenticateAgent(r *http.Request) (*models.Agent, error) {
	apiKey := r.Header.Get("Authorization")
	if apiKey != "" {
		apiKey = strings.TrimPrefix(apiKey, "Bearer ")
	}
	if apiKey == "" {
		apiKey = r.URL.Query().Get("api-key")
	}

	if apiKey != g.config.AgentAPIKey {
		return nil, fmt.Errorf("invalid API key")
	}

	hostname := r.Header.Get("X-Hostname")
	if hostname == "" {
		hostname = "unknown"
	}

	return g.store.FindOrCreateAgentByHostname(hostname)
}

func (g *Gateway) chatCompletionsHandler(w http.ResponseWriter, r *http.Request) {
	var req models.ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		g.log.Error("Failed to decode request", zap.Error(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	agent, err := g.authenticateAgent(r)
	if err != nil {
		g.log.Error("Authentication failed", zap.Error(err))
		http.Error(w, "invalid API key", http.StatusUnauthorized)
		return
	}

	g.log.Info("Received chat completions request",
		zap.String("model", req.Model),
		zap.Int("messages", len(req.Messages)),
		zap.Bool("stream", req.Stream),
		zap.String("hostname", agent.Hostname))

	// Enforce rate limiting
	if !g.rateLimiter.Allow(agent.ID) {
		g.log.Warn("Rate limit exceeded", zap.String("agent_id", agent.ID))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(models.ErrorResponse{
			Error: models.ErrorInfo{
				Message: "rate limit exceeded",
				Type:    "rate_limit_error",
				Code:    "rate_limit_exceeded",
			},
		})
		return
	}

	sessionID, err := g.getOrCreateSession(agent.ID)
	if err != nil {
		g.log.Error("Failed to get session", zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Enforce per-session token budget
	g.mu.RLock()
	totalTokens := g.tokenCounts[sessionID]
	g.mu.RUnlock()

	if maxTok := g.config.Session.MaxTokensPerSession; maxTok > 0 && totalTokens >= maxTok {
		g.log.Warn("Session token budget exceeded",
			zap.String("session_id", sessionID),
			zap.Int("total_tokens", totalTokens),
			zap.Int("max_tokens", maxTok))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(models.ErrorResponse{
			Error: models.ErrorInfo{
				Message: fmt.Sprintf("session token budget exceeded (%d/%d)", totalTokens, maxTok),
				Type:    "token_budget_error",
				Code:    "token_budget_exceeded",
			},
		})
		return
	}

	// Read sub-agent role from header (empty for main agent)
	subAgentRole := r.Header.Get("X-Sub-Agent-Role")

	// Store only NEW incoming messages in DB for dashboard visibility.
	// The agent sends the full conversation each time, so skip messages
	// we already have stored. Count is scoped per sub-agent role because
	// each sub-agent maintains its own independent conversation.
	existingCount, _ := g.store.GetMessageCountByRole(sessionID, subAgentRole)
	if len(req.Messages) > existingCount {
		for _, msg := range req.Messages[existingCount:] {
			if msg.Role != "assistant" {
				msg.SubAgentRole = subAgentRole
				g.store.AppendMessage(sessionID, &msg, "", 0, 0, 0, 0)
			}
		}
	}

	// Handle streaming requests
	if req.Stream {
		g.handleStreamingRequest(w, r, agent, sessionID, req)
		return
	}

	startTime := time.Now()
	resp, err := g.provider.ChatCompletion(r.Context(), req)
	if err != nil {
		g.log.Error("Provider request failed", zap.Error(err))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(models.ErrorResponse{
			Error: models.ErrorInfo{
				Message: err.Error(),
				Type:    "provider_error",
				Code:    "provider_request_failed",
			},
		})
		return
	}

	latency := time.Since(startTime).Milliseconds()

	g.store.LogAudit(agent.ID, "request", map[string]any{
		"model":     req.Model,
		"messages":  len(req.Messages),
		"stream":    false,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})

	// Calculate cost
	var costUSD float64
	if p, ok := g.provider.(*provider.OpenAICompatibleProvider); ok && resp.Usage != nil {
		_, _, costUSD = p.Pricing.CalculateCost(resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
	}

	var tokensIn, tokensOut int
	if resp.Usage != nil {
		tokensIn = resp.Usage.PromptTokens
		tokensOut = resp.Usage.CompletionTokens
	}

	g.store.LogAudit(agent.ID, "response", map[string]any{
		"model":       resp.Model,
		"tokens_in":   tokensIn,
		"tokens_out":  tokensOut,
		"cost_usd":    costUSD,
		"latency_ms":  latency,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	})

	g.log.Info("Request completed",
		zap.String("model", resp.Model),
		zap.Int("tokens_in", tokensIn),
		zap.Int("tokens_out", tokensOut),
		zap.Float64("cost_usd", costUSD),
		zap.Int64("latency_ms", latency),
		zap.String("hostname", agent.Hostname))

	if resp.Usage != nil {
		g.mu.Lock()
		g.tokenCounts[sessionID] += tokensIn + tokensOut
		g.mu.Unlock()
	}

	g.store.AppendMessage(sessionID, &models.Message{
		Role:         "assistant",
		Content:      resp.Choices[0].Message.Content,
		ToolCalls:    resp.Choices[0].Message.ToolCalls,
		SubAgentRole: subAgentRole,
	}, resp.Model, tokensIn, tokensOut, costUSD, int(latency))

	g.store.UpdateAgentConfigUpstream(sessionID, subAgentRole, req.Model, resp.Model, g.config.Providers[0].Name)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (g *Gateway) getOrCreateSession(agentID string) (string, error) {
	existingSession, err := g.store.GetSessionForAgent(agentID)
	if err == nil {
		return existingSession.ID, nil
	}

	sess, err := g.store.CreateSession(agentID)
	if err != nil {
		return "", err
	}

	return sess.ID, nil
}

func (g *Gateway) modelsHandler(w http.ResponseWriter, r *http.Request) {
	var modelList []models.Model
	for _, p := range g.config.Providers {
		for _, m := range p.Models {
			modelList = append(modelList, models.Model{
				ID:               m.Alias,
				Object:           "model",
				Created:          time.Now().Unix(),
				OwnedBy:          p.Name,
				MaxContextWindow: m.MaxContextWindow,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"object": "list",
		"data":   modelList,
	})
}

func (g *Gateway) agentConfigHandler(w http.ResponseWriter, r *http.Request) {
	agent, err := g.authenticateAgent(r)
	if err != nil {
		http.Error(w, "invalid API key", http.StatusUnauthorized)
		return
	}

	g.log.Debug("Serving agent profile config", zap.String("hostname", agent.Hostname))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"agents":                 g.config.Agents,
		"default_max_iterations": g.config.DefaultMaxIterations,
	})
}

func (g *Gateway) sessionReaper() {
	// 3 missed heartbeats at 10s interval = 30s threshold for inactive
	heartbeatThreshold := 30 * time.Second
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		// Mark sessions as inactive if heartbeats are missed
		if n, err := g.store.ReapStaleSessions(heartbeatThreshold); err != nil {
			g.log.Error("Failed to reap stale sessions", zap.Error(err))
		} else if n > 0 {
			g.log.Info("Reaped stale sessions", zap.Int64("count", n))
		}

		// Close sessions that exceeded TimeoutMinutes since last message
		if timeoutMin := g.config.Session.TimeoutMinutes; timeoutMin > 0 {
			timeout := time.Duration(timeoutMin) * time.Minute
			if n, err := g.store.CloseTimedOutSessions(timeout); err != nil {
				g.log.Error("Failed to close timed-out sessions", zap.Error(err))
			} else if n > 0 {
				g.log.Info("Closed timed-out sessions", zap.Int64("count", n))
			}
		}
	}
}

func (g *Gateway) heartbeatHandler(w http.ResponseWriter, r *http.Request) {
	agent, err := g.authenticateAgent(r)
	if err != nil {
		http.Error(w, "invalid API key", http.StatusUnauthorized)
		return
	}

	sessionID, err := g.store.HeartbeatSession(agent.ID)
	if err != nil {
		g.log.Debug("Heartbeat: no active session", zap.String("agent_id", agent.ID))
		w.WriteHeader(http.StatusNoContent)
		return
	}

	g.log.Debug("Heartbeat received", zap.String("session_id", sessionID), zap.String("hostname", agent.Hostname))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"session_id": sessionID, "status": "ok"})
}

func (g *Gateway) closeSessionHandler(w http.ResponseWriter, r *http.Request) {
	agent, err := g.authenticateAgent(r)
	if err != nil {
		http.Error(w, "invalid API key", http.StatusUnauthorized)
		return
	}

	session, err := g.store.GetSessionForAgent(agent.ID)
	if err != nil {
		g.log.Debug("Close session: no active session", zap.String("agent_id", agent.ID))
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Check if the agent is reporting an error
	var req struct {
		Error string `json:"error"`
	}
	// Body is optional — if missing or unparseable, error stays empty
	json.NewDecoder(r.Body).Decode(&req)

	if req.Error != "" {
		if err := g.store.CloseSessionWithError(session.ID, req.Error); err != nil {
			g.log.Error("Failed to close session with error", zap.Error(err))
			http.Error(w, "failed to close session", http.StatusInternalServerError)
			return
		}
		g.log.Warn("Session closed with error",
			zap.String("session_id", session.ID),
			zap.String("hostname", agent.Hostname),
			zap.String("error", req.Error))
	} else {
		if err := g.store.CloseSession(session.ID); err != nil {
			g.log.Error("Failed to close session", zap.Error(err))
			http.Error(w, "failed to close session", http.StatusInternalServerError)
			return
		}
		g.log.Info("Session closed by agent", zap.String("session_id", session.ID), zap.String("hostname", agent.Hostname))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"session_id": session.ID, "status": "closed"})
}

func (g *Gateway) adminAgentsHandler(w http.ResponseWriter, r *http.Request) {
	agents, err := g.store.GetAgents()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(agents)
}

func (g *Gateway) adminSessionsHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	agentID := vars["agent_id"]

	sessions, err := g.store.GetSessions(agentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

func (g *Gateway) adminMessagesHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	sessionID := vars["session_id"]

	messages, err := g.store.GetSessionMessages(sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(messages)
}

func (g *Gateway) handleStreamingRequest(w http.ResponseWriter, r *http.Request, agent *models.Agent, sessionID string, req models.ChatCompletionRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	startTime := time.Now()

	resp, err := g.provider.ChatCompletion(r.Context(), req)
	if err != nil {
		g.log.Error("Provider request failed", zap.Error(err))
		fmt.Fprintf(w, "data: {\"error\": %q}\n\n", err.Error())
		flusher.Flush()
		return
	}

	latency := time.Since(startTime).Milliseconds()

	g.store.LogAudit(agent.ID, "request", map[string]any{
		"model":     req.Model,
		"messages":  len(req.Messages),
		"stream":    true,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})

	// Send the response as SSE chunks
	respBytes, _ := json.Marshal(resp)
	fmt.Fprintf(w, "data: %s\n\n", respBytes)
	flusher.Flush()

	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()

	// Calculate cost
	var costUSD float64
	if p, ok := g.provider.(*provider.OpenAICompatibleProvider); ok && resp.Usage != nil {
		_, _, costUSD = p.Pricing.CalculateCost(resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
	}

	var tokensIn, tokensOut int
	if resp.Usage != nil {
		tokensIn = resp.Usage.PromptTokens
		tokensOut = resp.Usage.CompletionTokens
	}

	g.store.LogAudit(agent.ID, "response", map[string]any{
		"model":      resp.Model,
		"tokens_in":  tokensIn,
		"tokens_out": tokensOut,
		"cost_usd":   costUSD,
		"latency_ms": latency,
		"stream":     true,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
	})

	g.log.Info("Streaming request completed",
		zap.String("model", resp.Model),
		zap.Int("tokens_in", tokensIn),
		zap.Int("tokens_out", tokensOut),
		zap.Float64("cost_usd", costUSD),
		zap.Int64("latency_ms", latency))

	if len(resp.Choices) > 0 {
		if resp.Usage != nil {
			g.mu.Lock()
			g.tokenCounts[sessionID] += tokensIn + tokensOut
			g.mu.Unlock()
		}
		g.store.AppendMessage(sessionID, &models.Message{
			Role:         "assistant",
			Content:      resp.Choices[0].Message.Content,
			ToolCalls:    resp.Choices[0].Message.ToolCalls,
			SubAgentRole: r.Header.Get("X-Sub-Agent-Role"),
		}, resp.Model, tokensIn, tokensOut, costUSD, int(latency))

		g.store.UpdateAgentConfigUpstream(sessionID, r.Header.Get("X-Sub-Agent-Role"), req.Model, resp.Model, g.config.Providers[0].Name)
	}
}

func (g *Gateway) adminEventsHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	sessionID := vars["session_id"]

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Send existing messages first
	messages, err := g.store.GetSessionMessages(sessionID)
	if err != nil {
		fmt.Fprintf(w, "data: {\"error\": %q}\n\n", err.Error())
		flusher.Flush()
		return
	}

	for _, msg := range messages {
		msgBytes, _ := json.Marshal(msg)
		fmt.Fprintf(w, "data: %s\n\n", msgBytes)
	}
	flusher.Flush()

	// Poll for new messages
	lastCount := len(messages)
	ctx := r.Context()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			messages, err := g.store.GetSessionMessages(sessionID)
			if err != nil {
				continue
			}
			if len(messages) > lastCount {
				for _, msg := range messages[lastCount:] {
					msgBytes, _ := json.Marshal(msg)
					fmt.Fprintf(w, "data: %s\n\n", msgBytes)
				}
				flusher.Flush()
				lastCount = len(messages)
			}
		}
	}
}

func (g *Gateway) submitFindingsHandler(w http.ResponseWriter, r *http.Request) {
	agent, err := g.authenticateAgent(r)
	if err != nil {
		http.Error(w, "invalid API key", http.StatusUnauthorized)
		return
	}

	var req struct {
		Findings []struct {
			ID         string   `json:"id"`
			Type       string   `json:"type"`
			Severity   string   `json:"severity"`
			Observable string   `json:"observable"`
			Analysis   string   `json:"analysis"`
			Evidence   []string `json:"evidence"`
			Status     string   `json:"status"`
		} `json:"findings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	session, err := g.store.GetSessionForAgent(agent.ID)
	if err != nil {
		http.Error(w, "no active session", http.StatusBadRequest)
		return
	}

	for _, f := range req.Findings {
		evidenceJSON, _ := json.Marshal(f.Evidence)
		status := f.Status
		if status == "" {
			status = "preliminary"
		}
		g.store.AddFinding(session.ID, &models.SessionFinding{
			FindingID:  f.ID,
			Type:       f.Type,
			Severity:   f.Severity,
			Observable: f.Observable,
			Analysis:   f.Analysis,
			Evidence:   string(evidenceJSON),
			Status:     status,
		})
	}

	g.log.Info("Findings submitted", zap.String("hostname", agent.Hostname), zap.Int("count", len(req.Findings)))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "ok", "count": len(req.Findings)})
}

func (g *Gateway) updateFindingStatusHandler(w http.ResponseWriter, r *http.Request) {
	agent, err := g.authenticateAgent(r)
	if err != nil {
		http.Error(w, "invalid API key", http.StatusUnauthorized)
		return
	}

	var req struct {
		Findings []struct {
			FindingID string `json:"finding_id"`
			Status    string `json:"status"`
		} `json:"findings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	session, err := g.store.GetSessionForAgent(agent.ID)
	if err != nil {
		http.Error(w, "no active session", http.StatusBadRequest)
		return
	}

	for _, f := range req.Findings {
		if err := g.store.UpdateFindingStatus(session.ID, f.FindingID, f.Status); err != nil {
			g.log.Error("Failed to update finding status", zap.String("finding_id", f.FindingID), zap.Error(err))
		}
	}

	g.log.Info("Finding statuses updated", zap.String("hostname", agent.Hostname), zap.Int("count", len(req.Findings)))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "ok", "count": len(req.Findings)})
}

func (g *Gateway) submitReportHandler(w http.ResponseWriter, r *http.Request) {
	agent, err := g.authenticateAgent(r)
	if err != nil {
		http.Error(w, "invalid API key", http.StatusUnauthorized)
		return
	}

	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	session, err := g.store.GetSessionForAgent(agent.ID)
	if err != nil {
		http.Error(w, "no active session", http.StatusBadRequest)
		return
	}

	if err := g.store.AddReport(session.ID, req.Content); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	g.log.Info("Report submitted", zap.String("hostname", agent.Hostname))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (g *Gateway) submitAgentSettingsHandler(w http.ResponseWriter, r *http.Request) {
	agent, err := g.authenticateAgent(r)
	if err != nil {
		http.Error(w, "invalid API key", http.StatusUnauthorized)
		return
	}

	var req struct {
		Agents map[string]struct {
			Model       string  `json:"model"`
			Temperature float32 `json:"temperature"`
			TopP        float32 `json:"top_p"`
			TopK        int     `json:"top_k"`
		} `json:"agents"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	session, err := g.store.GetSessionForAgent(agent.ID)
	if err != nil {
		http.Error(w, "no active session", http.StatusBadRequest)
		return
	}

	for role, cfg := range req.Agents {
		g.store.AddAgentConfig(session.ID, &models.SessionAgentConfig{
			Role:        role,
			Model:       cfg.Model,
			Temperature: cfg.Temperature,
			TopP:        cfg.TopP,
			TopK:        cfg.TopK,
		})
	}

	g.log.Info("Agent settings submitted", zap.String("hostname", agent.Hostname), zap.Int("count", len(req.Agents)))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "ok", "count": len(req.Agents)})
}

func (g *Gateway) adminLogsHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	agentID := vars["agent_id"]

	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")
	eventType := r.URL.Query().Get("event_type")

	var start, end time.Time
	if startStr != "" {
		start, _ = time.Parse(time.RFC3339, startStr)
	}
	if endStr != "" {
		end, _ = time.Parse(time.RFC3339, endStr)
	}

	logs, err := g.store.GetAuditLog(agentID, start, end, eventType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(logs)
}
