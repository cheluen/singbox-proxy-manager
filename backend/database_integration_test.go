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

	_, err := db.Exec(`
		INSERT INTO proxy_nodes (name, remark, type, config, inbound_port, username, password, tcp_reuse_enabled, sort_order, latency, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "integration-socks5h", "remote-db", "socks5h", largeConfig, 31001, "user", "pass", true, 0, 0, true)
	if err != nil {
		t.Fatalf("insert node (%s): %v", dialect, err)
	}

	var node models.ProxyNode
	if err := db.QueryRow(`
		SELECT id, name, remark, type, config, inbound_port, username, password, tcp_reuse_enabled,
		       sort_order, node_ip, location, country_code, latency, enabled, created_at, updated_at
		FROM proxy_nodes WHERE name = ?
	`, "integration-socks5h").Scan(
		&node.ID, &node.Name, &node.Remark, &node.Type, &node.Config, &node.InboundPort,
		&node.Username, &node.Password, &node.TCPReuseEnabled, &node.SortOrder, &node.NodeIP,
		&node.Location, &node.CountryCode, &node.Latency, &node.Enabled, &node.CreatedAt, &node.UpdatedAt,
	); err != nil {
		t.Fatalf("query node (%s): %v", dialect, err)
	}
	if node.Type != "socks5h" || !node.TCPReuseEnabled || !node.Enabled || node.CreatedAt.IsZero() || node.UpdatedAt.IsZero() {
		t.Fatalf("unexpected node from %s: %+v", dialect, node)
	}
	if node.Config != largeConfig {
		t.Fatalf("large config was truncated by %s: got=%d want=%d", dialect, len(node.Config), len(largeConfig))
	}

	var preserve bool
	if err := db.QueryRow("SELECT preserve_inbound_ports FROM settings LIMIT 1").Scan(&preserve); err != nil {
		t.Fatalf("query settings (%s): %v", dialect, err)
	}
	if preserve {
		t.Fatalf("default preserve_inbound_ports should be false")
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
