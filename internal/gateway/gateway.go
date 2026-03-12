package gateway

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"go.uber.org/zap"

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
	sessions    map[string]*SessionState
	rateLimiter *provider.RateLimiter
}

type SessionState struct {
	ID          string
	AgentID     string
	HistoryMode string
	Messages    []models.Message
	LastActivity time.Time
}

func NewGateway(config *models.Config, log *zap.Logger) (*Gateway, error) {
	store, err := db.NewStore(config.Database.Path, log)
	if err != nil {
		return nil, err
	}

	providerConfig := &config.Providers[0]
	p, err := provider.NewProvider(providerConfig, log)
	if err != nil {
		return nil, err
	}

	return &Gateway{
		store:       store,
		log:         log,
		provider:    p,
		config:      config,
		sessions:    make(map[string]*SessionState),
		rateLimiter: provider.NewRateLimiter(config.RateLimit.RequestsPerMinute, config.RateLimit.MaxConcurrent),
	}, nil
}

func (g *Gateway) Start() error {
	router := mux.NewRouter()

	g.setupRoutes(router)

	addr := g.config.Server.Listen
	if g.config.Server.TLS.Cert != "" && g.config.Server.TLS.Key != "" {
		server := &http.Server{
			Addr:      addr,
			Handler:   router,
			TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		}
		g.log.Info("Starting gateway with TLS", zap.String("addr", addr))
		return server.ListenAndServeTLS(g.config.Server.TLS.Cert, g.config.Server.TLS.Key)
	}

	g.log.Info("Starting gateway without TLS", zap.String("addr", addr))
	return http.ListenAndServe(addr, router)
}

func (g *Gateway) setupRoutes(router *mux.Router) {
	router.HandleFunc("/health", g.healthHandler).Methods("GET")
	router.HandleFunc("/v1/chat/completions", g.chatCompletionsHandler).Methods("POST")
	router.HandleFunc("/v1/models", g.modelsHandler).Methods("GET")

	router.HandleFunc("/admin/exercises", g.adminExercisesHandler).Methods("GET")
	router.HandleFunc("/admin/exercises/{exercise_id}/agents", g.adminAgentsHandler).Methods("GET")
	router.HandleFunc("/admin/agents/{agent_id}/sessions", g.adminSessionsHandler).Methods("GET")
	router.HandleFunc("/admin/agents/{agent_id}/sessions/{session_id}/messages", g.adminMessagesHandler).Methods("GET")
	router.HandleFunc("/admin/agents/{agent_id}/logs", g.adminLogsHandler).Methods("GET")
}

func (g *Gateway) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (g *Gateway) chatCompletionsHandler(w http.ResponseWriter, r *http.Request) {
	var req models.ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		g.log.Error("Failed to decode request", zap.Error(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	apiKey := r.Header.Get("Authorization")
	if apiKey != "" {
		apiKey = strings.TrimPrefix(apiKey, "Bearer ")
	}

	if apiKey == "" {
		apiKey = r.URL.Query().Get("api-key")
	}

	g.log.Info("Received chat completions request",
		zap.String("model", req.Model),
		zap.Int("messages", len(req.Messages)),
		zap.Bool("stream", req.Stream))

	agent, err := g.store.FindAgentByAPIKey(apiKey)
	if err != nil {
		g.log.Error("Invalid API key", zap.Error(err))
		http.Error(w, "invalid API key", http.StatusUnauthorized)
		return
	}

	sessionID, err := g.getOrCreateSession(agent.ID)
	if err != nil {
		g.log.Error("Failed to get session", zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	session := g.sessions[sessionID]

	reqWithHistory, err := g.buildRequestWithHistory(req, session)
	if err != nil {
		g.log.Error("Failed to build request", zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp, err := g.provider.ChatCompletion(r.Context(), reqWithHistory)
	if err != nil {
		g.log.Error("Provider request failed", zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	latency := time.Since(time.Now()).Milliseconds()

	g.store.LogAudit(agent.ID, "request", map[string]any{
		"model":     req.Model,
		"messages":  reqWithHistory.Messages,
		"tools":     reqWithHistory.Tools,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})

	g.store.LogAudit(agent.ID, "response", map[string]any{
		"model":       resp.Model,
		"choices":     resp.Choices,
		"tokens_in":   resp.Usage.PromptTokens,
		"tokens_out":  resp.Usage.CompletionTokens,
		"latency_ms":  latency,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	})

	session.Messages = append(session.Messages, models.Message{
		Role:      "assistant",
		Content:   resp.Choices[0].Message.Content,
		ToolCalls: resp.Choices[0].Message.ToolCalls,
	})

	g.updateSession(session)

	g.store.AppendMessage(session.ID, &models.Message{
		Role:        "assistant",
		Content:     resp.Choices[0].Message.Content,
		ToolCalls:   resp.Choices[0].Message.ToolCalls,
	}, resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, int(latency))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (g *Gateway) buildRequestWithHistory(req models.ChatCompletionRequest, session *SessionState) (models.ChatCompletionRequest, error) {
	history := session.Messages

	if len(req.Messages) == 1 && req.Messages[0].Role == "user" {
		if session.HistoryMode == "gateway" || session.HistoryMode == "" {
			req.Messages = append(history, req.Messages...)
			return req, nil
		}
	}

	return req, nil
}

func (g *Gateway) getOrCreateSession(agentID string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	existingSession, err := g.store.GetSessionForAgent(agentID)
	if err == nil {
		if _, ok := g.sessions[existingSession.ID]; !ok {
			g.sessions[existingSession.ID] = &SessionState{
				ID:           existingSession.ID,
				AgentID:      agentID,
				HistoryMode:  g.config.Session.HistoryMode,
				Messages:     []models.Message{},
				LastActivity: time.Now(),
			}
		}
		return existingSession.ID, nil
	}

	sess, err := g.store.CreateSession(agentID)
	if err != nil {
		return "", err
	}

	g.sessions[sess.ID] = &SessionState{
		ID:           sess.ID,
		AgentID:      agentID,
		HistoryMode:  g.config.Session.HistoryMode,
		Messages:     []models.Message{},
		LastActivity: time.Now(),
	}

	return sess.ID, nil
}

func (g *Gateway) updateSession(session *SessionState) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, ok := g.sessions[session.ID]; ok {
		session.LastActivity = time.Now()
		g.sessions[session.ID] = session
	}
}

func (g *Gateway) modelsHandler(w http.ResponseWriter, r *http.Request) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var modelList []models.Model
	for _, p := range g.config.Providers {
		for _, m := range p.Models {
			modelList = append(modelList, models.Model{
				ID:      m.Alias,
				Object:  "model",
				Created: time.Now().Unix(),
				OwnedBy: p.Name,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"object": "list",
		"data":   modelList,
	})
}

func (g *Gateway) adminExercisesHandler(w http.ResponseWriter, r *http.Request) {
	exercises, err := g.store.GetExercises("")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(exercises)
}

func (g *Gateway) adminAgentsHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	exerciseID := vars["exercise_id"]

	agents, err := g.store.GetAgents(exerciseID)
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
