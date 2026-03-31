package db

import (
	"os"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overlordtm/jagr/internal/gateway/models"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "jagr-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	t.Cleanup(func() {
		os.Remove(tmpFile.Name())
		tmpFile.Close()
	})

	logger, _ := zap.NewDevelopment()
	store, err := NewStore(tmpFile.Name(), logger)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	return store
}

func TestNewStore(t *testing.T) {
	store := newTestStore(t)
	if store == nil {
		t.Error("Expected non-nil store")
	}
}

func TestFindOrCreateAgentByHostname(t *testing.T) {
	store := newTestStore(t)

	// First call creates the agent
	agent, err := store.FindOrCreateAgentByHostname("host-a")
	if err != nil {
		t.Fatalf("Failed to find or create agent: %v", err)
	}
	if agent.Hostname != "host-a" {
		t.Errorf("Expected hostname host-a, got %s", agent.Hostname)
	}
	if agent.ID == "" {
		t.Error("Expected non-empty agent ID")
	}

	// Second call returns the same agent
	agent2, err := store.FindOrCreateAgentByHostname("host-a")
	if err != nil {
		t.Fatalf("Failed to find agent: %v", err)
	}
	if agent2.ID != agent.ID {
		t.Errorf("Expected same agent ID %s, got %s", agent.ID, agent2.ID)
	}

	// Different hostname creates a different agent
	agent3, err := store.FindOrCreateAgentByHostname("host-b")
	if err != nil {
		t.Fatalf("Failed to find or create agent: %v", err)
	}
	if agent3.ID == agent.ID {
		t.Error("Expected different agent ID for different hostname")
	}
}

func TestGetAgents(t *testing.T) {
	store := newTestStore(t)

	store.FindOrCreateAgentByHostname("host-1")
	store.FindOrCreateAgentByHostname("host-2")

	agents, err := store.GetAgents()
	if err != nil {
		t.Fatalf("Failed to get agents: %v", err)
	}
	if len(agents) != 2 {
		t.Errorf("Expected 2 agents, got %d", len(agents))
	}
}

func TestDeleteAgentByHostname(t *testing.T) {
	store := newTestStore(t)

	store.FindOrCreateAgentByHostname("host-del")

	err := store.DeleteAgentByHostname("host-del")
	if err != nil {
		t.Fatalf("Failed to delete agent: %v", err)
	}

	err = store.DeleteAgentByHostname("host-del")
	if err != ErrNotFound {
		t.Errorf("Expected ErrNotFound, got %v", err)
	}
}

func TestCreateSession(t *testing.T) {
	store := newTestStore(t)

	agent, _ := store.FindOrCreateAgentByHostname("test-host")

	session, err := store.CreateSession(agent.ID)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	if session.ID == "" {
		t.Error("Expected non-empty session ID")
	}
	if session.AgentID != agent.ID {
		t.Errorf("Expected agentID %s, got %s", agent.ID, session.AgentID)
	}
}

func TestGetSession(t *testing.T) {
	store := newTestStore(t)

	agent, _ := store.FindOrCreateAgentByHostname("test-host")
	session, _ := store.CreateSession(agent.ID)

	found, err := store.GetSession(session.ID)
	if err != nil {
		t.Fatalf("Failed to get session: %v", err)
	}
	if found.ID != session.ID {
		t.Errorf("Expected ID %s, got %s", session.ID, found.ID)
	}
}

