package db

import (
	"os"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overlordtm/jagr/internal/gateway/models"
)

func TestNewStore(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "jagr-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()
	
	logger, _ := zap.NewDevelopment()
	store, err := NewStore(tmpFile.Name(), logger)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	
	if store == nil {
		t.Error("Expected non-nil store")
	}
}

func TestCreateExercise(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "jagr-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()
	
	logger, _ := zap.NewDevelopment()
	store, err := NewStore(tmpFile.Name(), logger)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	
	ex, err := store.CreateExercise("ex-123", "Test Exercise")
	if err != nil {
		t.Fatalf("Failed to create exercise: %v", err)
	}
	
	if ex.ID != "ex-123" {
		t.Errorf("Expected ID ex-123, got %s", ex.ID)
	}
	if ex.Name != "Test Exercise" {
		t.Errorf("Expected name Test Exercise, got %s", ex.Name)
	}
}

func TestCreateAgent(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "jagr-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()
	
	logger, _ := zap.NewDevelopment()
	store, err := NewStore(tmpFile.Name(), logger)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	
	_, err = store.CreateExercise("ex-123", "Test")
	if err != nil {
		t.Fatalf("Failed to create exercise: %v", err)
	}
	
	agent, err := store.CreateAgent("ex-123", "sk-test", "test-host")
	if err != nil {
		t.Fatalf("Failed to create agent: %v", err)
	}
	
	if agent.ID == "" {
		t.Error("Expected non-empty agent ID")
	}
	if agent.Hostname != "test-host" {
		t.Errorf("Expected hostname test-host, got %s", agent.Hostname)
	}
}

