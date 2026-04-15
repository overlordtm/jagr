package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Tool defines an available LLM tool for the agent
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters,omitempty"`
}

// ToolCall represents an LLM tool call
type ToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

type Function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolResult represents the result of executing a tool
type ToolResult struct {
	ToolID   string        `json:"tool_id"`
	Name     string        `json:"name"`
	Content  string        `json:"content"`
	IsError  bool          `json:"is_error,omitempty"`
	ExitCode int           `json:"exit_code,omitempty"`
	Duration time.Duration `json:"-"`
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
			Description: "Register a confirmed security finding. Call once per distinct finding.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"type": map[string]any{
						"type":        "string",
						"description": "Finding category (e.g. backdoor, persistence, privesc, credential_exposure, c2, rootkit, misconfiguration)",
					},
					"severity": map[string]any{
						"type":        "string",
						"enum":        []string{"critical", "high", "medium", "low", "info"},
						"description": "Impact severity",
					},
					"observable": map[string]any{
						"type":        "string",
						"description": "The specific artifact: file path, process name, cron entry, network endpoint, etc.",
					},
					"analysis": map[string]any{
						"type":        "string",
						"description": "What you found and why it is a security issue. Include MITRE ATT&CK technique ID if applicable.",
					},
					"evidence": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Supporting evidence: relevant command output snippets, file contents, timestamps.",
					},
				},
				"required": []string{"type", "severity", "observable", "analysis"},
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
		{
			Name:        "query_knowledge_base",
			Description: "Search the exercise knowledge base for relevant exercise documentation, network maps, and manuals for specialized software. Use this if you find something you have no knowledge of.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Natural language search query describing what you want to know",
					},
					"top_k": map[string]any{
						"type":        "integer",
						"description": "Number of top entities/relations to retrieve (default: 5)",
						"default":     5,
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "check_cron",
			Description: "Analyze all cron jobs on the system. Returns every cron entry with metadata about the binary it executes (existence, package ownership, file type, timestamps). You must review ALL entries and determine which are suspicious.",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "check_users",
			Description: "Analyze all user accounts with login shells. Returns each user with UID, groups, password aging, SSH authorized_keys count, sudo rules, and home directory status.",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "check_systemd",
			Description: "Analyze custom/modified systemd units in /etc/systemd/system and /run/systemd/system. Returns each unit with ExecStart binary metadata, package ownership, and drop-in override detection.",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "check_suid",
			Description: "Find all SUID/SGID binaries and files with capabilities. Returns each with permissions, package ownership, SHA256 hash, file type, and modification time.",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "check_modules",
			Description: "Analyze loaded kernel modules. Cross-references with on-disk module files, package ownership, module parameters, and dmesg load messages.",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "check_listeners",
			Description: "Analyze all TCP/UDP network listeners. Returns each with process name, PID, binary path, package ownership, command line, and deleted-binary detection.",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "write_memo",
			Description: "Write a note to remember later. Use 'agent' scope for private notes, 'host' scope to share with other agents on this host, 'exercise' scope for cross-host observations.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"scope": map[string]any{
						"type":        "string",
						"enum":        []string{"agent", "host", "exercise"},
						"description": "Visibility scope: 'agent' (private), 'host' (shared on this host), 'exercise' (all hosts)",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "The note content. Be concise but specific.",
					},
					"memo_type": map[string]any{
						"type":        "string",
						"enum":        []string{"observation", "finding_lead", "correlation", "system_overview"},
						"description": "Type of memo (default: observation)",
						"default":     "observation",
					},
				},
				"required": []string{"scope", "content"},
			},
		},
		{
			Name:        "read_memos",
			Description: "Read previously written notes. Use to recall what you or other agents have observed.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"scope": map[string]any{
						"type":        "string",
						"enum":        []string{"agent", "host", "exercise"},
						"description": "Which scope to read from",
					},
					"since_minutes": map[string]any{
						"type":        "integer",
						"description": "Only return memos from the last N minutes. Omit for all.",
					},
				},
				"required": []string{"scope"},
			},
		},
		{
			Name:        "delegate_investigation",
			Description: "Spawn an Investigator Agent to drill deeply into a specific suspicious file, process, or configuration.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"target": map[string]any{
						"type":        "string",
						"description": "The target to investigate (e.g. /etc/crontab, PID 1234)",
					},
					"context": map[string]any{
						"type":        "string",
						"description": "Why this target is suspicious and what the Investigator should look for",
					},
				},
				"required": []string{"target", "context"},
			},
		},
		{
			Name:        "read_cached_output",
			Description: "Read specific lines from a previously executed tool's truncated output cache.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tool_call_id": map[string]any{
						"type":        "string",
						"description": "The ID of the tool call that resulted in truncated output.",
					},
					"start_line": map[string]any{
						"type":        "integer",
						"description": "0-indexed starting line.",
					},
					"max_lines": map[string]any{
						"type":        "integer",
						"description": "Maximum lines to read.",
						"default":     100,
					},
				},
				"required": []string{"tool_call_id", "start_line"},
			},
		},
	}
}

// GetToolsForRole filters the available tools based on the SubAgent's role
func GetToolsForRole(role string) []Tool {
	allTools := GetAvailableTools()

	switch role {
	case "reporter":
		// Reporter only needs file reading/writing and conclude
		return filterTools(allTools, []string{"read_file", "write_file", "conclude"})
	case "system_overview":
		return filterTools(allTools, []string{
			"execute_trusted", "read_file", "list_dir",
			"get_system_env", "get_network_info", "check_listeners",
			"check_systemd", "write_memo", "conclude", "read_cached_output",
		})
	case "investigator":
		// Investigator does the deep dive, needs full access but not delegation
		return filterTools(allTools, []string{
			"execute_trusted", "read_file", "write_file", "get_system_env",
			"run_linpeas_sh", "run_linpeas_static", "run_pspy", "list_dir",
			"search_files", "get_network_info", "submit_finding", "conclude",
			"write_memo", "read_memos", "query_knowledge_base",
			"check_cron", "check_users", "check_systemd", "check_suid", "check_modules", "check_listeners",
			"read_cached_output",
		})
	default:
		// Phase Agents do broad searches, can delegate investigations
		return filterTools(allTools, []string{
			"execute_trusted", "read_file", "write_file", "get_system_env",
			"run_linpeas_sh", "run_linpeas_static", "run_pspy", "list_dir",
			"search_files", "get_network_info", "delegate_investigation", "conclude",
			"write_memo", "read_memos", "query_knowledge_base",
			"check_cron", "check_users", "check_systemd", "check_suid", "check_modules", "check_listeners",
			"read_cached_output",
		})
	}
}

func filterTools(tools []Tool, allowed []string) []Tool {
	var filtered []Tool
	allowSet := make(map[string]bool)
	for _, a := range allowed {
		allowSet[a] = true
	}

	for _, t := range tools {
		if allowSet[t.Name] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// ParseToolArguments parses JSON arguments for a tool
func ParseToolArguments(args string) (map[string]any, error) {
	var result map[string]any
	if err := json.Unmarshal([]byte(args), &result); err != nil {
		return nil, err
	}
	return result, nil
}

// stringArg extracts a string value from parsed tool arguments.
func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

// stringSliceArg extracts a []string from parsed tool arguments.
func stringSliceArg(args map[string]any, key string) []string {
	raw, ok := args[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
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
