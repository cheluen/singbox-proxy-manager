package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	appdb "sb-proxy/backend/database"
	"sb-proxy/backend/models"
)

func TestLocalSQLiteDatabaseIntegration(t *testing.T) {
	for _, key := range []string{
		"DATABASE_URL",
		"POSTGRES_DATABASE_URL",
		"POSTGRES_URL",
		"PGSQL_DATABASE_URL",
		"PGSQL",
		"MYSQL_DATABASE_URL",
		"MYSQL",
		"TURSO_DATABASE_URL",
		"TURSO_AUTH_TOKEN",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("ADMIN_PASSWORD", "")

	db, err := openDatabase(t.TempDir())
	if err != nil {
		t.Fatalf("open local SQLite database: %v", err)
	}
	defer db.Close()
	runDatabaseIntegrationContract(t, db)
}

func TestDatabaseURLIntegration(t *testing.T) {
	databaseURL := os.Getenv("SBPM_INTEGRATION_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SBPM_INTEGRATION_DATABASE_URL is not set")
	}

	t.Setenv("DATABASE_URL", databaseURL)
	t.Setenv("TURSO_DATABASE_URL", "")
	t.Setenv("TURSO_AUTH_TOKEN", "")
	t.Setenv("ADMIN_PASSWORD", "")

	db, err := openDatabase(t.TempDir())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	runDatabaseIntegrationContract(t, db)
}

