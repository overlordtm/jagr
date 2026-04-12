package agent

import (
	"os"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestJagrHarness_Hostname(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	gw := &GatewayClient{
		hostname: "test-host",
		logger:   logger,
	}

	if gw.Hostname() == "" {
		t.Error("Expected non-empty hostname")
	}
}

func TestFindingsStore_GetSummary(t *testing.T) {
	store := NewFindingsStore()
	store.Add(Finding{Severity: "critical"}, "agent-1")
	store.Add(Finding{Severity: "critical", Observable: "obs-2"}, "agent-1")
	store.Add(Finding{Severity: "high", Observable: "obs-3"}, "agent-1")
	store.Add(Finding{Severity: "medium", Observable: "obs-4"}, "agent-1")
	store.Add(Finding{Severity: "low", Observable: "obs-5"}, "agent-1")
	store.Add(Finding{Severity: "info", Observable: "obs-6"}, "agent-1")
	store.Add(Finding{Severity: "info", Observable: "obs-7"}, "agent-1")

	summary := store.GetSummary()

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

func TestToolBox_ExecuteTrusted(t *testing.T) {
	cleanRoom, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cleanRoom.Cleanup()

	logger, _ := zap.NewDevelopment()
	tb := NewToolBox(cleanRoom, 5, logger)

	tc := ToolCall{
		Function: Function{
			Name:      "execute_trusted",
			Arguments: `{"command": "echo", "args": ["test"]}`,
		},
	}

	result, err := tb.ExecuteTool(tc)

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if !strings.Contains(result.Content, "test") {
		t.Errorf("Expected output to contain 'test', got %s", result.Content)
	}
}

func TestToolBox_ExecuteTrusted_MissingCommand(t *testing.T) {
	cleanRoom, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cleanRoom.Cleanup()

	logger, _ := zap.NewDevelopment()
	tb := NewToolBox(cleanRoom, 5, logger)

	tc := ToolCall{
		Function: Function{
			Name:      "execute_trusted",
			Arguments: `{}`,
		},
	}

	_, err = tb.ExecuteTool(tc)

	if err == nil {
		t.Error("Expected error when command is missing")
	}
}

func TestToolBox_ReadFile(t *testing.T) {
	cleanRoom, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cleanRoom.Cleanup()

	logger, _ := zap.NewDevelopment()
	tb := NewToolBox(cleanRoom, 5, logger)

	tc := ToolCall{
		Function: Function{
			Name:      "read_file",
			Arguments: `{"path": "/etc/passwd"}`,
		},
	}

	result, err := tb.ExecuteTool(tc)

	if err != nil {
		t.Errorf("Expected no error reading /etc/passwd, got %v", err)
	}
	if result.Content == "" {
		t.Error("Expected non-empty content")
	}
}

func TestToolBox_ListDir(t *testing.T) {
	cleanRoom, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cleanRoom.Cleanup()

	logger, _ := zap.NewDevelopment()
	tb := NewToolBox(cleanRoom, 5, logger)

	tc := ToolCall{
		Function: Function{
			Name:      "list_dir",
			Arguments: `{"path": "/"}`,
		},
	}

	result, err := tb.ExecuteTool(tc)

	if err != nil {
		t.Errorf("Expected no error listing /, got %v", err)
	}
	if result.Content == "" {
		t.Error("Expected non-empty content")
	}
}

func TestToolBox_SearchFiles(t *testing.T) {
	cleanRoom, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cleanRoom.Cleanup()

	logger, _ := zap.NewDevelopment()
	tb := NewToolBox(cleanRoom, 5, logger)

	tc := ToolCall{
		Function: Function{
			Name:      "search_files",
			Arguments: `{"pattern": "root", "path": "/etc"}`,
		},
	}

	result, err := tb.ExecuteTool(tc)

	if err != nil {
		t.Errorf("Expected no error searching, got %v", err)
	}
	if result.Content == "" {
		t.Error("Expected non-empty content")
	}
}

func TestToolBox_GetSystemEnv(t *testing.T) {
	cleanRoom, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cleanRoom.Cleanup()

	logger, _ := zap.NewDevelopment()
	tb := NewToolBox(cleanRoom, 5, logger)

	tc := ToolCall{
		Function: Function{
			Name:      "get_system_env",
			Arguments: `{"pid": 1}`,
		},
	}

	_, err = tb.ExecuteTool(tc)

	if err != nil {
		t.Logf("Expected permission denied error in test environment: %v", err)
	}
}

func TestFindingsStore_Add(t *testing.T) {
	store := NewFindingsStore()

	finding := Finding{
		ID:         "test-123",
		Type:       "test-type",
		Severity:   "high",
		Observable: "test-observable",
		Analysis:   "test-analysis",
		Evidence:   []string{"test-evidence"},
	}

	stored, isDuplicate := store.Add(finding, "test-agent")

	if isDuplicate {
		t.Error("Expected finding not to be duplicate")
	}
	if stored.ID != "test-123" {
		t.Errorf("Expected finding ID test-123, got %s", stored.ID)
	}
	if stored.AgentName != "test-agent" {
		t.Errorf("Expected agent name test-agent, got %s", stored.AgentName)
	}
	if store.Count() != 1 {
		t.Errorf("Expected 1 finding, got %d", store.Count())
	}

	// Test deduplication
	_, isDuplicate = store.Add(Finding{Observable: "test-observable"}, "test-agent-2")
	if !isDuplicate {
		t.Error("Expected duplicate finding")
	}
	if store.Count() != 1 {
		t.Errorf("Expected still 1 finding after duplicate, got %d", store.Count())
	}
}

func TestToolBox_Pspy(t *testing.T) {
	cleanRoom, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cleanRoom.Cleanup()

	logger, _ := zap.NewDevelopment()
	tb := NewToolBox(cleanRoom, 5, logger)

	tc := ToolCall{
		Function: Function{
			Name:      "run_pspy",
			Arguments: `{"duration_seconds": 1}`,
		},
	}

	result, err := tb.ExecuteTool(tc)

	if err != nil && !strings.Contains(err.Error(), "command timed out") {
		t.Errorf("Unexpected error: %v", err)
	}

	t.Logf("Pspy result: %v", result)
}

func TestFindingsStruct(t *testing.T) {
	finding := Finding{
		ID:         "test-123",
		Type:       "vuln",
		Severity:   "high",
		Observable: "observable",
		Analysis:   "analysis",
		Evidence:   []string{"evidence"},
		AgentName:  "test-agent",
	}

	if finding.ID != "test-123" {
		t.Errorf("Expected ID test-123, got %s", finding.ID)
	}
	if finding.AgentName != "test-agent" {
		t.Errorf("Expected AgentName test-agent, got %s", finding.AgentName)
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

func TestJagrHarness_StartTime(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	cleanRoom, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cleanRoom.Cleanup()

	gw := NewGatewayClient(
		"http://localhost:8080",
		"test-key",
		"test-host",
		"gpt-4",
		logger,
		false,
		0,
		"",
		"",
	)

	harness := NewJagrHarness(
		"audit",
		10,
		5,
		"gpt-4",
		"test objective",
		"/tmp",
		logger,
		gw,
		cleanRoom,
	)

	if harness.startTime.IsZero() {
		t.Error("Expected non-zero start time")
	}
}

func TestToolBox_CircuitBreaker(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	cleanRoom, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cleanRoom.Cleanup()

	tb := NewToolBox(cleanRoom, 3, logger)

	if tb.IsCircuitBroken("test_tool") {
		t.Error("Circuit should not be broken initially")
	}

	tb.IncrementFailure("test_tool")
	tb.IncrementFailure("test_tool")
	if tb.IsCircuitBroken("test_tool") {
		t.Error("Circuit should not be broken after 2 failures (max 3)")
	}

	tb.IncrementFailure("test_tool")
	if !tb.IsCircuitBroken("test_tool") {
		t.Error("Circuit should be broken after 3 failures")
	}

	tb.ResetFailure("test_tool")
	if tb.IsCircuitBroken("test_tool") {
		t.Error("Circuit should not be broken after reset")
	}
}

func TestFindingsStore_Validate(t *testing.T) {
	store := NewFindingsStore()
	store.Add(Finding{Observable: "obs-1", Severity: "high", Type: "vuln"}, "agent-1")
	store.Add(Finding{Observable: "obs-2", Severity: "info", Type: "incomplete_investigation"}, "agent-2")

	updates := store.Validate()

	if len(updates) != 2 {
		t.Fatalf("Expected 2 updates, got %d", len(updates))
	}
	if updates[0].Status != "valid" {
		t.Errorf("Expected first finding to be valid, got %s", updates[0].Status)
	}
	if updates[1].Status != "invalid" {
		t.Errorf("Expected incomplete_investigation/info finding to be invalid, got %s", updates[1].Status)
	}
}

func TestFindingsStore_AutoID(t *testing.T) {
	store := NewFindingsStore()
	stored, _ := store.Add(Finding{Observable: "obs-1"}, "agent-1")
	if stored.ID != "finding-1" {
		t.Errorf("Expected auto-assigned ID finding-1, got %s", stored.ID)
	}

	stored2, _ := store.Add(Finding{Observable: "obs-2"}, "agent-1")
	if stored2.ID != "finding-2" {
		t.Errorf("Expected auto-assigned ID finding-2, got %s", stored2.ID)
	}
}

// Ensure unused import doesn't cause build failure
var _ = os.Remove
