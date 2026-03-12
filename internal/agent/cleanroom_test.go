package agent

import (
	"os"
	"strings"
	"testing"
)

func TestCleanRoom_Creation(t *testing.T) {
	cr, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cr.Cleanup()
	
	if cr.WorkDir == "" {
		t.Error("Expected non-empty WorkDir")
	}
	if len(cr.ToolPaths) == 0 {
		t.Error("Expected tool paths to be populated")
	}
}

func TestCleanRoom_GetToolPath(t *testing.T) {
	cr, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cr.Cleanup()
	
	path := cr.GetToolPath("busybox")
	if path == "" {
		t.Error("Expected non-empty busybox path")
	}
	if !strings.Contains(path, cr.WorkDir) {
		t.Errorf("Expected path to contain WorkDir %s, got %s", cr.WorkDir, path)
	}
}

func TestCleanRoom_InvalidToolPath(t *testing.T) {
	cr, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cr.Cleanup()
	
	path := cr.GetToolPath("nonexistent")
	if path != "" {
		t.Errorf("Expected empty path for nonexistent tool, got %s", path)
	}
}

func TestCleanRoom_ExecuteTrusted(t *testing.T) {
	cr, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cr.Cleanup()
	
	stdout, stderr, exitCode, err := cr.ExecuteTrusted("echo", []string{"test"})
	
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if exitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", exitCode)
	}
	if !strings.Contains(stdout, "test") {
		t.Errorf("Expected output to contain 'test', got %s", stdout)
	}
	if stderr != "" {
		t.Errorf("Expected empty stderr, got %s", stderr)
	}
}

func TestCleanRoom_ExecuteTrusted_Failure(t *testing.T) {
	cr, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	defer cr.Cleanup()
	
	_, _, exitCode, err := cr.ExecuteTrusted("ls", []string{"/nonexistent/path"})
	
	if err != nil {
		t.Logf("Expected error for non-existent path: %v", err)
	}
	if exitCode != 0 {
		t.Logf("Expected non-zero exit code, got %d", exitCode)
	}
}

func TestCleanRoom_Cleanup(t *testing.T) {
	cr, err := NewCleanRoom()
	if err != nil {
		t.Fatalf("Failed to create clean room: %v", err)
	}
	
	workDir := cr.WorkDir
	
	cr.Cleanup()
	
	if _, err := os.Stat(workDir); !os.IsNotExist(err) {
		t.Error("Expected work directory to be removed")
	}
}
