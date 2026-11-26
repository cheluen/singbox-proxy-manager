package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"

	appdb "sb-proxy/backend/database"
)

func TestGroupUpstreamSchemaDialects(t *testing.T) {
	for _, dialect := range []appdb.Dialect{appdb.DialectSQLite, appdb.DialectTurso, appdb.DialectPostgres, appdb.DialectMySQL} {
		t.Run(string(dialect), func(t *testing.T) {
			fields := make(map[string]string)
			for _, field := range groupUpstreamColumns(dialect) {
				name, definition, _ := strings.Cut(field, " ")
				fields[name] = definition
			}
			if len(fields) != 9 || fields["upstream_enabled"] != "BOOLEAN NOT NULL DEFAULT FALSE" || fields["upstream_latency"] != "INTEGER NOT NULL DEFAULT 0" || fields["upstream_revision"] != "INTEGER NOT NULL DEFAULT 0" {
				t.Errorf("unexpected group upstream schema %v", fields)
			}
			for name, mysqlType := range map[string]string{"type": "VARCHAR(32)", "config": "LONGTEXT", "ip": "VARCHAR(255)", "location": "VARCHAR(255)", "country_code": "VARCHAR(32)", "error": "VARCHAR(2048)"} {
				want := "TEXT NOT NULL DEFAULT ''"
				if dialect == appdb.DialectMySQL {
					want = mysqlType + " NOT NULL"
					if name != "config" {
						want += " DEFAULT ''"
					}
				}
				if got := fields["upstream_"+name]; got != want {
					t.Errorf("%s: %q, want %q", name, got, want)
				}
			}
		})
	}
}

