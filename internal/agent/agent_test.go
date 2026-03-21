package agent

import (
	"os"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestAgent_Hostname(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	agent := &Agent{
		logger: logger,
	}
	
	hostname := agent.hostname()
	
	if hostname == "" {
		t.Error("Expected non-empty hostname")
	}
}

func TestAgent_BuildSystemPrompt(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	agent := &Agent{
		logger: logger,
	}
	
	prompt := agent.buildSystemPrompt()
	
	if !strings.Contains(prompt, "JAGR") {
		t.Error("Expected prompt to contain JAGR")
	}
	if !strings.Contains(prompt, "security auditor") {
		t.Error("Expected prompt to contain 'security auditor'")
	}
	if !strings.Contains(prompt, "Available Tools") {
		t.Error("Expected prompt to contain 'Available Tools'")
	}
}

func TestAgent_BuildSummary(t *testing.T) {
	agent := &Agent{
		findings: []Finding{
			{Severity: "critical"},
			{Severity: "critical"},
			{Severity: "high"},
			{Severity: "medium"},
			{Severity: "low"},
			{Severity: "info"},
			{Severity: "info"},
		},
	}
	
	summary := agent.buildSummary()
	
	if summary.Critical != 2 {
		t.Errorf("Expected 2 critical findings, got %d", summary.Critical)
	}
	if summary.High != 1 {
		t.Errorf("Expected 1 high finding, got %d", summary.High)
	}
	if summary.Medium != 1 {
		t.Errorf("Expected 1 medium finding, got %d", summary.Medium)
	}
	if summary.Low != 1 {
		t.Errorf("Expected 1 low finding, got %d", summary.Low)
	}
	if summary.Info != 2 {
		t.Errorf("Expected 2 info findings, got %d", summary.Info)
	}
}

func TestAgent_ExecuteTrusted(t *testing.T) {
	cleanRoom, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cleanRoom.Cleanup()
	
	logger, _ := zap.NewDevelopment()
	agent := &Agent{
		cleanRoom: cleanRoom,
		logger:    logger,
	}
	
	tc := ToolCall{
		Function: Function{
			Name: "execute_trusted",
		},
	}
	
	args := map[string]any{
		"command": "echo",
		"args":    []any{"test"},
	}
	
	result, err := agent.execExecuteTrusted(tc, args)
	
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if !strings.Contains(result.Content, "test") {
		t.Errorf("Expected output to contain 'test', got %s", result.Content)
	}
}

func TestAgent_ExecuteTrusted_MissingCommand(t *testing.T) {
	cleanRoom, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cleanRoom.Cleanup()
	
	logger, _ := zap.NewDevelopment()
	agent := &Agent{
		cleanRoom: cleanRoom,
		logger:    logger,
	}
	
	tc := ToolCall{
		Function: Function{
			Name: "execute_trusted",
		},
	}
	
	args := map[string]any{}
	
	_, err = agent.execExecuteTrusted(tc, args)
	
	if err == nil {
		t.Error("Expected error when command is missing")
	}
}

func TestAgent_ReadFile(t *testing.T) {
	cleanRoom, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cleanRoom.Cleanup()
	
	logger, _ := zap.NewDevelopment()
	agent := &Agent{
		cleanRoom: cleanRoom,
		logger:    logger,
	}
	
	tc := ToolCall{
		Function: Function{
			Name: "read_file",
		},
	}
	
	args := map[string]any{
		"path": "/etc/passwd",
	}
	
	result, err := agent.execReadFile(tc, args)
	
	if err != nil {
		t.Errorf("Expected no error reading /etc/passwd, got %v", err)
	}
	if result.Content == "" {
		t.Error("Expected non-empty content")
	}
}

func TestAgent_ListDir(t *testing.T) {
	cleanRoom, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cleanRoom.Cleanup()
	
	logger, _ := zap.NewDevelopment()
	agent := &Agent{
		cleanRoom: cleanRoom,
		logger:    logger,
	}
	
	tc := ToolCall{
		Function: Function{
			Name: "list_dir",
		},
	}
	
	args := map[string]any{
		"path": "/",
	}
	
	result, err := agent.execListDir(tc, args)
	
	if err != nil {
		t.Errorf("Expected no error listing /, got %v", err)
	}
	if result.Content == "" {
		t.Error("Expected non-empty content")
	}
}

func TestAgent_SearchFiles(t *testing.T) {
	cleanRoom, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cleanRoom.Cleanup()
	
	logger, _ := zap.NewDevelopment()
	agent := &Agent{
		cleanRoom: cleanRoom,
		logger:    logger,
	}
	
	tc := ToolCall{
		Function: Function{
			Name: "search_files",
		},
	}
	
	args := map[string]any{
		"pattern": "root",
		"path":    "/etc",
	}
	
	result, err := agent.execSearchFiles(tc, args)
	
	if err != nil {
		t.Errorf("Expected no error searching, got %v", err)
	}
	if result.Content == "" {
		t.Error("Expected non-empty content")
	}
}

func TestAgent_GetSystemEnv(t *testing.T) {
	cleanRoom, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cleanRoom.Cleanup()
	
	logger, _ := zap.NewDevelopment()
	agent := &Agent{
		cleanRoom: cleanRoom,
		logger:    logger,
	}
	
	tc := ToolCall{
		Function: Function{
			Name: "get_system_env",
		},
	}
	
	args := map[string]any{
		"pid": 1,
	}
	
	_, err = agent.execGetSystemEnv(tc, args)
	
	if err != nil {
		t.Logf("Expected permission denied error in test environment: %v", err)
	}
}

func TestAgent_Conclude(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	agent := &Agent{
		logger: logger,
	}
	
	tc := ToolCall{
		Function: Function{
			Name: "conclude",
		},
	}
	
	args := map[string]any{
		"summary": "Test summary",
	}
	
	result, err := agent.execConclude(tc, args)
	
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if !strings.Contains(result.Content, "Test summary") {
		t.Errorf("Expected content to contain 'Test summary', got %s", result.Content)
	}
}

func TestAgent_SubmitFinding(t *testing.T) {
	tempFile, _ := os.CreateTemp("", "jagr-test-*")
	defer os.Remove(tempFile.Name())
	
	logger, _ := zap.NewDevelopment()
	agent := &Agent{
		logger:   logger,
		findings: []Finding{},
	}
	
	tc := ToolCall{
		Function: Function{
			Name: "submit_finding",
		},
	}
	
	finding := Finding{
		ID:         "test-123",
		Type:       "test-type",
		Severity:   "high",
		Observable: "test-observable",
		Analysis:   "test-analysis",
		Evidence:   []string{"test-evidence"},
	}
	
	args := map[string]any{
		"finding": finding,
	}
	
	_, err := agent.execSubmitFinding(tc, args)
	
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if len(agent.findings) != 1 {
		t.Errorf("Expected 1 finding, got %d", len(agent.findings))
	}
	if agent.findings[0].ID != "test-123" {
		t.Errorf("Expected finding ID test-123, got %s", agent.findings[0].ID)
	}
}

func TestFindingsStruct(t *testing.T) {
	finding := Finding{
		ID:         "test-123",
		Type:       "vuln",
		Severity:   "high",
		Observable: "observable",
		Analysis:   "analysis",
		Evidence:   []string{"evidence"},
	}
	
	if finding.ID != "test-123" {
		t.Errorf("Expected ID test-123, got %s", finding.ID)
	}
}

func TestMessageStruct(t *testing.T) {
	msg := Message{
		Role:    "user",
		Content: "test",
		Name:    "test",
	}
	
	if msg.Role != "user" {
		t.Errorf("Expected role user, got %s", msg.Role)
	}
}

func TestFindingSummaryStruct(t *testing.T) {
	summary := FindingSummary{
		Critical: 1,
		High:     2,
		Medium:   3,
		Low:      4,
		Info:     5,
	}
	
	if summary.Critical != 1 {
		t.Errorf("Expected Critical 1, got %d", summary.Critical)
	}
}

func TestReportStruct(t *testing.T) {
	report := Report{
		Metadata: ReportMetadata{
			Project: "test",
			Version: "1.0",
		},
		Findings: []Finding{},
		Summary:  FindingSummary{},
	}
	
	if report.Metadata.Project != "test" {
		t.Errorf("Expected project test, got %s", report.Metadata.Project)
	}
}

func TestEventStruct(t *testing.T) {
	event := Event{
		Timestamp: time.Now(),
		Type:      "test",
		Data:      map[string]any{"key": "value"},
	}
	
	if event.Type != "test" {
		t.Errorf("Expected type test, got %s", event.Type)
	}
}

func TestAgent_StartTime(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	cleanRoom, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cleanRoom.Cleanup()
	
	agent, err := NewAgent(
		"http://localhost:8080",
		"test-key",
		"audit",
		10,
		5,
		"gpt-4",
		"test objective",
		"/tmp",
		logger,
		cleanRoom,
		false,
		0,
	)
	
	if err != nil {
		t.Fatalf("Failed to create agent: %v", err)
	}
	
	if agent.startTime.IsZero() {
		t.Error("Expected non-zero start time")
	}
}
