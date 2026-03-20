package agent

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

//go:embed tools/*
var toolsFS embed.FS

//go:embed tools/bin/*
var busyBoxFS embed.FS

type CleanRoom struct {
	WorkDir     string
	ToolPaths   map[string]string
	ToolHashes  map[string]string
}

func NewCleanRoom() (*CleanRoom, error) {
	workDir, err := createWorkDir()
	if err != nil {
		return nil, err
	}

	cr := &CleanRoom{
		WorkDir:    workDir,
		ToolPaths:  make(map[string]string),
		ToolHashes: make(map[string]string),
	}

	if err := cr.setupTools(); err != nil {
		return nil, err
	}

	return cr, nil
}

func createWorkDir() (string, error) {
	// Try /dev/shm first
	if workDir, err := createInShm(); err == nil {
		return workDir, nil
	}

	// Fallback to /tmp
	tmpDir := os.TempDir()
	workDir := filepath.Join(tmpDir, fmt.Sprintf(".jagr_%s", generateRandomID()))
	if err := os.MkdirAll(workDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create work dir in /tmp: %w", err)
	}

	return workDir, nil
}

func createInShm() (string, error) {
	// Check if /dev/shm exists and is writable
	if _, err := os.Stat("/dev/shm"); os.IsNotExist(err) {
		return "", fmt.Errorf("/dev/shm does not exist")
	}

	// Try to create in /dev/shm
	workDir := filepath.Join("/dev/shm", fmt.Sprintf(".jagr_%s", generateRandomID()))
	if err := os.MkdirAll(workDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create work dir in /dev/shm: %w", err)
	}

	// Check if executable
	testPath := filepath.Join(workDir, ".test_exec")
	if err := os.WriteFile(testPath, []byte{}, 0700); err == nil {
		os.Remove(testPath)
		return workDir, nil
	}

	return "", fmt.Errorf("/dev/shm may be noexec")
}

func generateRandomID() string {
	return fmt.Sprintf("%x", time.Now().UnixNano())[:8]
}

