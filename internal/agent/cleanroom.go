package agent

import (
	"embed"
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

func (cr *CleanRoom) setupTools() error {
	// Extract busybox
	busyBoxPath := filepath.Join(cr.WorkDir, "busybox")
	if err := extractFile(busyBoxFS, "tools/bin/busybox", busyBoxPath); err != nil {
		// Try to extract from tools directory
		if err := extractFile(toolsFS, "tools/busybox", busyBoxPath); err != nil {
			return fmt.Errorf("failed to extract busybox: %w", err)
		}
	}

	if err := os.Chmod(busyBoxPath, 0755); err != nil {
		return fmt.Errorf("failed to chmod busybox: %w", err)
	}
	cr.ToolPaths["busybox"] = busyBoxPath
	cr.ToolHashes["busybox"] = "placeholder-sha256" // Will be replaced with actual hash

	// Extract linpeas
	linpeasPath := filepath.Join(cr.WorkDir, "linpeas.sh")
	if err := extractFile(toolsFS, "tools/linpeas.sh", linpeasPath); err != nil {
		return fmt.Errorf("failed to extract linpeas: %w", err)
	}
	if err := os.Chmod(linpeasPath, 0755); err != nil {
		return fmt.Errorf("failed to chmod linpeas: %w", err)
	}
	cr.ToolPaths["linpeas"] = linpeasPath

	// Extract pspy
	pspyPath := filepath.Join(cr.WorkDir, "pspy")
	if runtime.GOARCH == "amd64" {
		if err := extractFile(toolsFS, "tools/pspy64", pspyPath); err != nil {
			// Try pspy32
			pspyPath = filepath.Join(cr.WorkDir, "pspy32")
			if err := extractFile(toolsFS, "tools/pspy32", pspyPath); err != nil {
				return fmt.Errorf("failed to extract pspy: %w", err)
			}
		}
	} else {
		pspyPath = filepath.Join(cr.WorkDir, "pspy")
		if err := extractFile(toolsFS, "tools/pspy", pspyPath); err != nil {
			return fmt.Errorf("failed to extract pspy: %w", err)
		}
	}
	if err := os.Chmod(pspyPath, 0755); err != nil {
		return fmt.Errorf("failed to chmod pspy: %w", err)
	}
	cr.ToolPaths["pspy"] = pspyPath

	// Create symlink farm using busybox --install
	cmd := exec.Command(busyBoxPath, "--install", "-s", cr.WorkDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create busybox symlinks: %w (output: %s)", err, output)
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

func (cr *CleanRoom) ExecuteTrusted(command string, args []string) (string, string, int, error) {
	cmd := exec.Command(command, args...)

	// Sanitize environment
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
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		}
	}

	return string(outBytes), string(errBytes), exitCode, nil
}

func (cr *CleanRoom) Cleanup() {
	os.RemoveAll(cr.WorkDir)
}
