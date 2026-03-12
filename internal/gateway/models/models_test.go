package models

import (
	"testing"
)

func TestConfigStruct(t *testing.T) {
	config := Config{
		Server: ServerConfig{
			Listen: ":8080",
			TLS: TLSConfig{
				Cert: "cert.pem",
				Key:  "key.pem",
			},
		},
		Database: DatabaseConfig{
			Path: "/path/to/db",
		},
		RateLimit: RateLimitConfig{
			RequestsPerMinute: 100,
			MaxConcurrent:     10,
		},
		Session: SessionConfig{
			TimeoutMinutes: 30,
			HistoryMode:    "gateway",
		},
		Providers: []ProviderConfig{
			{
				Name:  "openai",
				Type:  "openai_compatible",
				BaseURL: "https://api.openai.com",
				APIKey:  "sk-test",
				Models: []ModelMapping{
					{Alias: "gpt-4", Upstream: "gpt-4"},
				},
			},
		},
		DefaultProvider: "openai",
		DefaultModel:    "gpt-4",
		Logging: LoggingConfig{
			Level: "info",
			Audit: true,
		},
	}
	
	if config.Server.Listen != ":8080" {
		t.Errorf("Expected listen :8080, got %s", config.Server.Listen)
	}
	if config.RateLimit.RequestsPerMinute != 100 {
		t.Errorf("Expected 100 requests per minute, got %d", config.RateLimit.RequestsPerMinute)
	}
}

func TestProviderConfigStruct(t *testing.T) {
	config := ProviderConfig{
		Name:      "custom",
		Type:      "openai_compatible",
		BaseURL:   "https://api.example.com",
		APIKey:    "test-key",
		Models: []ModelMapping{
			{Alias: "model1", Upstream: "upstream1"},
			{Alias: "model2", Upstream: "upstream2"},
		},
	}
	
	if config.Name != "custom" {
		t.Errorf("Expected name custom, got %s", config.Name)
	}
	if len(config.Models) != 2 {
		t.Errorf("Expected 2 models, got %d", len(config.Models))
	}
}

func TestMessageStruct(t *testing.T) {
	msg := Message{
		Role:    "user",
		Content: "Hello",
		Name:    "test",
		ToolCalls: []ToolCall{
			{
				ID: "call_123",
				Type: "function",
				Function: Function{
					Name:      "test",
					Arguments: `{"key":"value"}`,
				},
			},
		},
		ToolCallID: "tool_123",
	}
	
	if msg.Role != "user" {
		t.Errorf("Expected role user, got %s", msg.Role)
	}
	if msg.Content != "Hello" {
		t.Errorf("Expected content Hello, got %s", msg.Content)
	}
}

func TestChatCompletionRequestStruct(t *testing.T) {
	req := ChatCompletionRequest{
		Model:    "gpt-4",
		Messages: []Message{{Role: "user", Content: "test"}},
		Tools: []Tool{
			{
				Type: "function",
				Function: FunctionDef{
					Name:        "test",
					Description: "test function",
				},
			},
		},
		ToolChoice: "auto",
		Stream:     false,
	}
	
	if req.Model != "gpt-4" {
		t.Errorf("Expected model gpt-4, got %s", req.Model)
	}
	if len(req.Messages) != 1 {
		t.Errorf("Expected 1 message, got %d", len(req.Messages))
	}
}

func TestChatCompletionResponseStruct(t *testing.T) {
	resp := ChatCompletionResponse{
		ID:      "chatcmpl_test",
		Object:  "chat.completion",
		Created: 1234567890,
		Model:   "gpt-4",
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: "Test response",
				},
				FinishReason: "stop",
			},
		},
		Usage: &Usage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}
	
	if resp.Choices[0].Message.Content != "Test response" {
		t.Errorf("Expected content Test response, got %s", resp.Choices[0].Message.Content)
	}
}

func TestErrorResponseStruct(t *testing.T) {
	err := ErrorResponse{
		Error: ErrorInfo{
			Message: "Invalid request",
			Type:    "invalid_request_error",
			Code:    "invalid_param",
		},
	}
	
	if err.Error.Message != "Invalid request" {
		t.Errorf("Expected message Invalid request, got %s", err.Error.Message)
	}
}

func TestModelStruct(t *testing.T) {
	model := Model{
		ID:      "gpt-4",
		Object:  "model",
		Created: 1234567890,
		OwnedBy: "openai",
	}
	
	if model.ID != "gpt-4" {
		t.Errorf("Expected ID gpt-4, got %s", model.ID)
	}
}

func TestExerciseStruct(t *testing.T) {
	ex := Exercise{
		ID:        "ex-123",
		Name:      "Test Exercise",
		Status:    "active",
	}
	
	if ex.Name != "Test Exercise" {
		t.Errorf("Expected name Test Exercise, got %s", ex.Name)
	}
}

func TestAgentStruct(t *testing.T) {
	agent := Agent{
		ID:         "agent-123",
		ExerciseID: "ex-123",
		APIKeyHash: "sk-xxx",
		Hostname:   "test-host",
	}
	
	if agent.Hostname != "test-host" {
		t.Errorf("Expected hostname test-host, got %s", agent.Hostname)
	}
}

func TestSessionStruct(t *testing.T) {
	session := Session{
		ID:        "sess-123",
		AgentID:   "agent-123",
		Status:    "active",
	}
	
	if session.AgentID != "agent-123" {
		t.Errorf("Expected agentID agent-123, got %s", session.AgentID)
	}
}

func TestAuditLogStruct(t *testing.T) {
	log := AuditLog{
		ID:        1,
		AgentID:   "agent-123",
		EventType: "request",
		Payload:   `{"test":"data"}`,
	}
	
	if log.EventType != "request" {
		t.Errorf("Expected eventType request, got %s", log.EventType)
	}
}

func TestMessageLogStruct(t *testing.T) {
	log := MessageLog{
		ID:          1,
		SessionID:   "sess-123",
		Role:        "user",
		Content:     "test",
		TokensIn:    10,
		TokensOut:   20,
		LatencyMs:   100,
	}
	
	if log.Content != "test" {
		t.Errorf("Expected content test, got %s", log.Content)
	}
}
