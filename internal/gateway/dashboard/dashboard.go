package dashboard

import (
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"go.uber.org/zap"

	"github.com/overlordtm/jagr/internal/gateway/db"
	"github.com/overlordtm/jagr/internal/gateway/models"
)

//go:embed static/*
var staticFiles embed.FS

var templates *template.Template

func init() {
	funcMap := template.FuncMap{
		"timeAgo": func(t time.Time) string {
			d := time.Since(t)
			switch {
			case d < time.Minute:
				return "just now"
			case d < time.Hour:
				return fmt.Sprintf("%dm ago", int(d.Minutes()))
			case d < 24*time.Hour:
				return fmt.Sprintf("%dh ago", int(d.Hours()))
			default:
				return fmt.Sprintf("%dd ago", int(d.Hours()/24))
			}
		},
		"formatTime": func(t time.Time) string {
			return t.Format("2006-01-02 15:04:05")
		},
		"formatDuration": func(d time.Duration) string {
			d = d.Round(time.Second)
			h := d / time.Hour
			d -= h * time.Hour
			m := d / time.Minute
			d -= m * time.Minute
			s := d / time.Second
			if h > 0 {
				return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
			}
			return fmt.Sprintf("%02d:%02d", m, s)
		},
		"formatCost": func(c float64) string {
			if c < 0.01 {
				return fmt.Sprintf("$%.4f", c)
			}
			return fmt.Sprintf("$%.2f", c)
		},
		"truncate": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "..."
		},
		"shortID": func(s string) string {
			if len(s) > 8 {
				return s[:8]
			}
			return s
		},
		"roleColor": func(role string) string {
			switch role {
			case "user":
				return "bg-blue-500/20 text-blue-400"
			case "assistant":
				return "bg-emerald-500/20 text-emerald-400"
			case "tool":
				return "bg-purple-500/20 text-purple-400"
			default:
				return "bg-gray-500/20 text-gray-400"
			}
		},
		"statusColor": func(status string) string {
			switch status {
			case "active":
				return "bg-emerald-500/20 text-emerald-400"
			case "completed":
				return "bg-sky-500/20 text-sky-400"
			case "error":
				return "bg-red-500/20 text-red-400"
			case "valid":
				return "bg-green-500/20 text-green-400"
			case "invalid":
				return "bg-red-500/20 text-red-400"
			case "duplicate":
				return "bg-yellow-500/20 text-yellow-400"
			case "preliminary":
				return "bg-gray-500/20 text-gray-400"
			default:
				return "bg-gray-500/20 text-gray-500"
			}
		},
		"isLong": func(s string) bool {
			return len(s) > 500
		},
		"previewContent": func(s string) string {
			if len(s) <= 500 {
				return s
			}
			// Cut at last newline before 500 chars for a clean break
			cut := s[:500]
			if i := strings.LastIndex(cut, "\n"); i > 200 {
				cut = cut[:i]
			}
			return cut + "\n..."
		},
		"parseToolCalls": func(s string) []parsedToolCall {
			var calls []parsedToolCall
			var raw []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			}
			if err := json.Unmarshal([]byte(s), &raw); err != nil {
				return nil
			}
			for _, r := range raw {
				tc := parsedToolCall{
					ID:   r.ID,
					Name: r.Function.Name,
				}
				// Try to pretty-print arguments
				var args any
				if json.Unmarshal([]byte(r.Function.Arguments), &args) == nil {
					if pretty, err := json.MarshalIndent(args, "", "  "); err == nil {
						tc.Arguments = string(pretty)
					} else {
						tc.Arguments = r.Function.Arguments
					}
				} else {
					tc.Arguments = r.Function.Arguments
				}
				// Check if this is a finding submission
				if r.Function.Name == "submit_finding" {
					tc.IsFinding = true
					var findingArgs struct {
						Finding struct {
							Severity   string `json:"severity"`
							Type       string `json:"type"`
							Observable string `json:"observable"`
						} `json:"finding"`
					}
					if json.Unmarshal([]byte(r.Function.Arguments), &findingArgs) == nil {
						tc.FindingSeverity = findingArgs.Finding.Severity
						tc.FindingType = findingArgs.Finding.Type
						tc.FindingObservable = findingArgs.Finding.Observable
					}
				}
				calls = append(calls, tc)
			}
			return calls
		},
		"severityColor": func(sev string) string {
			switch strings.ToLower(sev) {
			case "critical":
				return "bg-red-500/20 text-red-400"
			case "high":
				return "bg-orange-500/20 text-orange-400"
			case "medium":
				return "bg-yellow-500/20 text-yellow-400"
			case "low":
				return "bg-sky-500/20 text-sky-400"
			default:
				return "bg-gray-500/20 text-gray-400"
			}
		},
		"hasToolCalls": func(s string) bool {
			return s != "" && s != "null" && s != "[]"
		},
		"formatFloat": func(f float32) string {
			return fmt.Sprintf("%g", f)
		},
		"agentColor": func(role string) string {
			switch role {
			case "planner":
				return "bg-sky-500/20 text-sky-400 border-sky-500/30"
			case "investigator":
				return "bg-amber-500/20 text-amber-400 border-amber-500/30"
			case "reporter":
				return "bg-purple-500/20 text-purple-400 border-purple-500/30"
			default:
				return "bg-emerald-500/20 text-emerald-400 border-emerald-500/30"
			}
		},
		"agentBorder": func(role string) string {
			switch role {
			case "planner":
				return "border-l-sky-500/50"
			case "investigator":
				return "border-l-amber-500/50"
			case "reporter":
				return "border-l-purple-500/50"
			default:
				return "border-l-emerald-500/50"
			}
		},
		"parseEvidence": func(s string) []string {
			var evidence []string
			json.Unmarshal([]byte(s), &evidence)
			return evidence
		},
		"memoTypeColor": func(t string) string {
			switch t {
			case "finding_lead":
				return "bg-amber-500/20 text-amber-400"
			case "correlation":
				return "bg-sky-500/20 text-sky-400"
			case "sitrep":
				return "bg-emerald-500/20 text-emerald-400"
			case "enrichment":
				return "bg-purple-500/20 text-purple-400"
			default: // observation
				return "bg-gray-500/20 text-gray-400"
			}
		},
		"scopeColor": func(s string) string {
			switch s {
			case "host":
				return "bg-orange-500/20 text-orange-400"
			case "exercise":
				return "bg-indigo-500/20 text-indigo-400"
			default: // agent
				return "bg-blue-500/20 text-blue-400"
			}
		},
		"add": func(a, b int) int {
			return a + b
		},
		"subtract": func(a, b int) int {
			return a - b
		},
		"groupMessagesByAgent": func(messages []models.MessageLog) []messageGroup {
			order := []string{}
			byName := map[string]*messageGroup{}
			for _, m := range messages {
				name := m.AgentName
				if name == "" {
					name = "main"
				}
				g, ok := byName[name]
				if !ok {
					g = &messageGroup{AgentName: name}
					byName[name] = g
					order = append(order, name)
				}
				g.Messages = append(g.Messages, m)
			}
			// Detect whether each agent concluded normally by checking the
			// last assistant message: either it called the "conclude" tool,
			// or it responded with content but no tool calls (LLM chose to stop).
			for _, g := range byName {
				for i := len(g.Messages) - 1; i >= 0; i-- {
					m := g.Messages[i]
					if m.Role != "assistant" {
						continue
					}
					if strings.Contains(m.ToolCalls, `"conclude"`) {
						g.Concluded = true
					} else if m.Content != "" && (m.ToolCalls == "" || m.ToolCalls == "null" || m.ToolCalls == "[]") {
						g.Concluded = true
					}
					break
				}
			}
			groups := make([]messageGroup, 0, len(order))
			for _, role := range order {
				groups = append(groups, *byName[role])
			}
			return groups
		},
	}
	templates = template.Must(template.New("").Funcs(funcMap).ParseFS(staticFiles, "static/templates/*.html"))
}

