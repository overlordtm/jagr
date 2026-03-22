package dashboard

import (
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
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
			if status == "active" {
				return "bg-emerald-500/20 text-emerald-400"
			}
			return "bg-gray-500/20 text-gray-500"
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
		"groupMessagesByAgent": func(messages []models.MessageLog) []messageGroup {
			order := []string{}
			byRole := map[string]*messageGroup{}
			for _, m := range messages {
				role := m.SubAgentRole
				if role == "" {
					role = "main"
				}
				g, ok := byRole[role]
				if !ok {
					g = &messageGroup{AgentRole: role}
					byRole[role] = g
					order = append(order, role)
				}
				g.Messages = append(g.Messages, m)
			}
			groups := make([]messageGroup, 0, len(order))
			for _, role := range order {
				groups = append(groups, *byRole[role])
			}
			return groups
		},
	}
	templates = template.Must(template.New("").Funcs(funcMap).ParseFS(staticFiles, "static/templates/*.html"))
}

type messageGroup struct {
	AgentRole string
	Messages  []models.MessageLog
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
	router.Handle("/partials/agents/{agent_id}/reports", wrap(d.agentReportsPartial)).Methods("GET")
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

	findingCount, _ := d.store.GetSessionFindingCount(sessionID)
	hasReport, _ := d.store.HasReport(sessionID)

	d.render(w, "layout", map[string]any{
		"Page":         "session",
		"Agent":        agent,
		"Session":      session,
		"FindingCount": findingCount,
		"HasReport":    hasReport,
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
	}

	var rows []sessionRow
	for _, s := range sessions {
		count, _ := d.store.GetMessageCount(s.ID)
		cost, _ := d.store.GetSessionCost(s.ID)
		findingCount, _ := d.store.GetSessionFindingCount(s.ID)
		hasReport, _ := d.store.HasReport(s.ID)
		rows = append(rows, sessionRow{
			Session:      s,
			MessageCount: count,
			TotalCost:    cost,
			FindingCount: findingCount,
			HasReport:    hasReport,
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

	sortMode := r.URL.Query().Get("sort")
	if sortMode != "agent" {
		sortMode = "chronological"
	}

	messages, err := d.store.GetSessionMessages(sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	d.render(w, "messages_partial", map[string]any{
		"Messages": messages,
		"SortMode": sortMode,
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

func (d *Dashboard) agentFindingsPartial(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	agentID := vars["agent_id"]

	findings, err := d.store.GetFindingsForAgent(agentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	d.render(w, "agent_findings_partial", findings)
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
