package models

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

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
		if _, err := db.Exec("UPDATE settings SET admin_password = ?, admin_password_set = 0", string(hash)); err != nil {
			t.Fatalf("seed database password: %v", err)
		}
		if err := ReconcileAdminPassword(db, ""); err != nil {
			t.Fatalf("reconcile database-only password: %v", err)
		}
		var passwordSet int
		if err := db.QueryRow("SELECT admin_password_set FROM settings").Scan(&passwordSet); err != nil {
			t.Fatalf("query password state: %v", err)
		}
		if passwordSet != 1 {
			t.Fatalf("expected valid database password to self-heal password_set, got %d", passwordSet)
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
		if _, err := db.Exec("UPDATE settings SET admin_password = ?, admin_password_set = 1", string(hash)); err != nil {
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
		if _, err := db.Exec("UPDATE settings SET admin_password = ?, admin_password_set = 1", string(legacyHash)); err != nil {
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
		INSERT INTO proxy_nodes (name, type, config, inbound_port, username, password, sort_order, latency, enabled)
		VALUES ('node1', 'direct', '{}', 30001, 'u', 'p', 0, 0, 1)
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
		INSERT INTO proxy_nodes (name, type, config, inbound_port, sort_order, enabled)
		VALUES ('node1', 'direct', '{}', 30001, 0, 1)
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
		id               int
		singletonKey     int
		storedHash       string
		adminPasswordSet int
		authGeneration   int64
		startPort        int
	)
	if err := db.QueryRow(`
		SELECT id, singleton_key, admin_password, admin_password_set, auth_generation, start_port
		FROM settings
	`).Scan(&id, &singletonKey, &storedHash, &adminPasswordSet, &authGeneration, &startPort); err != nil {
		t.Fatalf("query migrated settings: %v", err)
	}
	if id != 1 || singletonKey != 1 || startPort != 31000 {
		t.Fatalf("expected first legacy row to be retained, got id=%d singleton_key=%d start_port=%d", id, singletonKey, startPort)
	}
	if adminPasswordSet != 1 || authGeneration != 0 {
		t.Fatalf("unexpected migrated auth state: password_set=%d generation=%d", adminPasswordSet, authGeneration)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte("first-password-123")); err != nil {
		t.Fatalf("expected first legacy password to be retained: %v", err)
	}

	if _, err := db.Exec(`
		INSERT INTO settings (
			singleton_key, admin_password, admin_password_set, auth_generation,
			start_port, preserve_inbound_ports
		) VALUES (1, '', 0, 0, 33000, 0)
	`); err == nil {
		t.Fatal("expected singleton unique index to reject a second settings row")
	}
}

func TestProxyNodeParseConfigWireGuard(t *testing.T) {
	rawConfig := WireGuardConfig{
		Server:         "engage.cloudflareclient.com",
		ServerPort:     2408,
		LocalAddress:   []string{"172.16.0.2/32", "2606:4700:110:8765::2/128"},
		PrivateKey:     "private-key",
		PeerPublicKey:  "peer-public-key",
		AllowedIPs:     []string{"0.0.0.0/0", "::/0"},
		Reserved:       []uint8{162, 104, 222},
		DomainResolver: "local",
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
