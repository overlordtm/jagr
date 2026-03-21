package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Tool defines an available LLM tool for the agent
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters,omitempty"`
}

// ToolCall represents an LLM tool call
type ToolCall struct {
	ID       string    `json:"id"`
	Type     string    `json:"type"`
	Function Function  `json:"function"`
}

type Function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolResult represents the result of executing a tool
type ToolResult struct {
	ToolID     string `json:"tool_id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error,omitempty"`
	ExitCode   int    `json:"exit_code,omitempty"`
}

// GetAvailableTools returns the list of tools the agent can use
func GetAvailableTools() []Tool {
	return []Tool{
		{
			Name:        "execute_trusted",
			Description: "Execute a command in the sanitized Clean Room environment. All commands run with PATH pointing to the clean toolset, protecting against host-level compromises.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The command binary name only, without arguments (e.g., 'ls', 'cat', 'grep'). Pass arguments separately in the 'args' array.",
					},
					"args": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Array of arguments for the command",
					},
				},
				"required": []string{"command"},
			},
		},
		{
			Name:        "read_file",
			Description: "Read content from a file on the host filesystem. Use this to examine configuration files, logs, or other text files.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the file to read",
					},
					"max_lines": map[string]any{
						"type":        "integer",
						"description": "Maximum number of lines to return (default: 1000)",
						"default":     1000,
					},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        "write_file",
			Description: "Write content to a file. Use this for creating remediation scripts or configuration files.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path where the file should be written",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "Content to write to the file",
					},
				},
				"required": []string{"path", "content"},
			},
		},
		{
			Name:        "get_system_env",
			Description: "Read environment variables from a process's /proc/environ file without loading them into the current process. This helps detect environment-based attacks.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pid": map[string]any{
						"type":        "integer",
						"description": "Process ID to read environ from (default: 1, init process)",
						"default":     1,
					},
				},
			},
		},
		{
			Name:        "run_linpeas_sh",
			Description: "Execute LinPEAS shell script for privilege escalation checks. Pre-filters results to only show critical/high findings in the initial output.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"flags": map[string]any{
						"type":        "string",
						"description": "Optional flags to pass to linpeas.sh (e.g., '-a' for all checks)",
						"default":     "-a",
					},
				},
			},
		},
		{
			Name:        "run_linpeas_static",
			Description: "Execute statically compiled LinPEAS binary. Faster than shell version with same functionality.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"flags": map[string]any{
						"type":        "string",
						"description": "Optional flags to pass to linpeas",
						"default":     "-a",
					},
				},
			},
		},
		{
			Name:        "run_pspy",
			Description: "Run pspy to monitor process creation events. Helps detect suspicious process execution patterns like cron spawning shells.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"duration_seconds": map[string]any{
						"type":        "integer",
						"description": "How long to run pspy in seconds (default: 30)",
						"default":     30,
					},
				},
			},
		},
		{
			Name:        "list_dir",
			Description: "List contents of a directory with metadata. Uses the clean ls tool to avoid host-level compromise.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the directory to list",
						"default":     "/",
					},
					"recursive": map[string]any{
						"type":        "boolean",
						"description": "Whether to list subdirectories recursively",
						"default":     false,
					},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        "search_files",
			Description: "Search for patterns in files using grep. Searches the entire filesystem by default.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Pattern to search for (regex)",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Directory to search in (default: /)",
						"default":     "/",
					},
					"max_results": map[string]any{
						"type":        "integer",
						"description": "Maximum number of results to return",
						"default":     100,
					},
				},
				"required": []string{"pattern"},
			},
		},
		{
			Name:        "get_network_info",
			Description: "Capture consolidated network state including interfaces, routes, connections, and listening ports.",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "submit_finding",
			Description: "Register a confirmed security finding to the agent's report. Include severity, type, observable, and analysis.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"finding": map[string]any{
						"type":        "object",
						"description": "Finding object with type, severity, observable, and analysis",
					},
				},
				"required": []string{"finding"},
			},
		},
		{
			Name:        "conclude",
			Description: "Signal investigation complete. Triggers report generation with all findings.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"summary": map[string]any{
						"type":        "string",
						"description": "Brief summary of the investigation findings",
					},
				},
				"required": []string{"summary"},
			},
		},
	}
}

// ParseToolArguments parses JSON arguments for a tool
func ParseToolArguments(args string) (map[string]any, error) {
	var result map[string]any
	if err := json.Unmarshal([]byte(args), &result); err != nil {
		return nil, err
	}
	return result, nil
}

// FormatToolOutput formats tool output for the LLM, applying filters and truncation
func FormatToolOutput(output string, tool string, maxLines int) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	
	if len(lines) > maxLines {
		// Show first and last lines with truncation notice
		truncated := make([]string, 0, maxLines)
		half := maxLines / 2
		for i := 0; i < half; i++ {
			if i < len(lines) {
				truncated = append(truncated, lines[i])
			}
		}
		truncated = append(truncated, fmt.Sprintf("... [truncated %d lines] ...", len(lines)-maxLines))
		for i := len(lines) - half; i < len(lines); i++ {
			if i >= 0 {
				truncated = append(truncated, lines[i])
			}
		}
		return strings.Join(truncated, "\n")
	}
	
	return output
}

// FilterLinPEASOutput extracts critical and high severity findings from LinPEAS output
func FilterLinPEASOutput(output string) string {
	var criticalHigh []string
	lines := strings.Split(output, "\n")
	
	for _, line := range lines {
		lineLower := strings.ToLower(line)
		// Look for RED/YELLOW color codes or severity keywords
		if strings.Contains(line, "[") && strings.Contains(line, "]") {
			// LinPEAS uses [CRITICAL], [HIGH], [MEDIUM], [LOW], [INFO]
			if strings.Contains(lineLower, "[critical") || strings.Contains(lineLower, "[high") {
				criticalHigh = append(criticalHigh, line)
			}
		}
	}
	
	return strings.Join(criticalHigh, "\n")
}

// DeduplicatepspyEvents removes duplicate process events and groups by binary
func DeduplicatepspyEvents(output string) string {
	events := make(map[string][]string)
	lines := strings.Split(output, "\n")
	
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Extract binary path (simplified - in production, use more robust parsing)
		binary := extractBinaryFrompspyLine(line)
		if binary != "" {
			events[binary] = append(events[binary], line)
		}
	}
	
	var result []string
	for binary, binaryEvents := range events {
		// Show one representative event per binary
		result = append(result, fmt.Sprintf("[%s] %d events", binary, len(binaryEvents)))
		if len(binaryEvents) > 0 {
			result = append(result, "  "+binaryEvents[0])
		}
	}
	
	return strings.Join(result, "\n")
}

func extractBinaryFrompspyLine(line string) string {
	// Simple extraction - in production, parse properly
	parts := strings.Fields(line)
	if len(parts) >= 3 {
		return parts[2]
	}
	return ""
}
