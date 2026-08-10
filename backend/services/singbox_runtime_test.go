package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSingBoxWatcherReportsUnexpectedExitAndSecuresFiles(t *testing.T) {
	configDir := t.TempDir()
	stableMarker := filepath.Join(t.TempDir(), "stable")
	fakeBinary := filepath.Join(t.TempDir(), "fake-sing-box")
	script := `#!/bin/sh
if [ "$1" = "check" ]; then
  exit 0
fi
if [ -f "$SBPM_TEST_STABLE_MARKER" ]; then
  exec sleep 300
fi
sleep 0.06
exit 17
`
	if err := os.WriteFile(fakeBinary, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sing-box: %v", err)
	}
	t.Setenv("SINGBOX_BINARY", fakeBinary)
	t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "10ms")
	t.Setenv("SBPM_SINGBOX_RECOVERY_MAX_ATTEMPTS", "3")
	t.Setenv("SBPM_SINGBOX_RECOVERY_BASE_DELAY", "10ms")
	t.Setenv("SBPM_SINGBOX_RECOVERY_MAX_DELAY", "20ms")
	t.Setenv("SBPM_SINGBOX_RECOVERY_STABLE_WINDOW", "1s")
	t.Setenv("SBPM_TEST_STABLE_MARKER", stableMarker)

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
		if status.Degraded && !status.Running && strings.Contains(status.Message, "recovery exhausted") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !status.Degraded || status.Running || status.State != "degraded" || status.RecoveryAttempts != 3 {
		t.Fatalf("unexpected-exit recovery did not stop at its retry limit: %+v", status)
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

	if err := os.WriteFile(stableMarker, []byte("ready"), 0o600); err != nil {
		t.Fatalf("write stable marker: %v", err)
	}
	if err := service.Restart(); err != nil {
		t.Fatalf("manual restart after recovery exhaustion: %v", err)
	}
	status = service.RuntimeStatus()
	if !status.Running || status.Degraded || status.RecoveryAttempts != 0 {
		t.Fatalf("manual restart did not reset the recovery circuit: %+v", status)
	}
}

func TestSingBoxRecoveryNeverOverlapsProcesses(t *testing.T) {
	configDir := t.TempDir()
	activeDir := filepath.Join(t.TempDir(), "active")
	overlapPath := filepath.Join(t.TempDir(), "overlap")
	fakeBinary := filepath.Join(t.TempDir(), "fake-sing-box")
	script := `#!/bin/sh
if [ "$1" = "check" ]; then
  exit 0
fi
if ! mkdir "$SBPM_TEST_ACTIVE_DIR" 2>/dev/null; then
  printf 'overlap\n' > "$SBPM_TEST_OVERLAP_FILE"
  exit 99
fi
trap 'rmdir "$SBPM_TEST_ACTIVE_DIR" 2>/dev/null || true' EXIT
sleep 0.04
exit 17
`
	if err := os.WriteFile(fakeBinary, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sing-box: %v", err)
	}
	t.Setenv("SINGBOX_BINARY", fakeBinary)
	t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "10ms")
	t.Setenv("SBPM_SINGBOX_RECOVERY_MAX_ATTEMPTS", "4")
	t.Setenv("SBPM_SINGBOX_RECOVERY_BASE_DELAY", "10ms")
	t.Setenv("SBPM_SINGBOX_RECOVERY_MAX_DELAY", "20ms")
	t.Setenv("SBPM_SINGBOX_RECOVERY_STABLE_WINDOW", "1s")
	t.Setenv("SBPM_TEST_ACTIVE_DIR", activeDir)
	t.Setenv("SBPM_TEST_OVERLAP_FILE", overlapPath)

	service := NewSingBoxService(configDir)
	if err := service.GenerateGlobalConfig(nil); err != nil {
		t.Fatalf("generate config: %v", err)
	}
	if err := service.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := service.RuntimeStatus()
		if status.RecoveryAttempts == 4 && strings.Contains(status.Message, "recovery exhausted") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	status := service.RuntimeStatus()
	if status.RecoveryAttempts != 4 || !strings.Contains(status.Message, "recovery exhausted") {
		t.Fatalf("recovery did not reach the configured circuit breaker: %+v", status)
	}
	if _, err := os.Stat(overlapPath); err == nil {
		t.Fatalf("automatic recovery launched overlapping sing-box processes")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat overlap marker: %v", err)
	}
}