func TestGetSessions(t *testing.T) {
	store := newTestStore(t)

	agent, _ := store.FindOrCreateAgentByHostname("test-host")
	store.CreateSession(agent.ID)
	store.CreateSession(agent.ID)

	sessions, err := store.GetSessions(agent.ID)
	if err != nil {
		t.Fatalf("Failed to get sessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("Expected 2 sessions, got %d", len(sessions))
	}
}

func TestCloseSession(t *testing.T) {
	store := newTestStore(t)

	agent, _ := store.FindOrCreateAgentByHostname("test-host")
	session, _ := store.CreateSession(agent.ID)

	err := store.CloseSession(session.ID)
	if err != nil {
		t.Fatalf("Failed to close session: %v", err)
	}

	found, _ := store.GetSession(session.ID)
	if found.Status != "completed" {
		t.Errorf("Expected status completed, got %s", found.Status)
	}
}

func TestAppendMessage(t *testing.T) {
	store := newTestStore(t)

	agent, _ := store.FindOrCreateAgentByHostname("test-host")
	session, _ := store.CreateSession(agent.ID)

	msg := &models.Message{
		Role:    "user",
		Content: "Hello",
	}

	err := store.AppendMessage(session.ID, msg, "gpt-4", 10, 20, 0, 0.001, 100)
	if err != nil {
		t.Fatalf("Failed to append message: %v", err)
	}

	count, err := store.GetMessageCount(session.ID)
	if err != nil {
		t.Fatalf("Failed to get message count: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 message, got %d", count)
	}
}

func TestLogAudit(t *testing.T) {
	store := newTestStore(t)

	agent, _ := store.FindOrCreateAgentByHostname("test-host")

	err := store.LogAudit(agent.ID, "test_event", map[string]any{"key": "value"})
	if err != nil {
		t.Fatalf("Failed to log audit: %v", err)
	}

	logs, err := store.GetAuditLog(agent.ID, time.Time{}, time.Time{}, "")
	if err != nil {
		t.Fatalf("Failed to get audit log: %v", err)
	}
	if len(logs) != 1 {
		t.Errorf("Expected 1 audit log, got %d", len(logs))
	}
}

func TestErrNotFound(t *testing.T) {
	if ErrNotFound == nil {
		t.Error("Expected ErrNotFound to be defined")
	}
}

func TestUpdateFindingStatus(t *testing.T) {
	store := newTestStore(t)

	agent, _ := store.FindOrCreateAgentByHostname("host-finding")
	session, _ := store.CreateSession(agent.ID)

	// Add a finding with preliminary status
	err := store.AddFinding(session.ID, &models.SessionFinding{
		FindingID:  "finding-1",
		Type:       "misconfiguration",
		Severity:   "high",
		Observable: "/etc/shadow",
		Analysis:   "World-readable shadow file",
		Evidence:   `["ls -la /etc/shadow"]`,
		Status:     "preliminary",
	})
	if err != nil {
		t.Fatalf("Failed to add finding: %v", err)
	}

	// Verify preliminary status
	findings, err := store.GetSessionFindings(session.ID)
	if err != nil {
		t.Fatalf("Failed to get findings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("Expected 1 finding, got %d", len(findings))
	}
	if findings[0].Status != "preliminary" {
		t.Errorf("Expected status preliminary, got %s", findings[0].Status)
	}

	// Update to valid
	err = store.UpdateFindingStatus(session.ID, "finding-1", "valid")
	if err != nil {
		t.Fatalf("Failed to update finding status: %v", err)
	}

	// Verify updated status
	findings, err = store.GetSessionFindings(session.ID)
	if err != nil {
		t.Fatalf("Failed to get findings after update: %v", err)
	}
	if findings[0].Status != "valid" {
		t.Errorf("Expected status valid, got %s", findings[0].Status)
	}

	// Add another finding and mark as duplicate
	store.AddFinding(session.ID, &models.SessionFinding{
		FindingID:  "finding-2",
		Type:       "misconfiguration",
		Severity:   "high",
		Observable: "/etc/shadow",
		Status:     "preliminary",
	})
	err = store.UpdateFindingStatus(session.ID, "finding-2", "duplicate")
	if err != nil {
		t.Fatalf("Failed to update finding status to duplicate: %v", err)
	}

	findings, err = store.GetSessionFindings(session.ID)
	if err != nil {
		t.Fatalf("Failed to get findings: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("Expected 2 findings, got %d", len(findings))
	}
	if findings[1].Status != "duplicate" {
		t.Errorf("Expected status duplicate, got %s", findings[1].Status)
	}
}

func TestGetLastMessageByName(t *testing.T) {
	store := newTestStore(t)
	agent, _ := store.FindOrCreateAgentByHostname("test-host")
	session, _ := store.CreateSession(agent.ID)

	msg1 := &models.Message{Role: "user", Content: "Hello", AgentName: "agent-1"}
	msg2 := &models.Message{Role: "assistant", Content: "Hi", AgentName: "agent-1"}
	msg3 := &models.Message{Role: "user", Content: "Other", AgentName: "agent-2"}

	store.AppendMessage(session.ID, msg1, "m1", 0, 0, 0, 0, 0)
	store.AppendMessage(session.ID, msg2, "m1", 0, 0, 0, 0, 0)
	store.AppendMessage(session.ID, msg3, "m1", 0, 0, 0, 0, 0)

	last, err := store.GetLastMessageByName(session.ID, "agent-1")
	if err != nil {
		t.Fatalf("Failed to get last message for agent-1: %v", err)
	}
	if last.Content != "Hi" {
		t.Errorf("Expected 'Hi', got %s", last.Content)
	}

	last2, err := store.GetLastMessageByName(session.ID, "agent-2")
	if err != nil {
		t.Fatalf("Failed to get last message for agent-2: %v", err)
	}
	if last2.Content != "Other" {
		t.Errorf("Expected 'Other', got %s", last2.Content)
	}

	_, err = store.GetLastMessageByName(session.ID, "non-existent")
	if err != ErrNotFound {
		t.Errorf("Expected ErrNotFound, got %v", err)
	}
}

func TestMemos(t *testing.T) {
	store := newTestStore(t)
	agent, _ := store.FindOrCreateAgentByHostname("test-host")
	session, _ := store.CreateSession(agent.ID)

	// Create a memo with agent name
	memo, err := store.CreateMemo(agent.ID, session.ID, "host-1", "host", "Observation 1", "observation", "agent-x")
	if err != nil {
		t.Fatalf("Failed to create memo: %v", err)
	}
	if memo.AgentName != "agent-x" {
		t.Errorf("Expected agent-x, got %s", memo.AgentName)
	}

	// Retrieve memos
	memos, err := store.GetMemos(agent.ID, "host", "host-1", "", "", "", 10)
	if err != nil {
		t.Fatalf("Failed to get memos: %v", err)
	}
	if len(memos) != 1 {
		t.Fatalf("Expected 1 memo, got %d", len(memos))
	}
	if memos[0].AgentName != "agent-x" {
		t.Errorf("Expected agent-x in retrieved memo, got %s", memos[0].AgentName)
	}
}