func TestGroupNodeRebuildWithoutSQLiteSequence(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(strings.Replace(schemaStatements(appdb.DialectSQLite)[0], "PRIMARY KEY AUTOINCREMENT", "PRIMARY KEY", 1)); err != nil {
		t.Fatal(err)
	}
	var sequence int
	if err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='sqlite_sequence'").Scan(&sequence); err != nil || sequence != 0 {
		t.Fatalf("unexpected sequence table: %d %v", sequence, err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = rebuildGroupNodes(tx, appdb.DialectSQLite); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateNodeListColumnWidths(t *testing.T) {
	if err := ValidateNodeListColumnWidths(map[string]int{
		"name": 40, "port": 1200, "user": 100, "pass": 110, "type": 80,
		"server": 200, "region": 160, "ip": 180, "latency": 90,
	}); err != nil {
		t.Fatalf("known columns and inclusive bounds were rejected: %v", err)
	}
	if err := ValidateNodeListColumnWidths(map[string]int{}); err != nil {
		t.Fatalf("empty widths should be valid: %v", err)
	}
	for _, widths := range []map[string]int{
		{"unknown": 80}, {"Name": 80}, {"name": -20}, {"name": 0}, {"name": 39}, {"name": 1201},
	} {
		if err := ValidateNodeListColumnWidths(widths); err == nil || !strings.Contains(err.Error(), "node_list_column_widths") {
			t.Fatalf("expected a column width validation error for %v, got %v", widths, err)
		}
	}
}

func TestNodeListColumnWidthsLegacySQLiteMigration(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		admin_password TEXT NOT NULL,
		start_port INTEGER DEFAULT 10000
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO settings (admin_password, start_port) VALUES ('', 32047)`); err != nil {
		t.Fatal(err)
	}
	if err := InitDB(db); err != nil {
		t.Fatal(err)
	}
	assertWidths := func(expected map[string]int) {
		t.Helper()
		settings, err := LoadRuntimeSettings(context.Background(), db)
		if err != nil || settings.NodeListColumnWidths == nil || !maps.Equal(settings.NodeListColumnWidths, expected) {
			t.Fatalf("migrated settings = %+v, %v, want widths %v", settings, err, expected)
		}
	}
	group, err := LoadGroup(context.Background(), db, 0)
	if err != nil || group.StartPort != 32047 || !group.AllocatePort {
		t.Fatalf("默认分组未继承旧起始端口：%+v，%v", group, err)
	}
	assertWidths(map[string]int{})
	if _, err := db.Exec(`UPDATE settings SET node_list_column_widths = '{"name":170}' WHERE singleton_key = 1`); err != nil {
		t.Fatal(err)
	}
	if err := InitDB(db); err != nil {
		t.Fatalf("repeat width migration: %v", err)
	}
	assertWidths(map[string]int{"name": 170})
	for _, empty := range []string{"", "{}", "null", " \n "} {
		if _, err := db.Exec(`UPDATE settings SET node_list_column_widths = ? WHERE singleton_key = 1`, empty); err != nil {
			t.Fatal(err)
		}
		assertWidths(map[string]int{})
	}
}

func TestReconcileAdminPasswordChangedEnvironmentOverridesDatabaseAndRevokesSessions(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := InitDB(db); err != nil {
		t.Fatalf("init db (first): %v", err)
	}

	oldEnvironmentPassword := "old-environment-password-123"
	if err := ReconcileAdminPassword(db, oldEnvironmentPassword); err != nil {
		t.Fatalf("reconcile initial environment password: %v", err)
	}

	future := time.Now().Add(24 * time.Hour).Unix()
	if _, err := db.Exec(
		`INSERT INTO admin_sessions (token_hash, auth_generation, expires_at, user_agent, ip)
		 SELECT ?, auth_generation, ?, '', '' FROM settings WHERE singleton_key = 1`,
		"tok",
		future,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	newEnvironmentPassword := "new-environment-password-456"
	if err := ReconcileAdminPassword(db, newEnvironmentPassword); err != nil {
		t.Fatalf("reconcile changed environment password: %v", err)
	}

	var currentHash string
	var fingerprint string
	if err := db.QueryRow("SELECT admin_password, admin_password_env_fingerprint FROM settings LIMIT 1").Scan(&currentHash, &fingerprint); err != nil {
		t.Fatalf("query settings: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(newEnvironmentPassword)); err != nil {
		t.Fatalf("expected stored hash to match new env password: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(oldEnvironmentPassword)); err == nil {
		t.Fatalf("expected stored hash to no longer match old password")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(fingerprint), []byte(newEnvironmentPassword)); err != nil {
		t.Fatalf("expected fingerprint to track changed environment password: %v", err)
	}

	var sessionCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM admin_sessions").Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 0 {
		t.Fatalf("expected sessions to be revoked after env reset, got %d", sessionCount)
	}
}

func TestReconcileAdminPasswordUnchangedEnvironmentPreservesPanelPasswordAndSessions(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := InitDB(db); err != nil {
		t.Fatalf("init db (first): %v", err)
	}
	environmentPassword := "stable-environment-password-123"
	if err := ReconcileAdminPassword(db, environmentPassword); err != nil {
		t.Fatalf("reconcile initial environment password: %v", err)
	}

	panelPassword := "admin123"
	panelHash, err := bcrypt.GenerateFromPassword([]byte(panelPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash panel password: %v", err)
	}
	if _, err := db.Exec(
		"UPDATE settings SET admin_password = ?, auth_generation = auth_generation + 1 WHERE singleton_key = 1",
		string(panelHash),
	); err != nil {
		t.Fatalf("simulate panel password change: %v", err)
	}

	future := time.Now().Add(24 * time.Hour).Unix()
	if _, err := db.Exec(
		`INSERT INTO admin_sessions (token_hash, auth_generation, expires_at, user_agent, ip)
		 SELECT ?, auth_generation, ?, '', '' FROM settings WHERE singleton_key = 1`,
		"tok",
		future,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	if err := ReconcileAdminPassword(db, environmentPassword); err != nil {
		t.Fatalf("reconcile unchanged environment password: %v", err)
	}

	var storedHash string
	if err := db.QueryRow("SELECT admin_password FROM settings WHERE singleton_key = 1").Scan(&storedHash); err != nil {
		t.Fatalf("query panel password: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(panelPassword)); err != nil {
		t.Fatalf("unchanged environment password overwrote panel password: %v", err)
	}

	var sessionCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM admin_sessions").Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 1 {
		t.Fatalf("expected sessions to remain when env password unchanged, got %d", sessionCount)
	}
}

func TestReconcileAdminPasswordStartupRules(t *testing.T) {
	t.Run("empty database requires environment password", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		defer db.Close()
		if err := InitDB(db); err != nil {
			t.Fatalf("init db: %v", err)
		}
		if err := ReconcileAdminPassword(db, ""); !errors.Is(err, ErrAdminPasswordRequired) {
			t.Fatalf("expected ErrAdminPasswordRequired, got %v", err)
		}
	})

	t.Run("database password works without environment password", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		defer db.Close()
		if err := InitDB(db); err != nil {
			t.Fatalf("init db: %v", err)
		}
		hash, err := bcrypt.GenerateFromPassword([]byte("database-password-123"), bcrypt.DefaultCost)
		if err != nil {
			t.Fatalf("hash database password: %v", err)
		}
		if _, err := db.Exec("UPDATE settings SET admin_password = ?", string(hash)); err != nil {
			t.Fatalf("seed database password: %v", err)
		}
		if err := ReconcileAdminPassword(db, ""); err != nil {
			t.Fatalf("reconcile database-only password: %v", err)
		}
	})

	t.Run("legacy database records fingerprint without overwriting", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		defer db.Close()
		if err := InitDB(db); err != nil {
			t.Fatalf("init db: %v", err)
		}
		databasePassword := "database-password-123"
		hash, err := bcrypt.GenerateFromPassword([]byte(databasePassword), bcrypt.DefaultCost)
		if err != nil {
			t.Fatalf("hash database password: %v", err)
		}
		if _, err := db.Exec("UPDATE settings SET admin_password = ?", string(hash)); err != nil {
			t.Fatalf("seed legacy database password: %v", err)
		}
		environmentPassword := "different-environment-password-456"
		if err := ReconcileAdminPassword(db, environmentPassword); err != nil {
			t.Fatalf("reconcile legacy database: %v", err)
		}
		var storedHash, fingerprint string
		if err := db.QueryRow("SELECT admin_password, admin_password_env_fingerprint FROM settings").Scan(&storedHash, &fingerprint); err != nil {
			t.Fatalf("query reconciled database: %v", err)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(databasePassword)); err != nil {
			t.Fatalf("legacy database password was overwritten: %v", err)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(fingerprint), []byte(environmentPassword)); err != nil {
			t.Fatalf("environment fingerprint was not recorded: %v", err)
		}
	})

	t.Run("environment password preserves exact bytes", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		defer db.Close()
		if err := InitDB(db); err != nil {
			t.Fatalf("init db: %v", err)
		}
		environmentPassword := "  exact-environment-password  "
		if err := ReconcileAdminPassword(db, environmentPassword); err != nil {
			t.Fatalf("reconcile exact environment password: %v", err)
		}
		var storedHash, fingerprint string
		if err := db.QueryRow("SELECT admin_password, admin_password_env_fingerprint FROM settings").Scan(&storedHash, &fingerprint); err != nil {
			t.Fatalf("query exact environment password: %v", err)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(environmentPassword)); err != nil {
			t.Fatalf("stored password did not preserve surrounding spaces: %v", err)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(fingerprint), []byte(environmentPassword)); err != nil {
			t.Fatalf("fingerprint did not preserve surrounding spaces: %v", err)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(strings.TrimSpace(environmentPassword))); err == nil {
			t.Fatal("stored password unexpectedly matched a trimmed environment value")
		}
	})

	t.Run("fingerprinted legacy literal remains stable", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		defer db.Close()
		if err := InitDB(db); err != nil {
			t.Fatalf("init db: %v", err)
		}
		if err := ReconcileAdminPassword(db, "admin123"); err != nil {
			t.Fatalf("reconcile initial environment password: %v", err)
		}
		if _, err := db.Exec(`
			INSERT INTO admin_sessions (token_hash, auth_generation, expires_at, user_agent, ip)
			SELECT 'stable-session', auth_generation, ?, '', '' FROM settings
		`, time.Now().Add(time.Hour).Unix()); err != nil {
			t.Fatalf("seed stable session: %v", err)
		}
		if err := ReconcileAdminPassword(db, "admin123"); err != nil {
			t.Fatalf("reconcile unchanged fingerprinted password: %v", err)
		}
		var sessionCount int
		if err := db.QueryRow("SELECT COUNT(*) FROM admin_sessions").Scan(&sessionCount); err != nil {
			t.Fatalf("count stable sessions: %v", err)
		}
		if sessionCount != 1 {
			t.Fatalf("unchanged fingerprinted password revoked sessions: %d", sessionCount)
		}
	})

	t.Run("unfingerprinted legacy default is invalid", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		defer db.Close()
		if err := InitDB(db); err != nil {
			t.Fatalf("init db: %v", err)
		}
		legacyHash, err := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.DefaultCost)
		if err != nil {
			t.Fatalf("hash legacy default: %v", err)
		}
		if _, err := db.Exec("UPDATE settings SET admin_password = ?", string(legacyHash)); err != nil {
			t.Fatalf("seed legacy default: %v", err)
		}
		if err := ReconcileAdminPassword(db, ""); !errors.Is(err, ErrAdminPasswordRequired) {
			t.Fatalf("expected legacy default to require recovery, got %v", err)
		}
	})

	t.Run("environment password rejects bcrypt overflow", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		defer db.Close()
		if err := InitDB(db); err != nil {
			t.Fatalf("init db: %v", err)
		}
		err = ReconcileAdminPassword(db, strings.Repeat("x", BcryptMaxPasswordBytes+1))
		if err == nil || !strings.Contains(err.Error(), "72 bytes") {
			t.Fatalf("expected bcrypt length error, got %v", err)
		}
	})
}

func TestInitDBProxyNodeTCPReuseEnabledDefaultsToTrue(t *testing.T) {
	t.Setenv("ADMIN_PASSWORD", "")

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := InitDB(db); err != nil {
		t.Fatalf("init db: %v", err)
	}

	if _, err := db.Exec(`
		INSERT INTO proxy_nodes (name, type, config, inbound_port, username, password, sort_order, latency, enabled, group_id)
		SELECT 'node1', 'direct', '{}', 30001, 'u', 'p', 0, 0, 1, id FROM node_groups ORDER BY sort_order, id LIMIT 1
	`); err != nil {
		t.Fatalf("insert proxy node: %v", err)
	}

	var tcpReuseEnabled int
	if err := db.QueryRow("SELECT tcp_reuse_enabled FROM proxy_nodes LIMIT 1").Scan(&tcpReuseEnabled); err != nil {
		t.Fatalf("query tcp_reuse_enabled: %v", err)
	}
	if tcpReuseEnabled != 1 {
		t.Fatalf("expected tcp_reuse_enabled default to 1, got %d", tcpReuseEnabled)
	}
}

func TestInitDBProxyNodeUpstreamModeDefaultsToDirect(t *testing.T) {
	t.Setenv("ADMIN_PASSWORD", "")
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := InitDB(db); err != nil {
		t.Fatalf("init db: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO proxy_nodes (name, type, config, inbound_port, sort_order, enabled, group_id)
		SELECT 'node1', 'direct', '{}', 30001, 0, 1, id FROM node_groups ORDER BY sort_order, id LIMIT 1
	`); err != nil {
		t.Fatalf("insert proxy node: %v", err)
	}
	var mode string
	if err := db.QueryRow("SELECT upstream_mode FROM proxy_nodes LIMIT 1").Scan(&mode); err != nil {
		t.Fatalf("query upstream mode: %v", err)
	}
	if mode != UpstreamModeNone {
		t.Fatalf("upstream mode=%q want %q", mode, UpstreamModeNone)
	}
}

func TestInitDBMigratesLegacyUpstreamModesWithoutChangingDetours(t *testing.T) {
	t.Setenv("ADMIN_PASSWORD", "")
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE proxy_nodes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			config TEXT NOT NULL,
			inbound_port INTEGER NOT NULL,
			username TEXT DEFAULT '',
			password TEXT DEFAULT '',
			sort_order INTEGER NOT NULL,
			node_ip TEXT DEFAULT '',
			location TEXT DEFAULT '',
			country_code TEXT DEFAULT '',
			latency INTEGER DEFAULT 0,
			enabled INTEGER DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		t.Fatalf("create legacy nodes: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO proxy_nodes (name, type, config, inbound_port, sort_order)
		VALUES
			('plain', 'direct', '{}', 30001, 0),
			('chained', 'direct', '{"detour":"direct"}', 30002, 1)
	`); err != nil {
		t.Fatalf("seed legacy nodes: %v", err)
	}
	if err := InitDB(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	rows, err := db.Query("SELECT name, upstream_mode FROM proxy_nodes ORDER BY sort_order")
	if err != nil {
		t.Fatalf("query migrated modes: %v", err)
	}
	defer rows.Close()
	want := []struct{ name, mode string }{{"plain", UpstreamModeGlobal}, {"chained", UpstreamModeLegacy}}
	for index := 0; rows.Next(); index++ {
		if index >= len(want) {
			t.Fatal("unexpected migrated row")
		}
		var name, mode string
		if err := rows.Scan(&name, &mode); err != nil {
			t.Fatalf("scan migrated mode: %v", err)
		}
		if name != want[index].name || mode != want[index].mode {
			t.Fatalf("row %d=(%q,%q) want (%q,%q)", index, name, mode, want[index].name, want[index].mode)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated modes: %v", err)
	}
}

func TestInitDBMigratesDuplicateLegacySettingsToSingleton(t *testing.T) {
	t.Setenv("ADMIN_PASSWORD", "")

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		CREATE TABLE settings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			admin_password TEXT NOT NULL,
			start_port INTEGER DEFAULT 10000,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		t.Fatalf("create legacy settings: %v", err)
	}

	firstHash, err := bcrypt.GenerateFromPassword([]byte("first-password-123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash first password: %v", err)
	}
	secondHash, err := bcrypt.GenerateFromPassword([]byte("second-password-456"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash second password: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO settings (admin_password, start_port) VALUES (?, ?), (?, ?)",
		string(firstHash),
		31000,
		string(secondHash),
		32000,
	); err != nil {
		t.Fatalf("seed duplicate legacy settings: %v", err)
	}

	if err := InitDB(db); err != nil {
		t.Fatalf("migrate legacy database: %v", err)
	}
	if err := ReconcileAdminPassword(db, ""); err != nil {
		t.Fatalf("reconcile migrated password: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM settings").Scan(&count); err != nil {
		t.Fatalf("count settings: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one normalized settings row, got %d", count)
	}

	var (
		id             int
		singletonKey   int
		storedHash     string
		authGeneration int64
	)
	if err := db.QueryRow(`
		SELECT id, singleton_key, admin_password, auth_generation
		FROM settings
	`).Scan(&id, &singletonKey, &storedHash, &authGeneration); err != nil {
		t.Fatalf("query migrated settings: %v", err)
	}
	group, err := LoadGroup(context.Background(), db, 0)
	if err != nil || id != 1 || singletonKey != 1 || group.StartPort != 31000 {
		t.Fatalf("默认分组未继承首条旧设置：id=%d singleton_key=%d group=%+v err=%v", id, singletonKey, group, err)
	}
	if authGeneration != 0 {
		t.Fatalf("unexpected migrated auth generation: %d", authGeneration)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte("first-password-123")); err != nil {
		t.Fatalf("expected first legacy password to be retained: %v", err)
	}

	if _, err := db.Exec(`
		INSERT INTO settings (
			singleton_key, admin_password, auth_generation, preserve_inbound_ports
		) VALUES (1, '', 0, 0)
	`); err == nil {
		t.Fatal("expected singleton unique index to reject a second settings row")
	}
}

func TestProxyNodeParseConfigWireGuard(t *testing.T) {
	rawConfig := WireGuardConfig{
		DialerOptions: DialerOptions{DomainResolver: "local"},
		Server:        "engage.cloudflareclient.com",
		ServerPort:    2408,
		LocalAddress:  []string{"172.16.0.2/32", "2606:4700:110:8765::2/128"},
		PrivateKey:    "private-key",
		PeerPublicKey: "peer-public-key",
		AllowedIPs:    []string{"0.0.0.0/0", "::/0"},
		Reserved:      []uint8{162, 104, 222},
	}
	configJSON, err := json.Marshal(rawConfig)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}

	node := ProxyNode{
		Type:   "wireguard",
		Config: string(configJSON),
	}

	parsed, err := node.ParseConfig()
	if err != nil {
		t.Fatalf("ParseConfig failed: %v", err)
	}

	cfg, ok := parsed.(*WireGuardConfig)
	if !ok {
		t.Fatalf("unexpected config type: %T", parsed)
	}
	if cfg.Server != rawConfig.Server || cfg.ServerPort != rawConfig.ServerPort {
		t.Fatalf("unexpected endpoint: %+v", cfg)
	}
	if len(cfg.LocalAddress) != 2 || cfg.LocalAddress[0] != "172.16.0.2/32" {
		t.Fatalf("unexpected local addresses: %+v", cfg.LocalAddress)
	}
	if len(cfg.Reserved) != 3 || cfg.Reserved[0] != 162 || cfg.Reserved[2] != 222 {
		t.Fatalf("unexpected reserved: %+v", cfg.Reserved)
	}
}
