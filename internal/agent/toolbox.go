package agent

import (
	"fmt"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ToolBox wraps a CleanRoom with per-agent tool failure tracking and circuit breakers.
// Each AiAgent gets its own ToolBox instance, but they share the same CleanRoom.
type ToolBox struct {
	cleanRoom     *CleanRoom
	failureCounts map[string]int
	maxFailures   int
	logger        *zap.Logger
}

// NewToolBox creates a new ToolBox with its own failure counters.
func NewToolBox(cleanRoom *CleanRoom, maxFailures int, logger *zap.Logger) *ToolBox {
	return &ToolBox{
		cleanRoom:     cleanRoom,
		failureCounts: make(map[string]int),
		maxFailures:   maxFailures,
		logger:        logger,
	}
}

// IncrementFailure increments the failure count for a tool and returns the new count.
func (tb *ToolBox) IncrementFailure(toolName string) int {
	tb.failureCounts[toolName]++
	return tb.failureCounts[toolName]
}

// ResetFailure resets the failure count for a tool back to zero.
func (tb *ToolBox) ResetFailure(toolName string) {
	tb.failureCounts[toolName] = 0
}

// IsCircuitBroken returns true if a tool has exceeded the max failure threshold.
func (tb *ToolBox) IsCircuitBroken(toolName string) bool {
	return tb.failureCounts[toolName] >= tb.maxFailures
}

// ExecuteTool dispatches a tool call to the appropriate handler.
// Special tools (submit_finding, conclude, delegate_investigation) are NOT handled here
// and must be handled by the AiAgent.
func (tb *ToolBox) ExecuteTool(tc ToolCall) (ToolResult, error) {
	args, err := ParseToolArguments(tc.Function.Arguments)
	if err != nil {
		return ToolResult{}, fmt.Errorf("failed to parse args: %w", err)
	}

	tb.logger.Debug("Executing tool", zap.String("tool", tc.Function.Name), zap.String("args", tc.Function.Arguments))

	switch tc.Function.Name {
	case "execute_trusted":
		return tb.execExecuteTrusted(tc, args)
	case "read_file":
		return tb.execReadFile(tc, args)
	case "write_file":
		return tb.execWriteFile(tc, args)
	case "get_system_env":
		return tb.execGetSystemEnv(tc, args)
	case "run_linpeas_sh":
		return tb.execLinpeasSh(tc, args)
	case "run_linpeas_static":
		return tb.execLinpeasStatic(tc, args)
	case "run_pspy":
		return tb.execPspy(tc, args)
	case "list_dir":
		return tb.execListDir(tc, args)
	case "search_files":
		return tb.execSearchFiles(tc, args)
	case "get_network_info":
		return tb.execGetNetworkInfo(tc, args)
	default:
		return ToolResult{}, fmt.Errorf("unknown tool: %s", tc.Function.Name)
	}
}

func (tb *ToolBox) execExecuteTrusted(tc ToolCall, args map[string]any) (ToolResult, error) {
	command, _ := args["command"].(string)
	if command == "" {
		return ToolResult{}, fmt.Errorf("command is required")
	}

	var cmdArgs []string
	if rawArgs, ok := args["args"].([]any); ok {
		for _, arg := range rawArgs {
			if s, ok := arg.(string); ok {
				cmdArgs = append(cmdArgs, s)
			}
		}
	}

	// Handle LLM sending full command string (e.g. "cat /etc/passwd") as the command name
	if len(cmdArgs) == 0 && strings.Contains(command, " ") {
		parts := strings.Fields(command)
		command = parts[0]
		cmdArgs = parts[1:]
	}

	stdout, stderr, exitCode, err := tb.cleanRoom.ExecuteTrusted(command, cmdArgs)
	return ToolResult{
		ToolID:   tc.ID,
		Name:     tc.Function.Name,
		Content:  stdout + "\n" + stderr,
		ExitCode: exitCode,
	}, err
}

func (tb *ToolBox) execReadFile(tc ToolCall, args map[string]any) (ToolResult, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return ToolResult{}, fmt.Errorf("path is required")
	}

	maxLines := 1000
	if ml, ok := args["max_lines"].(int); ok {
		maxLines = ml
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return ToolResult{}, err
	}

	content := FormatToolOutput(string(data), "read_file", maxLines)
	return ToolResult{
		ToolID:  tc.ID,
		Name:    tc.Function.Name,
		Content: content,
	}, nil
}

func (tb *ToolBox) execWriteFile(tc ToolCall, args map[string]any) (ToolResult, error) {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)

	if path == "" {
		return ToolResult{}, fmt.Errorf("path is required")
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return ToolResult{}, err
	}

	return ToolResult{
		ToolID:  tc.ID,
		Name:    tc.Function.Name,
		Content: fmt.Sprintf("Written %d bytes to %s", len(content), path),
	}, nil
}

func (tb *ToolBox) execGetSystemEnv(tc ToolCall, args map[string]any) (ToolResult, error) {
	pid := 1
	if p, ok := args["pid"].(int); ok {
		pid = p
	}

	environPath := fmt.Sprintf("/proc/%d/environ", pid)
	data, err := os.ReadFile(environPath)
	if err != nil {
		return ToolResult{}, err
	}

	envVars := strings.Split(string(data), "\x00")
	var result []string
	for _, v := range envVars {
		if v != "" {
			result = append(result, v)
		}
	}

	content := strings.Join(result, "\n")
	if len(content) > 5000 {
		content = content[:5000] + "\n... [truncated]"
	}

	return ToolResult{
		ToolID:  tc.ID,
		Name:    tc.Function.Name,
		Content: content,
	}, nil
}

