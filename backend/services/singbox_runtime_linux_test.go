//go:build linux

package services

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSingBoxStopTerminatesSpawnedProcessGroup(t *testing.T) {
	configDir := t.TempDir()
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	fakeBinary := filepath.Join(t.TempDir(), "fake-sing-box")
	script := `#!/bin/sh
if [ "$1" = "check" ]; then
  exit 0
fi
sleep 300 &
echo "$!" > "$SBPM_TEST_CHILD_PID_FILE"
wait
`
	if err := os.WriteFile(fakeBinary, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sing-box: %v", err)
	}
	t.Setenv("SINGBOX_BINARY", fakeBinary)
	t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "10ms")
	t.Setenv("SBPM_TEST_CHILD_PID_FILE", childPIDPath)
	t.Setenv("SINGBOX_ENV_ALLOWLIST", "SBPM_TEST_CHILD_PID_FILE")

	service := NewSingBoxService(configDir)
	if err := service.GenerateGlobalConfig(nil); err != nil {
		t.Fatalf("generate config: %v", err)
	}
	if err := service.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })

	deadline := time.Now().Add(time.Second)
	var childPID int
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(childPIDPath)
		if err == nil {
			childPID, err = strconv.Atoi(strings.TrimSpace(string(content)))
			if err == nil && childPID > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID <= 0 {
		t.Fatalf("fake runtime did not report its child PID")
	}
	if err := service.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}

	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("spawned child process %d survived Stop", childPID)
}
