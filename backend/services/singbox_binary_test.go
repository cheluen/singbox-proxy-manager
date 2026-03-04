package services

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveSingBoxBinaryFromEnvCommandName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this test uses unix executable permissions")
	}

	tmpDir := t.TempDir()
	executable := filepath.Join(tmpDir, "fake-sing-box")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	t.Setenv("PATH", tmpDir)
	t.Setenv("SINGBOX_BINARY", "fake-sing-box")

	svc := NewSingBoxService(t.TempDir())
	got, err := svc.resolveSingBoxBinary()
	if err != nil {
		t.Fatalf("resolveSingBoxBinary should resolve command name from PATH: %v", err)
	}
	if got != executable {
		t.Fatalf("unexpected resolved path: got %q want %q", got, executable)
	}
}

func TestResolveSingBoxBinaryFromEnvExplicitPath(t *testing.T) {
	tmpDir := t.TempDir()
	executable := filepath.Join(tmpDir, "sing-box")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	t.Setenv("SINGBOX_BINARY", executable)
	t.Setenv("PATH", "")

	svc := NewSingBoxService(t.TempDir())
	got, err := svc.resolveSingBoxBinary()
	if err != nil {
		t.Fatalf("resolveSingBoxBinary should accept explicit file path: %v", err)
	}
	if got != executable {
		t.Fatalf("unexpected resolved path: got %q want %q", got, executable)
	}
}

func TestResolveSingBoxBinaryRejectsNonExecutableExplicitPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows executable mode differs")
	}

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "sing-box")
	if err := os.WriteFile(filePath, []byte("placeholder"), 0o644); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	t.Setenv("SINGBOX_BINARY", filePath)
	t.Setenv("PATH", "")

	svc := NewSingBoxService(t.TempDir())
	if _, err := svc.resolveSingBoxBinary(); err == nil {
		t.Fatalf("expected non-executable explicit file to be rejected")
	}
}