// computeFileHash returns the SHA-256 hex digest of a file.
func computeFileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// computeEmbedHash returns the SHA-256 hex digest of an embedded file.
func computeEmbedHash(fs embed.FS, path string) (string, error) {
	data, err := fs.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (cr *CleanRoom) setupTools() error {
	// Extract busybox
	busyBoxPath := filepath.Join(cr.WorkDir, "busybox")
	bbSrc := "tools/bin/busybox"
	bbFS := busyBoxFS
	if err := extractFile(bbFS, bbSrc, busyBoxPath); err != nil {
		// Try to extract from tools directory
		bbSrc = "tools/busybox"
		bbFS = toolsFS
		if err := extractFile(toolsFS, bbSrc, busyBoxPath); err != nil {
			return fmt.Errorf("failed to extract busybox: %w", err)
		}
	}

	if err := os.Chmod(busyBoxPath, 0755); err != nil {
		return fmt.Errorf("failed to chmod busybox: %w", err)
	}
	cr.ToolPaths["busybox"] = busyBoxPath

	// Compute and verify checksum
	expectedHash, _ := computeEmbedHash(bbFS, bbSrc)
	cr.ToolHashes["busybox"] = expectedHash
	if err := cr.verifyTool("busybox"); err != nil {
		return err
	}

	// Extract linpeas
	linpeasPath := filepath.Join(cr.WorkDir, "linpeas.sh")
	if err := extractFile(toolsFS, "tools/linpeas.sh", linpeasPath); err != nil {
		return fmt.Errorf("failed to extract linpeas: %w", err)
	}
	if err := os.Chmod(linpeasPath, 0755); err != nil {
		return fmt.Errorf("failed to chmod linpeas: %w", err)
	}
	cr.ToolPaths["linpeas"] = linpeasPath
	cr.ToolHashes["linpeas"], _ = computeEmbedHash(toolsFS, "tools/linpeas.sh")
	if err := cr.verifyTool("linpeas"); err != nil {
		return err
	}

	// Extract pspy
	pspyPath := filepath.Join(cr.WorkDir, "pspy")
	pspySrc := "tools/pspy64"
	if runtime.GOARCH == "amd64" {
		if err := extractFile(toolsFS, "tools/pspy64", pspyPath); err != nil {
			pspyPath = filepath.Join(cr.WorkDir, "pspy32")
			pspySrc = "tools/pspy"
			if err := extractFile(toolsFS, "tools/pspy", pspyPath); err != nil {
				return fmt.Errorf("failed to extract pspy: %w", err)
			}
		}
	} else {
		pspySrc = "tools/pspy"
		if err := extractFile(toolsFS, "tools/pspy", pspyPath); err != nil {
			return fmt.Errorf("failed to extract pspy: %w", err)
		}
	}
	if err := os.Chmod(pspyPath, 0755); err != nil {
		return fmt.Errorf("failed to chmod pspy: %w", err)
	}
	cr.ToolPaths["pspy"] = pspyPath
	cr.ToolHashes["pspy"], _ = computeEmbedHash(toolsFS, pspySrc)
	if err := cr.verifyTool("pspy"); err != nil {
		return err
	}

	// Create symlink farm using busybox --install
	cmd := exec.Command(busyBoxPath, "--install", "-s", cr.WorkDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create busybox symlinks: %w (output: %s)", err, output)
	}

	return nil
}

// verifyTool checks that an extracted tool's SHA-256 matches the expected hash.
func (cr *CleanRoom) verifyTool(name string) error {
	path := cr.ToolPaths[name]
	expected := cr.ToolHashes[name]
	if path == "" || expected == "" {
		return nil
	}

	actual, err := computeFileHash(path)
	if err != nil {
		return fmt.Errorf("failed to hash %s: %w", name, err)
	}

	if actual != expected {
		return fmt.Errorf("integrity check failed for %s: expected %s, got %s", name, expected, actual)
	}
	return nil
}

func extractFile(f embed.FS, srcPath, dstPath string) error {
	data, err := f.ReadFile(srcPath)
	if err != nil {
		return err
	}

	dstDir := filepath.Dir(dstPath)
	if err := os.MkdirAll(dstDir, 0700); err != nil {
		return err
	}

	return os.WriteFile(dstPath, data, 0755)
}

func (cr *CleanRoom) GetToolPath(name string) string {
	return cr.ToolPaths[name]
}

// DefaultTimeout is the default per-command execution timeout.
const DefaultTimeout = 120 * time.Second

// LongTimeout is for long-running tools like linpeas and pspy.
const LongTimeout = 600 * time.Second

// ExecuteTrusted runs a command in the Clean Room with the default timeout.
func (cr *CleanRoom) ExecuteTrusted(command string, args []string) (string, string, int, error) {
	return cr.ExecuteTrustedWithTimeout(command, args, DefaultTimeout)
}

// ExecuteTrustedLong runs a command with the extended timeout (for linpeas, pspy).
func (cr *CleanRoom) ExecuteTrustedLong(command string, args []string) (string, string, int, error) {
	return cr.ExecuteTrustedWithTimeout(command, args, LongTimeout)
}

// ExecuteTrustedWithTimeout runs a command in the sanitized Clean Room environment
// with the specified timeout.
func (cr *CleanRoom) ExecuteTrustedWithTimeout(command string, args []string, timeout time.Duration) (string, string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, args...)

	// Sanitize environment — whitelist approach
	cmd.Env = []string{
		"PATH=" + cr.WorkDir,
		"HOME=/root",
		"TERM=" + os.Getenv("TERM"),
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", -1, err
	}
	defer stdout.Close()

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", "", -1, err
	}
	defer stderr.Close()

	if err := cmd.Start(); err != nil {
		return "", "", -1, err
	}

	outBytes, _ := io.ReadAll(stdout)
	errBytes, _ := io.ReadAll(stderr)

	err = cmd.Wait()
	exitCode := 0
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return string(outBytes), string(errBytes), -1, fmt.Errorf("command timed out after %s", timeout)
		}
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		}
	}

	return string(outBytes), string(errBytes), exitCode, nil
}

func (cr *CleanRoom) Cleanup() {
	os.RemoveAll(cr.WorkDir)
}
