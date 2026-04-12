package agent

import (
	"strings"
	"testing"
)

func TestGetAvailableTools(t *testing.T) {
	tools := GetAvailableTools()
	
	expectedTools := []string{
		"execute_trusted",
		"read_file",
		"write_file",
		"get_system_env",
		"run_linpeas_sh",
		"run_linpeas_static",
		"run_pspy",
		"list_dir",
		"search_files",
		"get_network_info",
		"submit_finding",
		"delegate_investigation",
		"conclude",
		"check_cron",
		"check_users",
		"check_systemd",
		"check_suid",
		"check_modules",
		"check_listeners",
		"write_memo",
		"read_memos",
		"query_knowledge_base",
		"read_cached_output",
	}

	if len(tools) != len(expectedTools) {
		t.Errorf("Expected %d tools, got %d", len(expectedTools), len(tools))
	}
	
	toolMap := make(map[string]bool)
	for _, tool := range tools {
		toolMap[tool.Name] = true
	}
	
	for _, expected := range expectedTools {
		if !toolMap[expected] {
			t.Errorf("Missing expected tool: %s", expected)
		}
	}
}

func TestParseToolArguments(t *testing.T) {
	tests := []struct {
		name     string
		args     string
		expected map[string]any
		hasError bool
	}{
		{
			name:     "valid simple args",
			args:     `{"key": "value"}`,
			expected: map[string]any{"key": "value"},
			hasError: false,
		},
		{
			name:     "valid nested args",
			args:     `{"nested": {"key": "value"}}`,
			expected: nil,
			hasError: false,
		},
		{
			name:     "invalid json",
			args:     `{invalid}`,
			expected: nil,
			hasError: true,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseToolArguments(tt.args)
			if tt.hasError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.hasError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if result != nil && !tt.hasError {
				for k, v := range tt.expected {
					if result[k] != v {
						t.Errorf("Expected %v for key %s, got %v", v, k, result[k])
					}
				}
			}
		})
	}
}

func TestFormatToolOutput(t *testing.T) {
	longOutput := strings.Repeat("line\n", 2000)
	
	tests := []struct {
		name           string
		output         string
		tool           string
		maxLines       int
		shouldTruncate bool
	}{
		{
			name:           "under limit",
			output:         "line1\nline2",
			tool:           "test",
			maxLines:       1000,
			shouldTruncate: false,
		},
		{
			name:           "over limit",
			output:         longOutput,
			tool:           "test",
			maxLines:       100,
			shouldTruncate: true,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatToolOutput(tt.output, tt.tool, tt.maxLines)
			
			if tt.shouldTruncate {
				if !strings.Contains(result, "[truncated") {
					t.Error("Expected truncated output but truncation notice not found")
				}
				lines := strings.Split(result, "\n")
				if len(lines) > tt.maxLines+1 {
					t.Errorf("Expected max %d lines, got %d", tt.maxLines+1, len(lines))
				}
			}
		})
	}
}

func TestFilterLinPEASOutput(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected int
	}{
		{
			name:     "extracts critical and high",
			output:   "[CRITICAL] Critical finding\n[HIGH] High finding\n[MEDIUM] Medium finding\n",
			expected: 2,
		},
		{
			name:     "filters out low and info",
			output:   "[HIGH] High\n[LOW] Low\n[INFO] Info\n",
			expected: 1,
		},
		{
			name:     "empty output",
			output:   "",
			expected: 0,
		},
		{
			name:     "single critical",
			output:   "[CRITICAL] Critical finding",
			expected: 1,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FilterLinPEASOutput(tt.output)
			if !strings.Contains(result, "[truncated") {
				lines := strings.Split(result, "\n")
				var nonEmpty int
				for _, line := range lines {
					if strings.TrimSpace(line) != "" {
						nonEmpty++
					}
				}
				if nonEmpty != tt.expected {
					t.Errorf("Expected %d non-empty lines, got %d (output: %s)", tt.expected, nonEmpty, result)
				}
			}
		})
	}
}

func TestDeduplicatepspyEvents(t *testing.T) {
	output := `2024/01/01 12:00:00.000000 [1234] /bin/bash args
2024/01/01 12:00:00.100000 [1234] /bin/bash args
2024/01/01 12:00:00.200000 [5678] /bin/ls args`
	
	result := DeduplicatepspyEvents(output)
	
	if !strings.Contains(result, "[1234]") {
		t.Errorf("Expected [1234] to be in result, got: %s", result)
	}
	if !strings.Contains(result, "[5678]") {
		t.Errorf("Expected [5678] to be in result, got: %s", result)
	}
	if !strings.Contains(result, "2 events") {
		t.Errorf("Expected '2 events' to be in result, got: %s", result)
	}
	if !strings.Contains(result, "1 events") {
		t.Errorf("Expected '1 events' to be in result, got: %s", result)
	}
}

func TestExtractBinaryFrompspyLine(t *testing.T) {
	tests := []struct {
		line     string
		expected string
	}{
		{"2024/01/01 12:00:00.000000 [1234] /bin/bash args", "[1234]"},
		{"2024/01/01 12:00:00.000000 [1] /bin/ls -la", "[1]"},
	}
	
	for _, tt := range tests {
		result := extractBinaryFrompspyLine(tt.line)
		if result != tt.expected {
			t.Errorf("For line %q, expected %q, got %q", tt.line, tt.expected, result)
		}
	}
}

func TestToolStruct(t *testing.T) {
	tool := Tool{
		Name:        "execute_trusted",
		Description: "Test description",
		Parameters:  map[string]any{"key": "value"},
	}
	
	if tool.Name != "execute_trusted" {
		t.Errorf("Expected name execute_trusted, got %s", tool.Name)
	}
}

func TestToolCallStruct(t *testing.T) {
	call := ToolCall{
		ID:   "call_123",
		Type: "function",
		Function: Function{
			Name:      "test",
			Arguments: `{"key": "value"}`,
		},
	}
	
	if call.ID != "call_123" {
		t.Errorf("Expected ID call_123, got %s", call.ID)
	}
}

func TestToolResultStruct(t *testing.T) {
	result := ToolResult{
		ToolID:   "tool_123",
		Name:     "test",
		Content:  "output",
		IsError:  true,
		ExitCode: 1,
	}
	
	if result.IsError != true {
		t.Error("Expected IsError to be true")
	}
	if result.ExitCode != 1 {
		t.Error("Expected ExitCode to be 1")
	}
}
