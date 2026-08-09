package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSingBoxWatcherReportsUnexpectedExitAndSecuresFiles(t *testing.T) {
	configDir := t.TempDir()
	fakeBinary := filepath.Join(t.TempDir(), "fake-sing-box")
	script := `#!/bin/sh
if [ "$1" = "check" ]; then
  exit 0
fi
sleep 0.06
exit 17
`
	if err := os.WriteFile(fakeBinary, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sing-box: %v", err)
	}
	t.Setenv("SINGBOX_BINARY", fakeBinary)
	t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "10ms")

	service := NewSingBoxService(configDir)
	if err := service.GenerateGlobalConfig(nil); err != nil {
		t.Fatalf("generate config: %v", err)
	}
	if err := service.Start(); err != nil {
		t.Fatalf("start should pass the initial stability window: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })

	deadline := time.Now().Add(2 * time.Second)
	var status SingBoxRuntimeStatus
	for time.Now().Before(deadline) {
		status = service.RuntimeStatus()
		if status.Degraded && !status.Running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !status.Degraded || status.Running || status.State != "degraded" {
		t.Fatalf("unexpected-exit watcher did not expose degraded state: %+v", status)
	}
	if status.Message == "" || status.LastExitAt == 0 {
		t.Fatalf("degraded status should include exit diagnostics: %+v", status)
	}

	for _, path := range []string{
		filepath.Join(configDir, "config.json"),
		filepath.Join(configDir, "config.json.last-good"),
		filepath.Join(configDir, "singbox.log"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat sensitive file %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("sensitive file %s mode=%#o want=0600", path, got)
		}
	}
}
