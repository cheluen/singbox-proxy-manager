package api

import (
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	appdb "sb-proxy/backend/database"
	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
)

func TestBatchImportRemoteDatabaseIntegration(t *testing.T) {
	db := openBatchImportIntegrationDatabase(t)
	if db == nil {
		t.Skip("remote database integration environment is not set")
	}
	resetBatchImportIntegrationTables(t, db)
	t.Cleanup(func() {
		resetBatchImportIntegrationTables(t, db)
		_ = db.Close()
	})
	if err := models.InitDB(db); err != nil {
		t.Fatalf("initialize remote database: %v", err)
	}

	script := `#!/bin/sh
if [ "$1" = "check" ]; then
  if grep -q "unsupported-flow" "$3"; then
    echo "FATAL[0000] decode config: unsupported flow" >&2
    exit 1
  fi
  exit 0
fi
sleep 300
`
	fakeBinary := filepath.Join(t.TempDir(), "fake-sing-box")
	if err := os.WriteFile(fakeBinary, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sing-box: %v", err)
	}
	t.Setenv("SINGBOX_BINARY", fakeBinary)
	t.Setenv("SBPM_SKIP_PORT_AVAILABILITY_CHECK", "1")
	service := services.NewSingBoxService(t.TempDir())
	t.Cleanup(func() { _ = service.Stop() })
	handler := NewHandler(db, service)

	payload := map[string]interface{}{
		"content": strings.Join([]string{
			"socks5://user:pass@127.0.0.1:1080#remote-valid-socks",
			"vless://00000000-0000-0000-0000-000000000000@127.0.0.1:443?security=tls&flow=unsupported-flow&detour=remote-valid-socks#remote-bad-vless",
		}, "\n"),
		"enabled": true,
	}
	recorder := postJSON(t, handler.BatchImportNodes, http.MethodPost, "/api/nodes/batch-import", payload, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("remote batch import failed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"success":1`) || !strings.Contains(recorder.Body.String(), `"failed":1`) {
		t.Fatalf("remote batch isolation did not report partial success: %s", recorder.Body.String())
	}

	var count int
	var proxyType, name string
	if err := db.QueryRow("SELECT COUNT(*), MIN(type), MIN(name) FROM proxy_nodes").Scan(&count, &proxyType, &name); err != nil {
		t.Fatalf("query remote imported nodes: %v", err)
	}
	if count != 1 || proxyType != "socks5" || name != "remote-valid-socks" {
		t.Fatalf("remote database retained wrong nodes: count=%d type=%q name=%q", count, proxyType, name)
	}
}

func TestRemoteDatabaseRepeatedNodeSave(t *testing.T) {
	db := openBatchImportIntegrationDatabase(t)
	if db == nil {
		t.Skip("remote database integration environment is not set")
	}
	resetBatchImportIntegrationTables(t, db)
	t.Cleanup(func() {
		resetBatchImportIntegrationTables(t, db)
		_ = db.Close()
	})
	if err := models.InitDB(db); err != nil {
		t.Fatalf("initialize remote database: %v", err)
	}

	fakeBinary := filepath.Join(t.TempDir(), "fake-sing-box")
	if err := os.WriteFile(fakeBinary, []byte("#!/bin/sh\nif [ \"$1\" = \"check\" ]; then exit 0; fi\nexec sleep 300\n"), 0o755); err != nil {
		t.Fatalf("write fake sing-box: %v", err)
	}
	t.Setenv("SINGBOX_BINARY", fakeBinary)
	t.Setenv("SBPM_SKIP_PORT_AVAILABILITY_CHECK", "1")
	t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "10ms")
	service := services.NewSingBoxService(t.TempDir())
	t.Cleanup(func() { _ = service.Stop() })
	handler := NewHandler(db, service)

	if _, err := db.Exec(`
		INSERT INTO proxy_nodes (
			name, remark, type, config, inbound_port, username, password,
			tcp_reuse_enabled, upstream_config, sort_order, latency, enabled
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "idempotent-save", "same-value", "direct", `{}`, 31001, "", "", true, "", 0, 0, false); err != nil {
		t.Fatalf("insert node for repeated save: %v", err)
	}
	var nodeID int
	if err := db.QueryRow("SELECT id FROM proxy_nodes WHERE name = ?", "idempotent-save").Scan(&nodeID); err != nil {
		t.Fatalf("query repeated-save node: %v", err)
	}

	payload := map[string]interface{}{
		"name":              "idempotent-save",
		"remark":            "same-value",
		"type":              "direct",
		"config":            `{}`,
		"inbound_port":      31001,
		"username":          "",
		"password":          "",
		"auth_enabled":      false,
		"enabled":           false,
		"tcp_reuse_enabled": true,
	}
	for attempt := 1; attempt <= 2; attempt++ {
		path := "/api/nodes/" + strconv.Itoa(nodeID)
		recorder := postJSON(
			t,
			handler.UpdateNode,
			http.MethodPut,
			path,
			payload,
			ginParams("id", strconv.Itoa(nodeID)),
		)
		if recorder.Code != http.StatusOK {
			t.Fatalf("repeated save attempt %d failed: status=%d body=%s", attempt, recorder.Code, recorder.Body.String())
		}
	}
}

func TestRemoteDatabaseCleanupVerification(t *testing.T) {
	db := openBatchImportIntegrationDatabase(t)
	if db == nil {
		t.Skip("remote database integration environment is not set")
	}
	defer db.Close()

	var query string
	switch appdb.DialectFor(db) {
	case appdb.DialectPostgres:
		query = `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name IN ('admin_sessions', 'proxy_nodes', 'settings', 'manager_instance_lock')`
	case appdb.DialectMySQL:
		query = `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name IN ('admin_sessions', 'proxy_nodes', 'settings', 'manager_instance_lock')`
	default:
		query = `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('admin_sessions', 'proxy_nodes', 'settings', 'manager_instance_lock')`
	}
	var count int
	if err := db.QueryRow(query).Scan(&count); err != nil {
		t.Fatalf("verify remote database cleanup: %v", err)
	}
	if count != 0 {
		t.Fatalf("remote database cleanup left %d application tables", count)
	}
}

func openBatchImportIntegrationDatabase(t *testing.T) *sql.DB {
	t.Helper()
	if databaseURL := strings.TrimSpace(os.Getenv("SBPM_BATCH_IMPORT_INTEGRATION_DATABASE_URL")); databaseURL != "" {
		db, dialect, err := appdb.OpenURL(databaseURL)
		if err != nil {
			t.Fatalf("open remote database: %v", err)
		}
		appdb.RegisterDialect(db, dialect)
		return db
	}

	tursoURL := strings.TrimSpace(os.Getenv("SBPM_BATCH_IMPORT_INTEGRATION_TURSO_URL"))
	tursoToken := strings.TrimSpace(os.Getenv("SBPM_BATCH_IMPORT_INTEGRATION_TURSO_TOKEN"))
	if tursoURL == "" || tursoToken == "" {
		return nil
	}
	for _, key := range []string{
		"DATABASE_URL", "POSTGRES_DATABASE_URL", "POSTGRES_URL", "PGSQL_DATABASE_URL", "PGSQL",
		"MYSQL_DATABASE_URL", "MYSQL",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("TURSO_DATABASE_URL", tursoURL)
	t.Setenv("TURSO_AUTH_TOKEN", tursoToken)
	db, err := appdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open Turso database: %v", err)
	}
	return db
}

func resetBatchImportIntegrationTables(t *testing.T, db *sql.DB) {
	t.Helper()
	var inspectionQuery string
	switch appdb.DialectFor(db) {
	case appdb.DialectPostgres:
		inspectionQuery = `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name IN ('admin_sessions', 'proxy_nodes', 'settings', 'manager_instance_lock')`
	case appdb.DialectMySQL:
		inspectionQuery = `SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name IN ('admin_sessions', 'proxy_nodes', 'settings', 'manager_instance_lock')`
	default:
		inspectionQuery = `SELECT name FROM sqlite_master WHERE type = 'table' AND name IN ('admin_sessions', 'proxy_nodes', 'settings', 'manager_instance_lock')`
	}
	rows, err := db.Query(inspectionQuery)
	if err != nil {
		t.Fatalf("inspect integration tables: %v", err)
	}
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			_ = rows.Close()
			t.Fatalf("inspect integration table name: %v", err)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatalf("inspect integration tables: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close integration table inspection: %v", err)
	}
	for _, table := range []string{"admin_sessions", "proxy_nodes", "settings", "manager_instance_lock"} {
		if _, err := db.Exec("DROP TABLE IF EXISTS " + table); err != nil {
			t.Fatalf("reset integration table %s: %v", table, err)
		}
	}
}
