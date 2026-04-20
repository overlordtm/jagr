package agent

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/overlordtm/jagr/internal/agent/enrichment"
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
	case "check_cron":
		return tb.execEnrichment(tc, enrichment.EnrichCron)
	case "check_users":
		return tb.execEnrichment(tc, enrichment.EnrichUsers)
	case "check_systemd":
		return tb.execEnrichment(tc, enrichment.EnrichSystemd)
	case "check_suid":
		return tb.execEnrichment(tc, enrichment.EnrichSUID)
	case "check_modules":
		return tb.execEnrichment(tc, enrichment.EnrichModules)
	case "check_listeners":
		return tb.execEnrichment(tc, enrichment.EnrichListeners)
	case "check_packages":
		return tb.execEnrichment(tc, enrichment.EnrichPackages)
	default:
		return ToolResult{}, fmt.Errorf("unknown tool: %s", tc.Function.Name)
	}
}

// execEnrichment runs a Go-side enrichment parser and returns its formatted output.
func (tb *ToolBox) execEnrichment(tc ToolCall, fn func(enrichment.Runner) string) (ToolResult, error) {
	output := fn(tb.cleanRoom)
	return ToolResult{
		ToolID:  tc.ID,
		Name:    tc.Function.Name,
		Content: output,
	}, nil
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

	// Always route through "sh -c" so the shell handles argument splitting,
	// pipes, redirections, and other syntax the LLM may produce.
	// Individual args are single-quoted to prevent glob expansion, word
	// splitting, and variable substitution (e.g. /proc/* must not expand).
	fullCmd := command
	if len(cmdArgs) > 0 {
		quoted := make([]string, len(cmdArgs))
		for i, a := range cmdArgs {
			quoted[i] = "'" + strings.ReplaceAll(a, "'", "'\\''") + "'"
		}
		fullCmd = command + " " + strings.Join(quoted, " ")
	}

	stdout, stderr, exitCode, err := tb.cleanRoom.ExecuteTrusted("sh", []string{"-c", fullCmd})
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

	f, err := os.Open(path)
	if err != nil {
		return ToolResult{}, err
	}
	defer f.Close()

	// Detect binary files by reading the first 512 bytes and checking for
	// non-text content. Reading large binaries into LLM context wastes tokens
	// and can derail reasoning; use execute_trusted with `file` and `sha256sum`.
	header := make([]byte, 512)
	n, _ := f.Read(header)
	header = header[:n]
	if isBinaryContent(header) {
		fi, _ := f.Stat()
		size := int64(0)
		if fi != nil {
			size = fi.Size()
		}
		return ToolResult{
			ToolID: tc.ID,
			Name:   tc.Function.Name,
			Content: fmt.Sprintf(
				"[Binary file — content not shown to avoid context pollution]\nPath: %s\nSize: %d bytes\nTo analyze: use execute_trusted with `file %s` and `sha256sum %s`.",
				path, size, path, path,
			),
		}, nil
	}

	// Rewind and read the full file.
	if _, err := f.Seek(0, 0); err != nil {
		return ToolResult{}, err
	}
	data, err := io.ReadAll(f)
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

// isBinaryContent returns true when the byte slice looks like binary data.
// Checks for ELF magic, common binary magic bytes, or high ratio of non-printable bytes.
func isBinaryContent(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	// ELF magic
	if len(b) >= 4 && b[0] == 0x7f && b[1] == 'E' && b[2] == 'L' && b[3] == 'F' {
		return true
	}
	// Count non-printable, non-whitespace bytes
	nonText := 0
	for _, c := range b {
		if c < 0x08 || (c > 0x0d && c < 0x20 && c != 0x1b) || c == 0x7f {
			nonText++
		}
	}
	return nonText*10 > len(b) // >10% non-text bytes
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
	if d, ok := args["duration_seconds"].(float64); ok {
		duration = int(d)
	} else if d, ok := args["duration_seconds"].(int); ok {
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

	stdout, _, _, _ = tb.cleanRoom.ExecuteTrusted("netstat", []string{"-tuln"})
	results = append(results, "\n=== Connections ===")
	results = append(results, strings.Split(stdout, "\n")...)

	content := strings.Join(results, "\n")

	return ToolResult{
		ToolID:  tc.ID,
		Name:    tc.Function.Name,
		Content: content,
	}, nil
}
