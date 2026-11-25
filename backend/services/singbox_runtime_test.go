package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeRuntimeConfigFixture(t *testing.T, service *SingBoxService) {
	t.Helper()
	data, err := service.BuildGlobalConfigContext(context.Background(), nil)
	if err != nil {
		t.Fatalf("build fixture config: %v", err)
	}
	if err := service.writeConfigFile(data); err != nil {
		t.Fatalf("write fixture config: %v", err)
	}
}

func TestColdPreservedPolicyRequiresScheduler(t *testing.T) {
	service := NewSingBoxService(t.TempDir())
	data := []byte(`{"inbounds":[{"type":"socks","tag":"internal-policy-dispatch-in","listen":"127.0.0.1","listen_port":32500}]}`)
	if err := service.saveLastGoodBytes(data); err != nil {
		t.Fatal(err)
	}
	err := service.StartPreservedConfig()
	status := service.RuntimeStatus()
	if err == nil || !strings.Contains(err.Error(), "matching scheduler") || status.Running || !status.Degraded || service.features.dispatcher != nil {
		t.Fatalf("cold policy restore: %+v, %v", status, err)
	}
	last, err := os.ReadFile(service.lastGoodConfigPath())
	if err != nil || string(last) != string(data) {
		t.Fatal("recovery changed the saved policy configuration")
	}
}

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
	t.Setenv("SBPM_SINGBOX_LOG_OUTPUT", "file")
	t.Setenv("SBPM_SINGBOX_RECOVERY_BASE_DELAY", "10ms")
	t.Setenv("SBPM_SINGBOX_RECOVERY_MAX_DELAY", "20ms")
	t.Setenv("SBPM_SINGBOX_RECOVERY_STABLE_WINDOW", "1s")
	t.Setenv("SBPM_TEST_STABLE_MARKER", stableMarker)
	t.Setenv("SINGBOX_ENV_ALLOWLIST", "SBPM_TEST_STABLE_MARKER")

	service := NewSingBoxService(configDir)
	writeRuntimeConfigFixture(t, service)
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
	if err := service.Start(); err != nil {
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
	t.Setenv("SINGBOX_ENV_ALLOWLIST", "SBPM_TEST_ACTIVE_DIR,SBPM_TEST_OVERLAP_FILE")

	service := NewSingBoxService(configDir)
	writeRuntimeConfigFixture(t, service)
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
	if service.recoveryCancel != nil {
		t.Fatalf("completed recovery state was not cleared")
	}
}

func TestStartPreservedConfigTriesEachDistinctSnapshotOnce(t *testing.T) {
	const bad = `{"fixture":"bad"}`
	const otherBad = `{"fixture":"other-bad"}`
	const good = `{"fixture":"good"}`
	for _, test := range []struct {
		name, current, lastGood, attempts string
		wantRunning                       bool
	}{
		{name: "identical failed snapshots", current: bad, lastGood: bad, attempts: bad + "\n"},
		{name: "different failed snapshots", current: bad, lastGood: otherBad, attempts: bad + "\n" + otherBad + "\n"},
		{name: "fallback after failed current", current: bad, lastGood: good, attempts: bad + "\n" + good + "\n", wantRunning: true},
		{name: "missing current", lastGood: good, attempts: good + "\n", wantRunning: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixtureDir := t.TempDir()
			binary := filepath.Join(fixtureDir, "fake-sing-box")
			attemptLog := filepath.Join(fixtureDir, "startup-attempts")
			script := `#!/bin/sh
config=$(cat "$3")
printf '%s\n' "$config" >> "$SBPM_TEST_STARTUP_ATTEMPTS"
if [ "$config" = '{"fixture":"good"}' ]; then
  exec sleep 300
fi
exit 23
`
			if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SBPM_TEST_STARTUP_ATTEMPTS", attemptLog)
			t.Setenv("SINGBOX_ENV_ALLOWLIST", "SBPM_TEST_STARTUP_ATTEMPTS")
			t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "250ms")
			service := NewSingBoxService(t.TempDir(), WithBinaryPath(binary))
			t.Cleanup(func() { _ = service.Stop() })
			for path, data := range map[string]string{service.ConfigPath(): test.current, service.lastGoodConfigPath(): test.lastGood} {
				if data != "" {
					if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := service.StartPreservedConfig()
			status := service.RuntimeStatus()
			if (err == nil) != test.wantRunning || status.Running != test.wantRunning || status.RecoveryAttempts != 0 {
				t.Fatalf("unexpected startup result: status=%+v err=%v", status, err)
			}
			attempts, readErr := os.ReadFile(attemptLog)
			if readErr != nil || string(attempts) != test.attempts {
				t.Fatalf("unexpected startup attempts: got=%q want=%q err=%v", attempts, test.attempts, readErr)
			}
			wantCurrent := test.current
			if test.wantRunning {
				wantCurrent = good
				if status.ActiveConfigHash != runtimeConfigHash([]byte(good)) {
					t.Fatal("running fallback does not match the active snapshot")
				}
			}
			for path, want := range map[string]string{service.ConfigPath(): wantCurrent, service.lastGoodConfigPath(): test.lastGood} {
				data, readErr := os.ReadFile(path)
				if readErr != nil || string(data) != want {
					t.Fatalf("preserved snapshot changed: path=%s got=%q want=%q err=%v", path, data, want, readErr)
				}
			}
		})
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
	t.Setenv("SINGBOX_ENV_ALLOWLIST", "SBPM_TEST_FIRST_BAD_RUN")

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