type messageGroup struct {
	AgentName string
	Messages  []models.MessageLog
	Concluded bool // true if the subagent concluded normally (called conclude tool or responded without tool calls)
}

type parsedToolCall struct {
	ID                string
	Name              string
	Arguments         string
	IsFinding         bool
	FindingSeverity   string
	FindingType       string
	FindingObservable string
}

type Dashboard struct {
	store      *db.Store
	log        *zap.Logger
	config     *models.DashboardConfig
	fullConfig *models.Config
}

func New(store *db.Store, log *zap.Logger, config *models.DashboardConfig, fullConfig *models.Config) *Dashboard {
	return &Dashboard{
		store:      store,
		log:        log,
		config:     config,
		fullConfig: fullConfig,
	}
}

func (d *Dashboard) SetupRoutes(router *mux.Router) {
	// Serve built Vite assets
	assetsFS, _ := fs.Sub(staticFiles, "static/assets")
	router.PathPrefix("/assets/").Handler(
		http.StripPrefix("/assets/", http.FileServer(http.FS(assetsFS))),
	)

	// Apply auth middleware if users are configured
	var handler func(http.Handler) http.Handler
	if len(d.config.Users) > 0 {
		handler = d.basicAuthMiddleware
	}

	wrap := func(h http.HandlerFunc) http.Handler {
		var out http.Handler = h
		if handler != nil {
			out = handler(out)
		}
		return out
	}

	// Pages
	router.Handle("/", wrap(d.indexHandler)).Methods("GET")
	router.Handle("/configuration", wrap(d.configurationHandler)).Methods("GET")
	router.Handle("/memos", wrap(d.memosHandler)).Methods("GET")
	router.Handle("/agents/{agent_id}", wrap(d.agentHandler)).Methods("GET")
	router.Handle("/agents/{agent_id}/sessions/{session_id}", wrap(d.sessionHandler)).Methods("GET")

	// HTMX partials
	router.Handle("/partials/agents", wrap(d.agentsPartial)).Methods("GET")
	router.Handle("/partials/stats", wrap(d.statsPartial)).Methods("GET")
	router.Handle("/partials/agents/{agent_id}/sessions", wrap(d.sessionsPartial)).Methods("GET")
	router.Handle("/partials/agents/{agent_id}/sessions/{session_id}/messages", wrap(d.messagesPartial)).Methods("GET")
	router.Handle("/partials/agents/{agent_id}/sessions/{session_id}/findings", wrap(d.sessionFindingsPartial)).Methods("GET")
	router.Handle("/partials/agents/{agent_id}/sessions/{session_id}/report", wrap(d.sessionReportPartial)).Methods("GET")
	router.Handle("/partials/agents/{agent_id}/sessions/{session_id}/agent-configs", wrap(d.sessionAgentConfigsPartial)).Methods("GET")
	router.Handle("/partials/agents/{agent_id}/findings", wrap(d.agentFindingsPartial)).Methods("GET")
	router.Handle("/partials/findings/{id}/status", wrap(d.updateFindingStatusPartial)).Methods("PATCH")
	router.Handle("/partials/agents/{agent_id}/reports", wrap(d.agentReportsPartial)).Methods("GET")
	router.Handle("/partials/memos", wrap(d.allMemosPartial)).Methods("GET")
	router.Handle("/partials/agents/{agent_id}/memos", wrap(d.agentMemosPartial)).Methods("GET")
	router.Handle("/partials/agents/{agent_id}/sessions/{session_id}/memos", wrap(d.sessionMemosPartial)).Methods("GET")

	// Mutation endpoints
	router.Handle("/partials/agents/{agent_id}/sessions/{session_id}", wrap(d.deleteSessionPartial)).Methods("DELETE")
	router.Handle("/partials/memos/{memo_id}", wrap(d.getMemoPartial)).Methods("GET")
	router.Handle("/partials/memos/{memo_id}/edit", wrap(d.getMemoEditPartial)).Methods("GET")
	router.Handle("/partials/memos/{memo_id}", wrap(d.updateMemoPartial)).Methods("PATCH")
	router.Handle("/partials/memos/{memo_id}", wrap(d.deleteMemoPartial)).Methods("DELETE")
}