func TestTursoDatabaseIntegration(t *testing.T) {
	tursoURL := os.Getenv("SBPM_INTEGRATION_TURSO_URL")
	tursoToken := os.Getenv("SBPM_INTEGRATION_TURSO_TOKEN")
	if tursoURL == "" || tursoToken == "" {
		t.Skip("SBPM_INTEGRATION_TURSO_URL/SBPM_INTEGRATION_TURSO_TOKEN are not set")
	}

	for _, key := range []string{
		"DATABASE_URL",
		"POSTGRES_DATABASE_URL",
		"POSTGRES_URL",
		"PGSQL_DATABASE_URL",
		"PGSQL",
		"MYSQL_DATABASE_URL",
		"MYSQL",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("TURSO_DATABASE_URL", tursoURL)
	t.Setenv("TURSO_AUTH_TOKEN", tursoToken)
	t.Setenv("ADMIN_PASSWORD", "")

	db, err := openDatabase(t.TempDir())
	if err != nil {
		t.Fatalf("open Turso database: %v", err)
	}
	defer db.Close()
	runDatabaseIntegrationContract(t, db)
}

func runDatabaseIntegrationContract(t *testing.T, db *sql.DB) {
	t.Helper()

	dialect := appdb.DialectFor(db)
	resetApplicationTables(t, db)
	defer resetApplicationTables(t, db)

	if err := models.InitDB(db); err != nil {
		t.Fatalf("init db (%s): %v", dialect, err)
	}
	verifyInstanceLeaseContract(t, db)

	// Keep the payload above MySQL TEXT's 64 KiB limit. Protocol configs can
	// legitimately grow this large (for example, a WireGuard endpoint with
	// many peers), so every supported remote backend must preserve it intact.
	largeConfig := `{"server":"example.com","server_port":1080,"padding":"` + strings.Repeat("x", 70<<10) + `"}`
	largeUpstreamConfig := `{"server":"upstream.example.com","server_port":1080,"padding":"` + strings.Repeat("y", 70<<10) + `"}`

	_, err := db.Exec(`
		INSERT INTO proxy_nodes (
			name, remark, type, config, inbound_port, username, password,
			tcp_reuse_enabled, upstream_mode, upstream_type, upstream_config,
			upstream_ip, upstream_location, upstream_country_code,
			upstream_latency, upstream_error, sort_order, latency, enabled
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "integration-socks5h", "remote-db", "socks5h", largeConfig, 31001, "user", "pass",
		true, models.UpstreamModeCustom, "socks5", largeUpstreamConfig,
		"198.51.100.40", "Upstream Integration", "UI", 45, "", 0, 0, true)
	if err != nil {
		t.Fatalf("insert node (%s): %v", dialect, err)
	}

	var node models.ProxyNode
	if err := db.QueryRow(`
		SELECT id, name, remark, type, config, inbound_port, username, password, tcp_reuse_enabled,
		       upstream_mode, upstream_type, upstream_config, upstream_ip, upstream_location,
		       upstream_country_code, upstream_latency, upstream_error, sort_order, node_ip,
		       location, country_code, latency, enabled, created_at, updated_at
		FROM proxy_nodes WHERE name = ?
	`, "integration-socks5h").Scan(
		&node.ID, &node.Name, &node.Remark, &node.Type, &node.Config, &node.InboundPort,
		&node.Username, &node.Password, &node.TCPReuseEnabled, &node.UpstreamMode,
		&node.UpstreamType, &node.UpstreamConfig, &node.UpstreamIP, &node.UpstreamLocation,
		&node.UpstreamCountryCode, &node.UpstreamLatency, &node.UpstreamError, &node.SortOrder,
		&node.NodeIP, &node.Location, &node.CountryCode, &node.Latency, &node.Enabled,
		&node.CreatedAt, &node.UpdatedAt,
	); err != nil {
		t.Fatalf("query node (%s): %v", dialect, err)
	}
	if node.Type != "socks5h" || !node.TCPReuseEnabled || !node.Enabled || node.CreatedAt.IsZero() || node.UpdatedAt.IsZero() {
		t.Fatalf("unexpected node from %s: %+v", dialect, node)
	}
	if node.Config != largeConfig {
		t.Fatalf("large config was truncated by %s: got=%d want=%d", dialect, len(node.Config), len(largeConfig))
	}
	if node.UpstreamMode != models.UpstreamModeCustom || node.UpstreamType != "socks5" || node.UpstreamConfig != largeUpstreamConfig {
		t.Fatalf("upstream config was not preserved by %s: mode=%q type=%q got=%d want=%d", dialect, node.UpstreamMode, node.UpstreamType, len(node.UpstreamConfig), len(largeUpstreamConfig))
	}
	if node.UpstreamIP != "198.51.100.40" || node.UpstreamLocation != "Upstream Integration" || node.UpstreamCountryCode != "UI" || node.UpstreamLatency != 45 || node.UpstreamError != "" {
		t.Fatalf("upstream IP check state was not preserved by %s: %+v", dialect, node)
	}

	var preserve bool
	if err := db.QueryRow("SELECT preserve_inbound_ports FROM settings LIMIT 1").Scan(&preserve); err != nil {
		t.Fatalf("query settings (%s): %v", dialect, err)
	}
	if preserve {
		t.Fatalf("default preserve_inbound_ports should be false")
	}

	verifyUpstreamMigrationContract(t, db, largeUpstreamConfig)
}

func verifyUpstreamMigrationContract(t *testing.T, db *sql.DB, largeUpstreamConfig string) {
	t.Helper()
	dialect := appdb.DialectFor(db)
	if _, err := db.Exec(`
		INSERT INTO proxy_nodes (name, type, config, inbound_port, upstream_config, sort_order, enabled)
		VALUES ('integration-legacy-detour', 'direct', '{"detour":"direct"}', 31002, '', 1, ?)
	`, true); err != nil {
		t.Fatalf("seed legacy detour node (%s): %v", dialect, err)
	}

	for _, column := range []struct {
		table string
		name  string
	}{
		{table: "proxy_nodes", name: "upstream_mode"},
		{table: "proxy_nodes", name: "upstream_type"},
		{table: "proxy_nodes", name: "upstream_config"},
		{table: "proxy_nodes", name: "upstream_ip"},
		{table: "proxy_nodes", name: "upstream_location"},
		{table: "proxy_nodes", name: "upstream_country_code"},
		{table: "proxy_nodes", name: "upstream_latency"},
		{table: "proxy_nodes", name: "upstream_error"},
		{table: "settings", name: "admin_password_env_fingerprint"},
		{table: "settings", name: "global_upstream_enabled"},
		{table: "settings", name: "global_upstream_type"},
		{table: "settings", name: "global_upstream_config"},
		{table: "settings", name: "global_upstream_ip"},
		{table: "settings", name: "global_upstream_location"},
		{table: "settings", name: "global_upstream_country_code"},
		{table: "settings", name: "global_upstream_latency"},
		{table: "settings", name: "global_upstream_error"},
	} {
		if _, err := db.Exec("ALTER TABLE " + column.table + " DROP COLUMN " + column.name); err != nil {
			t.Fatalf("drop legacy migration column %s.%s (%s): %v", column.table, column.name, dialect, err)
		}
	}
	if err := models.InitDB(db); err != nil {
		t.Fatalf("re-run upstream schema migration (%s): %v", dialect, err)
	}

	rows, err := db.Query("SELECT name, upstream_mode FROM proxy_nodes ORDER BY sort_order")
	if err != nil {
		t.Fatalf("query migrated upstream modes (%s): %v", dialect, err)
	}
	defer rows.Close()
	wantModes := map[string]string{
		"integration-socks5h":       models.UpstreamModeGlobal,
		"integration-legacy-detour": models.UpstreamModeLegacy,
	}
	for rows.Next() {
		var name, mode string
		if err := rows.Scan(&name, &mode); err != nil {
			t.Fatalf("scan migrated upstream mode (%s): %v", dialect, err)
		}
		if want, exists := wantModes[name]; !exists || mode != want {
			t.Fatalf("migrated upstream mode for %q on %s=%q want %q", name, dialect, mode, want)
		}
		delete(wantModes, name)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatalf("iterate migrated upstream modes (%s): %v", dialect, err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close migrated upstream modes (%s): %v", dialect, err)
	}
	if len(wantModes) != 0 {
		t.Fatalf("missing migrated nodes on %s: %v", dialect, wantModes)
	}

	if _, err := db.Exec(`
		UPDATE proxy_nodes
		SET upstream_mode = ?, upstream_type = 'socks5', upstream_config = ?,
		    upstream_ip = '198.51.100.41', upstream_location = 'Migrated Upstream',
		    upstream_country_code = 'MU', upstream_latency = 46, upstream_error = ''
		WHERE name = 'integration-socks5h'
	`, models.UpstreamModeCustom, largeUpstreamConfig); err != nil {
		t.Fatalf("write migrated node upstream config (%s): %v", dialect, err)
	}
	if _, err := db.Exec(`
		UPDATE settings
		SET global_upstream_enabled = ?, global_upstream_type = 'socks5', global_upstream_config = ?,
		    global_upstream_ip = '198.51.100.42', global_upstream_location = 'Migrated Global',
		    global_upstream_country_code = 'MG', global_upstream_latency = 47,
		    global_upstream_error = ''
		WHERE singleton_key = 1
	`, true, largeUpstreamConfig); err != nil {
		t.Fatalf("write migrated global upstream config (%s): %v", dialect, err)
	}
	var nodeConfig, nodeUpstreamIP, globalConfig, globalUpstreamIP, passwordFingerprint string
	var nodeUpstreamLatency, globalUpstreamLatency int
	if err := db.QueryRow(`
		SELECT node.upstream_config, node.upstream_ip, node.upstream_latency,
		       singleton.global_upstream_config, singleton.global_upstream_ip,
		       singleton.global_upstream_latency, singleton.admin_password_env_fingerprint
		FROM proxy_nodes AS node
		CROSS JOIN settings AS singleton
		WHERE node.name = 'integration-socks5h' AND singleton.singleton_key = 1
	`).Scan(
		&nodeConfig,
		&nodeUpstreamIP,
		&nodeUpstreamLatency,
		&globalConfig,
		&globalUpstreamIP,
		&globalUpstreamLatency,
		&passwordFingerprint,
	); err != nil {
		t.Fatalf("read migrated upstream configs (%s): %v", dialect, err)
	}
	if nodeConfig != largeUpstreamConfig || globalConfig != largeUpstreamConfig {
		t.Fatalf("migrated upstream LONGTEXT truncated on %s: node=%d global=%d want=%d", dialect, len(nodeConfig), len(globalConfig), len(largeUpstreamConfig))
	}
	if nodeUpstreamIP != "198.51.100.41" || nodeUpstreamLatency != 46 || globalUpstreamIP != "198.51.100.42" || globalUpstreamLatency != 47 || passwordFingerprint != "" {
		t.Fatalf(
			"migrated password/upstream check fields failed on %s: node=%q/%d global=%q/%d fingerprint=%q",
			dialect,
			nodeUpstreamIP,
			nodeUpstreamLatency,
			globalUpstreamIP,
			globalUpstreamLatency,
			passwordFingerprint,
		)
	}
	if dialect == appdb.DialectMySQL {
		verifyMySQLLongTextContract(t, db)
	}
}

func verifyMySQLLongTextContract(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, column := range []struct {
		table string
		name  string
	}{
		{table: "proxy_nodes", name: "config"},
		{table: "proxy_nodes", name: "upstream_config"},
		{table: "settings", name: "global_upstream_config"},
	} {
		var dataType, isNullable string
		var defaultValue sql.NullString
		if err := db.QueryRow(`
			SELECT data_type, is_nullable, column_default
			FROM information_schema.columns
			WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?
		`, column.table, column.name).Scan(&dataType, &isNullable, &defaultValue); err != nil {
			t.Fatalf("inspect MySQL column %s.%s: %v", column.table, column.name, err)
		}
		if !strings.EqualFold(dataType, "longtext") || !strings.EqualFold(isNullable, "NO") || defaultValue.Valid {
			t.Fatalf(
				"unexpected MySQL column %s.%s: type=%q nullable=%q default=%q",
				column.table,
				column.name,
				dataType,
				isNullable,
				defaultValue.String,
			)
		}
	}
}

func verifyInstanceLeaseContract(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	first, err := appdb.AcquireInstanceLease(ctx, db)
	if err != nil {
		t.Fatalf("acquire first instance lease: %v", err)
	}
	if _, err := appdb.AcquireInstanceLease(ctx, db); !errors.Is(err, appdb.ErrManagerInstanceActive) {
		_ = first.Release(context.Background())
		t.Fatalf("second manager was not rejected: %v", err)
	}
	if err := first.Release(ctx); err != nil {
		t.Fatalf("release first instance lease: %v", err)
	}

	second, err := appdb.AcquireInstanceLease(ctx, db)
	if err != nil {
		t.Fatalf("acquire instance lease after handoff: %v", err)
	}
	if err := second.Release(ctx); err != nil {
		t.Fatalf("release handoff instance lease: %v", err)
	}
}

func resetApplicationTables(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"admin_sessions", "proxy_nodes", "settings", "manager_instance_lock"} {
		if _, err := db.Exec("DROP TABLE IF EXISTS " + table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
}