func (tb *ToolBox) execLinpeasSh(tc ToolCall, args map[string]any) (ToolResult, error) {
	flags := "-a"
	if f, ok := args["flags"].(string); ok {
		flags = f
	}

	scriptPath := tb.cleanRoom.GetToolPath("linpeas")
	if scriptPath == "" {
		return ToolResult{}, fmt.Errorf("linpeas script not found")
	}

	stdout, stderr, exitCode, err := tb.cleanRoom.ExecuteTrustedLong(scriptPath, []string{flags})
	content := stdout + "\n" + stderr
	content = FilterLinPEASOutput(content)

	return ToolResult{
		ToolID:   tc.ID,
		Name:     tc.Function.Name,
		Content:  content,
		ExitCode: exitCode,
	}, err
}

func (tb *ToolBox) execLinpeasStatic(tc ToolCall, args map[string]any) (ToolResult, error) {
	flags := "-a"
	if f, ok := args["flags"].(string); ok {
		flags = f
	}

	staticPath := tb.cleanRoom.GetToolPath("linpeas_static")
	if staticPath == "" {
		return tb.execLinpeasSh(tc, args)
	}

	stdout, stderr, exitCode, err := tb.cleanRoom.ExecuteTrustedLong(staticPath, []string{flags})
	content := stdout + "\n" + stderr
	content = FilterLinPEASOutput(content)

	return ToolResult{
		ToolID:   tc.ID,
		Name:     tc.Function.Name,
		Content:  content,
		ExitCode: exitCode,
	}, err
}

func (tb *ToolBox) execPspy(tc ToolCall, args map[string]any) (ToolResult, error) {
	duration := 300
	if d, ok := args["duration_seconds"].(int); ok {
		duration = d
	}

	pspyPath := tb.cleanRoom.GetToolPath("pspy")
	if pspyPath == "" {
		return ToolResult{}, fmt.Errorf("pspy not found")
	}

	stdout, stderr, exitCode, err := tb.cleanRoom.ExecuteTrustedWithTimeout(pspyPath, nil, time.Duration(duration)*time.Second)
	content := stdout + "\n" + stderr
	content = DeduplicatepspyEvents(content)

	return ToolResult{
		ToolID:   tc.ID,
		Name:     tc.Function.Name,
		Content:  content,
		ExitCode: exitCode,
	}, err
}

func (tb *ToolBox) execListDir(tc ToolCall, args map[string]any) (ToolResult, error) {
	path, _ := args["path"].(string)
	if path == "" {
		path = "/"
	}

	recursive := false
	if r, ok := args["recursive"].(bool); ok {
		recursive = r
	}

	argsSlice := []string{"-la", path}
	if recursive {
		argsSlice = []string{"-laR", path}
	}

	stdout, stderr, exitCode, err := tb.cleanRoom.ExecuteTrusted("ls", argsSlice)
	content := stdout + "\n" + stderr

	return ToolResult{
		ToolID:   tc.ID,
		Name:     tc.Function.Name,
		Content:  content,
		ExitCode: exitCode,
	}, err
}

func (tb *ToolBox) execSearchFiles(tc ToolCall, args map[string]any) (ToolResult, error) {
	pattern, _ := args["pattern"].(string)
	path, _ := args["path"].(string)
	maxResults := 100
	if mr, ok := args["max_results"].(int); ok {
		maxResults = mr
	}

	if pattern == "" {
		return ToolResult{}, fmt.Errorf("pattern is required")
	}

	if path == "" {
		path = "/"
	}

	argsSlice := []string{"-r", "-n", "-m", fmt.Sprintf("%d", maxResults), pattern, path}

	stdout, stderr, exitCode, err := tb.cleanRoom.ExecuteTrusted("grep", argsSlice)
	content := stdout + "\n" + stderr

	return ToolResult{
		ToolID:   tc.ID,
		Name:     tc.Function.Name,
		Content:  content,
		ExitCode: exitCode,
	}, err
}

func (tb *ToolBox) execGetNetworkInfo(tc ToolCall, args map[string]any) (ToolResult, error) {
	var results []string

	stdout, _, _, _ := tb.cleanRoom.ExecuteTrusted("ip", []string{"a"})
	results = append(results, "=== Interfaces ===")
	results = append(results, strings.Split(stdout, "\n")...)

	stdout, _, _, _ = tb.cleanRoom.ExecuteTrusted("ip", []string{"r"})
	results = append(results, "\n=== Routes ===")
	results = append(results, strings.Split(stdout, "\n")...)

	stdout, _, _, _ = tb.cleanRoom.ExecuteTrusted("ss", []string{"-tuln"})
	results = append(results, "\n=== Connections ===")
	results = append(results, strings.Split(stdout, "\n")...)

	content := strings.Join(results, "\n")

	return ToolResult{
		ToolID:  tc.ID,
		Name:    tc.Function.Name,
		Content: content,
	}, nil
}