func (d *Dashboard) basicAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="JAGR Dashboard"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		valid := false
		for _, u := range d.config.Users {
			userMatch := subtle.ConstantTimeCompare(
				sha256Sum(user), sha256Sum(u.Username),
			) == 1
			passMatch := subtle.ConstantTimeCompare(
				sha256Sum(pass), sha256Sum(u.Password),
			) == 1
			if userMatch && passMatch {
				valid = true
				break
			}
		}

		if !valid {
			w.Header().Set("WWW-Authenticate", `Basic realm="JAGR Dashboard"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func sha256Sum(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// Page handlers

func (d *Dashboard) indexHandler(w http.ResponseWriter, r *http.Request) {
	d.render(w, "layout", map[string]any{"Page": "index"})
}

func (d *Dashboard) configurationHandler(w http.ResponseWriter, r *http.Request) {
	// Build a safe view of providers (no API keys)
	type providerView struct {
		Name   string
		Type   string
		Models []models.ModelMapping
	}
	var providers []providerView
	for _, p := range d.fullConfig.Providers {
		providers = append(providers, providerView{
			Name:   p.Name,
			Type:   p.Type,
			Models: p.Models,
		})
	}

	d.render(w, "layout", map[string]any{
		"Page":            "configuration",
		"Agents":          d.fullConfig.Agents,
		"Providers":       providers,
		"DefaultProvider": d.fullConfig.DefaultProvider,
		"DefaultModel":    d.fullConfig.DefaultModel,
		"RateLimit":       d.fullConfig.RateLimit,
		"Session":         d.fullConfig.Session,
	})
}

func (d *Dashboard) agentHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	agentID := vars["agent_id"]

	agent, err := d.store.GetAgent(agentID)
	if err != nil {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}

	d.render(w, "layout", map[string]any{
		"Page":  "agent",
		"Agent": agent,
	})
}

func (d *Dashboard) sessionHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	agentID := vars["agent_id"]
	sessionID := vars["session_id"]

	agent, err := d.store.GetAgent(agentID)
	if err != nil {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}

	session, err := d.store.GetSession(sessionID)
	if err != nil {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	var duration time.Duration
	if session.Status == "active" {
		duration = time.Since(session.CreatedAt)
	} else {
		duration = session.UpdatedAt.Sub(session.CreatedAt)
	}

	findingCount, _ := d.store.GetSessionFindingCount(sessionID)
	hasReport, _ := d.store.HasReport(sessionID)
	upstreamModel, _ := d.store.GetSessionUpstreamModel(sessionID)
	agentNames, _ := d.store.GetSessionAgentNames(sessionID)

	d.render(w, "layout", map[string]any{
		"Page":          "session",
		"Agent":         agent,
		"Session":       session,
		"Duration":      duration,
		"FindingCount":  findingCount,
		"HasReport":     hasReport,
		"UpstreamModel": upstreamModel,
		"AgentNames":    agentNames,
	})
}

// HTMX partial handlers

func (d *Dashboard) agentsPartial(w http.ResponseWriter, r *http.Request) {
	agents, err := d.store.GetAgents()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Get session counts per agent
	type agentRow struct {
		models.Agent
		SessionCount int
		LastActive   string
	}

	var rows []agentRow
	for _, a := range agents {
		sessions, _ := d.store.GetSessions(a.ID)
		lastActive := "never"
		for _, s := range sessions {
			if lastActive == "never" || s.UpdatedAt.After(a.CreatedAt) {
				lastActive = timeAgo(s.UpdatedAt)
			}
		}
		rows = append(rows, agentRow{
			Agent:        a,
			SessionCount: len(sessions),
			LastActive:   lastActive,
		})
	}

	d.render(w, "agents_partial", rows)
}

func (d *Dashboard) statsPartial(w http.ResponseWriter, r *http.Request) {
	stats, err := d.store.GetDashboardStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	d.render(w, "stats_partial", stats)
}

func (d *Dashboard) sessionsPartial(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	agentID := vars["agent_id"]

	sessions, err := d.store.GetSessions(agentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type sessionRow struct {
		models.Session
		MessageCount int
		TotalCost    float64
		FindingCount int
		HasReport    bool
		Duration     time.Duration
	}

	var rows []sessionRow
	for _, s := range sessions {
		count, _ := d.store.GetMessageCount(s.ID)
		cost, _ := d.store.GetSessionCost(s.ID)
		findingCount, _ := d.store.GetSessionFindingCount(s.ID)
		hasReport, _ := d.store.HasReport(s.ID)

		var duration time.Duration
		if s.Status == "active" {
			duration = time.Since(s.CreatedAt)
		} else {
			duration = s.UpdatedAt.Sub(s.CreatedAt)
		}

		rows = append(rows, sessionRow{
			Session:      s,
			MessageCount: count,
			TotalCost:    cost,
			FindingCount: findingCount,
			HasReport:    hasReport,
			Duration:     duration,
		})
	}

	d.render(w, "sessions_partial", map[string]any{
		"AgentID":  agentID,
		"Sessions": rows,
	})
}

func (d *Dashboard) messagesPartial(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	sessionID := vars["session_id"]
	agentID := vars["agent_id"]

	sortMode := r.URL.Query().Get("sort")
	if sortMode != "agent" {
		sortMode = "chronological"
	}

	agentFilter := r.URL.Query().Get("agent")

	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p
	}
	const perPage = 50

	messages, total, err := d.store.GetSessionMessagesPaginatedFiltered(sessionID, agentFilter, perPage, (page-1)*perPage)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	totalPages := (total + perPage - 1) / perPage
	if totalPages < 1 {
		totalPages = 1
	}

	session, _ := d.store.GetSession(sessionID)
	sessionStatus := ""
	if session != nil {
		sessionStatus = session.Status
	}

	agentNames, _ := d.store.GetSessionAgentNames(sessionID)

	d.render(w, "messages_partial", map[string]any{
		"Messages":      messages,
		"SortMode":      sortMode,
		"AgentFilter":   agentFilter,
		"AgentNames":    agentNames,
		"Page":          page,
		"TotalPages":    totalPages,
		"Total":         total,
		"AgentID":       agentID,
		"SessionID":     sessionID,
		"SessionStatus": sessionStatus,
	})
}

func (d *Dashboard) sessionFindingsPartial(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	sessionID := vars["session_id"]

	findings, err := d.store.GetSessionFindings(sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	d.render(w, "session_findings_partial", findings)
}

func (d *Dashboard) sessionReportPartial(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	sessionID := vars["session_id"]

	report, err := d.store.GetSessionReport(sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	d.render(w, "session_report_partial", report)
}

func (d *Dashboard) sessionAgentConfigsPartial(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	sessionID := vars["session_id"]

	configs, err := d.store.GetSessionAgentConfigs(sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	d.render(w, "session_agent_configs_partial", configs)
}

type agentFindingsData struct {
	AgentID  string
	Search   string
	Status   string
	Findings []models.SessionFinding
}

func (d *Dashboard) agentFindingsPartial(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	agentID := vars["agent_id"]
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("search")))
	statusFilter := r.URL.Query().Get("status")

	findings, err := d.store.GetFindingsForAgent(agentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if search != "" || statusFilter != "" {
		filtered := findings[:0]
		for _, f := range findings {
			if statusFilter != "" && f.Status != statusFilter {
				continue
			}
			if search != "" {
				haystack := strings.ToLower(f.Observable + " " + f.Type + " " + f.Analysis + " " + f.FindingID)
				if !strings.Contains(haystack, search) {
					continue
				}
			}
			filtered = append(filtered, f)
		}
		findings = filtered
	}

	d.render(w, "agent_findings_partial", agentFindingsData{
		AgentID:  agentID,
		Search:   search,
		Status:   statusFilter,
		Findings: findings,
	})
}

func (d *Dashboard) updateFindingStatusPartial(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	idStr := vars["id"]
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	validStatuses := map[string]bool{"preliminary": true, "valid": true, "invalid": true, "duplicate": true}
	if !validStatuses[body.Status] {
		http.Error(w, "invalid status", http.StatusBadRequest)
		return
	}

	if err := d.store.UpdateFindingStatusByPK(id, body.Status); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (d *Dashboard) agentReportsPartial(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	agentID := vars["agent_id"]

	reports, err := d.store.GetReportsForAgent(agentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	d.render(w, "agent_reports_partial", reports)
}

func (d *Dashboard) memosHandler(w http.ResponseWriter, r *http.Request) {
	d.render(w, "layout", map[string]any{"Page": "memos"})
}

func (d *Dashboard) allMemosPartial(w http.ResponseWriter, r *http.Request) {
	memos, err := d.store.GetAllMemos(500)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	d.render(w, "memos_partial", memos)
}

func (d *Dashboard) agentMemosPartial(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	agentID := vars["agent_id"]

	agent, err := d.store.GetAgent(agentID)
	if err != nil {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}

	memos, err := d.store.GetMemos(agentID, "", agent.Hostname, "", "", "", 200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	d.render(w, "memos_partial", memos)
}

func (d *Dashboard) sessionMemosPartial(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	agentID := vars["agent_id"]
	sessionID := vars["session_id"]

	memos, err := d.store.GetMemos(agentID, "", "", sessionID, "", "", 200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	d.render(w, "memos_partial", memos)
}

func (d *Dashboard) deleteSessionPartial(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	sessionID := vars["session_id"]

	if err := d.store.DeleteSession(sessionID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Return empty body — htmx outerHTML swap removes the row
	w.WriteHeader(http.StatusOK)
}

func (d *Dashboard) getMemoPartial(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	memoID := vars["memo_id"]

	memo, err := d.store.GetMemo(memoID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	d.render(w, "memo_row", memo)
}

func (d *Dashboard) getMemoEditPartial(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	memoID := vars["memo_id"]

	memo, err := d.store.GetMemo(memoID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	d.render(w, "memo_edit_form", memo)
}

func (d *Dashboard) updateMemoPartial(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	memoID := vars["memo_id"]

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	content := r.FormValue("content")
	if content == "" {
		http.Error(w, "content required", http.StatusBadRequest)
		return
	}

	if err := d.store.UpdateMemo(memoID, content); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	memo, err := d.store.GetMemo(memoID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	d.render(w, "memo_row", memo)
}

func (d *Dashboard) deleteMemoPartial(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	memoID := vars["memo_id"]

	if err := d.store.DeleteMemo(memoID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// Return empty body — htmx outerHTML swap removes the element
	w.WriteHeader(http.StatusOK)
}

func (d *Dashboard) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, name, data); err != nil {
		d.log.Error("Template render error", zap.String("template", name), zap.Error(err))
		if !headersSent(w) {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	}
}

func headersSent(w http.ResponseWriter) bool {
	// Once Write is called, headers are sent. This is a best-effort check.
	return false
}

func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func isHTMX(r *http.Request) bool {
	return strings.Contains(r.Header.Get("HX-Request"), "true")
}
