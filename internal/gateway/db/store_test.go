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
	if found.Status != "closed" {
		t.Errorf("Expected status closed, got %s", found.Status)
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

	err := store.AppendMessage(session.ID, msg, "gpt-4", 10, 20, 0.001, 100)
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
