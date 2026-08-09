package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"sb-proxy/backend/services"

	_ "modernc.org/sqlite"
)

func TestEnsureSecureConfigDirRepairsExistingPermissions(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")
	if err := os.Mkdir(configDir, 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := ensureSecureConfigDir(configDir); err != nil {
		t.Fatalf("secure config dir: %v", err)
	}
	info, err := os.Stat(configDir)
	if err != nil {
		t.Fatalf("stat config dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("config directory mode=%#o want=0700", got)
	}
}

func TestStartupDatabaseFailurePreservesExistingConfigAndReportsDegraded(t *testing.T) {
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.json")
	originalConfig := []byte(`{"preserved":true}`)
	if err := os.WriteFile(configPath, originalConfig, 0o644); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	fakeBinary := filepath.Join(t.TempDir(), "fake-sing-box")
	script := `#!/bin/sh
if [ "$1" = "check" ]; then
  exit 0
fi
exec sleep 300
`
	if err := os.WriteFile(fakeBinary, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sing-box: %v", err)
	}
	t.Setenv("SINGBOX_BINARY", fakeBinary)
	t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "10ms")

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	service := services.NewSingBoxService(configDir)
	t.Cleanup(func() { _ = service.Stop() })
	startSingBoxFromDatabase(db, service, configDir)

	currentConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read preserved config: %v", err)
	}
	if string(currentConfig) != string(originalConfig) {
		t.Fatalf("database failure overwrote existing config: got=%q want=%q", currentConfig, originalConfig)
	}
	status := service.RuntimeStatus()
	if !status.Running || !status.Degraded || status.State != "degraded" {
		t.Fatalf("preserved runtime should run while exposing degraded database state: %+v", status)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("preserved sensitive config mode=%#o want=0600", got)
	}
}
