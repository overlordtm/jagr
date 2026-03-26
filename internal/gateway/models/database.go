package models

import (
	"time"
)

type Agent struct {
	ID        string    `json:"id"`
	Hostname  string    `json:"hostname"`
	CreatedAt time.Time `json:"created_at"`
}

type Session struct {
	ID            string     `json:"id"`
	AgentID       string     `json:"agent_id"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	Status        string     `json:"status"`
	LastHeartbeat *time.Time `json:"last_heartbeat,omitempty"`
	Error         string     `json:"error,omitempty"`
}

type MessageLog struct {
	ID           int       `json:"id"`
	SessionID    string    `json:"session_id"`
	Role         string    `json:"role"`
	Content      string    `json:"content,omitempty"`
	ToolCalls    string    `json:"tool_calls,omitempty"`
	ToolCallID   string    `json:"tool_call_id,omitempty"`
	Model        string    `json:"model,omitempty"`
	TokensIn     int       `json:"tokens_in,omitempty"`
	TokensOut    int       `json:"tokens_out,omitempty"`
	CostUSD      float64   `json:"cost_usd,omitempty"`
	LatencyMs    int       `json:"latency_ms,omitempty"`
	SubAgentRole string    `json:"sub_agent_role,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type AuditLog struct {
	ID        int       `json:"id"`
	AgentID   string    `json:"agent_id"`
	EventType string    `json:"event_type"`
	Payload   string    `json:"payload"`
	CreatedAt time.Time `json:"created_at"`
}

type SessionFinding struct {
	ID         int       `json:"id"`
	SessionID  string    `json:"session_id"`
	FindingID  string    `json:"finding_id"`
	Type       string    `json:"type"`
	Severity   string    `json:"severity"`
	Observable string    `json:"observable"`
	Analysis   string    `json:"analysis"`
	Evidence   string    `json:"evidence"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

type SessionReport struct {
	ID        int       `json:"id"`
	SessionID string    `json:"session_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type SessionAgentConfig struct {
	ID          int       `json:"id"`
	SessionID   string    `json:"session_id"`
	Role        string    `json:"role"`
	Model       string    `json:"model"`
	ActualModel string    `json:"actual_model"`
	Provider    string    `json:"provider"`
	Temperature float32   `json:"temperature"`
	TopP        float32   `json:"top_p"`
	TopK        int       `json:"top_k"`
	CreatedAt   time.Time `json:"created_at"`
}

// Memo represents a persistent note written by an agent.
type Memo struct {
	ID         string    `json:"id"`
	ExerciseID string    `json:"exercise_id"`
	SessionID  string    `json:"session_id,omitempty"`
	Host       string    `json:"host,omitempty"`
	Scope      string    `json:"scope"`
	Content    string    `json:"content"`
	MemoType   string    `json:"memo_type"`
	CreatedAt  time.Time `json:"created_at"`
}