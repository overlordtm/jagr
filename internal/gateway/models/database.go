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
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Status    string    `json:"status"`
}

type MessageLog struct {
	ID          int       `json:"id"`
	SessionID   string    `json:"session_id"`
	Role        string    `json:"role"`
	Content     string    `json:"content,omitempty"`
	ToolCalls   string    `json:"tool_calls,omitempty"`
	ToolCallID  string    `json:"tool_call_id,omitempty"`
	Model       string    `json:"model,omitempty"`
	TokensIn    int       `json:"tokens_in,omitempty"`
	TokensOut   int       `json:"tokens_out,omitempty"`
	LatencyMs   int       `json:"latency_ms,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type AuditLog struct {
	ID        int       `json:"id"`
	AgentID   string    `json:"agent_id"`
	EventType string    `json:"event_type"`
	Payload   string    `json:"payload"`
	CreatedAt time.Time `json:"created_at"`
}