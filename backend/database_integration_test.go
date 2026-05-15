package main

import (
	"database/sql"
	"os"
	"testing"

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

	dialect := appdb.DialectFor(db)
	resetApplicationTables(t, db)
	defer resetApplicationTables(t, db)

	if err := models.InitDB(db); err != nil {
		t.Fatalf("init db (%s): %v", dialect, err)
	}

	_, err = db.Exec(`
		INSERT INTO proxy_nodes (name, remark, type, config, inbound_port, username, password, tcp_reuse_enabled, sort_order, latency, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "integration-socks5h", "remote-db", "socks5h", `{"server":"example.com","server_port":1080}`, 31001, "user", "pass", true, 0, 0, true)
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

	var preserve bool
	if err := db.QueryRow("SELECT preserve_inbound_ports FROM settings LIMIT 1").Scan(&preserve); err != nil {
		t.Fatalf("query settings (%s): %v", dialect, err)
	}
	if preserve {
		t.Fatalf("default preserve_inbound_ports should be false")
	}
}

func resetApplicationTables(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"admin_sessions", "proxy_nodes", "settings"} {
		if _, err := db.Exec("DROP TABLE IF EXISTS " + table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
}