func TestRecoveryStableWindowUsesPreciseProcessStartTime(t *testing.T) {
	t.Setenv("SBPM_SINGBOX_RECOVERY_STABLE_WINDOW", "1s")
	startedAt := time.Unix(100, 990*time.Millisecond.Nanoseconds())
	if recoveryStableWindowElapsed(startedAt, startedAt.Add(40*time.Millisecond)) {
		t.Fatalf("short-lived process crossing a Unix-second boundary reset recovery attempts")
	}
	if !recoveryStableWindowElapsed(startedAt, startedAt.Add(time.Second)) {
		t.Fatalf("process reaching the stable window did not reset recovery attempts")
	}
}

func TestRecoveryLoopReschedulesExitObservedDuringCleanup(t *testing.T) {
	t.Setenv("SBPM_SINGBOX_RECOVERY_MAX_ATTEMPTS", "3")
	service := NewSingBoxService(t.TempDir())
	service.mu.Lock()
	service.desiredRunning = true
	service.recoveryRunning = true
	service.recoveryEpoch = 7
	service.recoveryAttempts = 1
	service.process = nil
	service.recoveryCancel = make(chan struct{})
	service.mu.Unlock()

	if !service.finishRecoveryLoop(7) {
		t.Fatalf("process exit observed during recovery cleanup was not rescheduled")
	}
	service.mu.RLock()
	defer service.mu.RUnlock()
	if service.recoveryRunning || service.recoveryCancel != nil {
		t.Fatalf("completed recovery state was not cleared")
	}
}

func TestRecoveryFallbackRemainsVisiblyDegradedOnConfigMismatch(t *testing.T) {
	configDir := t.TempDir()
	firstBadRunMarker := filepath.Join(t.TempDir(), "first-bad-run")
	fakeBinary := filepath.Join(t.TempDir(), "fake-sing-box")
	script := `#!/bin/sh
if [ "$1" = "check" ]; then
  exit 0
fi
if grep -q '"generation":"bad"' "$3"; then
  if [ ! -f "$SBPM_TEST_FIRST_BAD_RUN" ]; then
    : > "$SBPM_TEST_FIRST_BAD_RUN"
    sleep 0.06
  fi
  exit 17
fi
exec sleep 300
`
	if err := os.WriteFile(fakeBinary, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sing-box: %v", err)
	}
	t.Setenv("SINGBOX_BINARY", fakeBinary)
	t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "10ms")
	t.Setenv("SBPM_SINGBOX_RECOVERY_BASE_DELAY", "10ms")
	t.Setenv("SBPM_SINGBOX_RECOVERY_MAX_DELAY", "10ms")
	t.Setenv("SBPM_TEST_FIRST_BAD_RUN", firstBadRunMarker)

	badConfig := []byte(`{"generation":"bad"}`)
	goodConfig := []byte(`{"generation":"good"}`)
	service := NewSingBoxService(configDir)
	if err := service.writeConfigFile(badConfig); err != nil {
		t.Fatalf("write bad current config: %v", err)
	}
	if err := service.saveLastGoodBytes(goodConfig); err != nil {
		t.Fatalf("write good last-good config: %v", err)
	}
	service.mu.Lock()
	service.desiredRunning = true
	service.runtimeStatus.DesiredConfigHash = runtimeConfigHash(badConfig)
	service.mu.Unlock()
	if err := service.startProcessLocked(runtimeConfigHash(badConfig)); err != nil {
		t.Fatalf("start initial short-lived process: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })

	deadline := time.Now().Add(2 * time.Second)
	var status SingBoxRuntimeStatus
	for time.Now().Before(deadline) {
		status = service.RuntimeStatus()
		if status.Running && status.Degraded && status.ActiveConfigHash == runtimeConfigHash(goodConfig) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !status.Running || !status.Degraded || status.State != "degraded" {
		t.Fatalf("fallback runtime mismatch was hidden: %+v", status)
	}
	if status.DesiredConfigHash != runtimeConfigHash(badConfig) ||
		status.ActiveConfigHash != runtimeConfigHash(goodConfig) ||
		!strings.Contains(status.Message, "reapply") {
		t.Fatalf("fallback status lost config mismatch diagnostics: %+v", status)
	}
}
