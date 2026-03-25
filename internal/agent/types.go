package agent

import "time"

// Message represents a conversation message in the LLM chat format.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// Finding represents a security finding discovered during investigation.
type Finding struct {
	ID         string   `json:"id"`
	Type       string   `json:"type"`
	Severity   string   `json:"severity"`
	Observable string   `json:"observable"`
	Analysis   string   `json:"analysis"`
	Evidence   []string `json:"evidence"`
	Status     string   `json:"status"`
	AgentName  string   `json:"agent_name,omitempty"`
}

// FindingSummary counts findings by severity.
type FindingSummary struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Info     int `json:"info"`
}

// AgentProfile holds per-role LLM configuration fetched from the gateway.
type AgentProfile struct {
	Model         string  `json:"model"`
	Temperature   float32 `json:"temperature"`
	TopP          float32 `json:"top_p"`
	TopK          int     `json:"top_k"`
	MaxIterations int     `json:"max_iterations"`
}

// Event represents a logged event written to the JSONL event log.
type Event struct {
	Timestamp time.Time      `json:"ts"`
	Type      string         `json:"type"`
	Data      map[string]any `json:"data"`
}

// Report is the top-level structure for the findings JSON report.
type Report struct {
	Metadata ReportMetadata `json:"metadata"`
	Findings []Finding      `json:"findings"`
	Summary  FindingSummary `json:"summary"`
}

// ReportMetadata holds metadata about the investigation run.
type ReportMetadata struct {
	Project     string    `json:"project"`
	Version     string    `json:"version"`
	AgentID     string    `json:"agent_id"`
	Hostname    string    `json:"hostname"`
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
	Mode        string    `json:"mode"`
	Model       string    `json:"model"`
	Iterations  int       `json:"iterations"`
	TotalTokens int       `json:"total_tokens,omitempty"`
}

// StatusUpdate is used for bulk finding status updates to the gateway.
type StatusUpdate struct {
	FindingID string `json:"finding_id"`
	Status    string `json:"status"`
}