func TestCreateSession(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "jagr-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()
	
	logger, _ := zap.NewDevelopment()
	store, err := NewStore(tmpFile.Name(), logger)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	
	agent, err := store.CreateAgent("ex-123", "sk-test", "test-host")
	if err != nil {
		t.Fatalf("Failed to create agent: %v", err)
	}
	
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
	tmpFile, err := os.CreateTemp("", "jagr-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()
	
	logger, _ := zap.NewDevelopment()
	store, err := NewStore(tmpFile.Name(), logger)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	
	agent, err := store.CreateAgent("ex-123", "sk-test", "test-host")
	if err != nil {
		t.Fatalf("Failed to create agent: %v", err)
	}
	
	session, err := store.CreateSession(agent.ID)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	
	found, err := store.GetSession(session.ID)
	if err != nil {
		t.Fatalf("Failed to get session: %v", err)
	}
	
	if found.ID != session.ID {
		t.Errorf("Expected ID %s, got %s", session.ID, found.ID)
	}
}

func TestAppendMessage(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "jagr-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()
	
	logger, _ := zap.NewDevelopment()
	store, err := NewStore(tmpFile.Name(), logger)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	
	agent, err := store.CreateAgent("ex-123", "sk-test", "test-host")
	if err != nil {
		t.Fatalf("Failed to create agent: %v", err)
	}
	
	session, err := store.CreateSession(agent.ID)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	
	msg := &models.Message{
		Role:    "user",
		Content: "Hello",
	}
	
	err = store.AppendMessage(session.ID, msg, "gpt-4", 10, 20, 100)
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
	tmpFile, err := os.CreateTemp("", "jagr-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()
	
	logger, _ := zap.NewDevelopment()
	store, err := NewStore(tmpFile.Name(), logger)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	
	agent, err := store.CreateAgent("ex-123", "sk-test", "test-host")
	if err != nil {
		t.Fatalf("Failed to create agent: %v", err)
	}
	
	err = store.LogAudit(agent.ID, "test_event", map[string]any{"key": "value"})
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

func TestGetExercises(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "jagr-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()
	
	logger, _ := zap.NewDevelopment()
	store, err := NewStore(tmpFile.Name(), logger)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	
	_, err = store.CreateExercise("ex-1", "Exercise 1")
	if err != nil {
		t.Fatalf("Failed to create exercise 1: %v", err)
	}
	_, err = store.CreateExercise("ex-2", "Exercise 2")
	if err != nil {
		t.Fatalf("Failed to create exercise 2: %v", err)
	}
	
	exercises, err := store.GetExercises("")
	if err != nil {
		t.Fatalf("Failed to get exercises: %v", err)
	}
	
	if len(exercises) != 2 {
		t.Errorf("Expected 2 exercises, got %d", len(exercises))
	}
}

func TestGetAgents(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "jagr-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()
	
	logger, _ := zap.NewDevelopment()
	store, err := NewStore(tmpFile.Name(), logger)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	
	_, err = store.CreateExercise("ex-1", "Exercise 1")
	if err != nil {
		t.Fatalf("Failed to create exercise: %v", err)
	}
	
	_, err = store.CreateAgent("ex-1", "sk-test1", "host1")
	if err != nil {
		t.Fatalf("Failed to create agent 1: %v", err)
	}
	_, err = store.CreateAgent("ex-1", "sk-test2", "host2")
	if err != nil {
		t.Fatalf("Failed to create agent 2: %v", err)
	}
	
	agents, err := store.GetAgents("ex-1")
	if err != nil {
		t.Fatalf("Failed to get agents: %v", err)
	}
	
	if len(agents) != 2 {
		t.Errorf("Expected 2 agents, got %d", len(agents))
	}
}

func TestGetSessions(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "jagr-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()
	
	logger, _ := zap.NewDevelopment()
	store, err := NewStore(tmpFile.Name(), logger)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	
	agent, err := store.CreateAgent("ex-1", "sk-test", "test-host")
	if err != nil {
		t.Fatalf("Failed to create agent: %v", err)
	}
	
	_, err = store.CreateSession(agent.ID)
	if err != nil {
		t.Fatalf("Failed to create session 1: %v", err)
	}
	_, err = store.CreateSession(agent.ID)
	if err != nil {
		t.Fatalf("Failed to create session 2: %v", err)
	}
	
	sessions, err := store.GetSessions(agent.ID)
	if err != nil {
		t.Fatalf("Failed to get sessions: %v", err)
	}
	
	if len(sessions) != 2 {
		t.Errorf("Expected 2 sessions, got %d", len(sessions))
	}
}

func TestErrNotFound(t *testing.T) {
	if ErrNotFound == nil {
		t.Error("Expected ErrNotFound to be defined")
	}
}

func TestUpdateAgentHostname(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "jagr-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()
	
	logger, _ := zap.NewDevelopment()
	store, err := NewStore(tmpFile.Name(), logger)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	
	agent, err := store.CreateAgent("ex-1", "sk-test", "original-host")
	if err != nil {
		t.Fatalf("Failed to create agent: %v", err)
	}
	
	err = store.UpdateAgentHostname(agent.ID, "updated-host")
	if err != nil {
		t.Fatalf("Failed to update hostname: %v", err)
	}
	
	found, err := store.GetAgent(agent.ID)
	if err != nil {
		t.Fatalf("Failed to get agent: %v", err)
	}
	
	if found.Hostname != "updated-host" {
		t.Errorf("Expected hostname updated-host, got %s", found.Hostname)
	}
}

func TestCloseSession(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "jagr-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()
	
	logger, _ := zap.NewDevelopment()
	store, err := NewStore(tmpFile.Name(), logger)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	
	agent, err := store.CreateAgent("ex-1", "sk-test", "test-host")
	if err != nil {
		t.Fatalf("Failed to create agent: %v", err)
	}
	
	session, err := store.CreateSession(agent.ID)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	
	err = store.CloseSession(session.ID)
	if err != nil {
		t.Fatalf("Failed to close session: %v", err)
	}
	
	found, err := store.GetSession(session.ID)
	if err != nil {
		t.Fatalf("Failed to get session: %v", err)
	}
	
	if found.Status != "closed" {
		t.Errorf("Expected status closed, got %s", found.Status)
	}
}
