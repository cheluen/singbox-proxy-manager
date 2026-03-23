package models

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

func TestInitDB_AdminPasswordFromEnvOverridesExisting(t *testing.T) {
	t.Setenv("ADMIN_PASSWORD", "")

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := InitDB(db); err != nil {
		t.Fatalf("init db (first): %v", err)
	}

	oldPassword := "old-password-123"
	oldHash, err := bcrypt.GenerateFromPassword([]byte(oldPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash old: %v", err)
	}
	if _, err := db.Exec("UPDATE settings SET admin_password = ?, admin_password_set = 1, updated_at = CURRENT_TIMESTAMP", string(oldHash)); err != nil {
		t.Fatalf("set old password: %v", err)
	}

	future := time.Now().Add(24 * time.Hour).Unix()
	if _, err := db.Exec(
		"INSERT INTO admin_sessions (token_hash, expires_at, user_agent, ip) VALUES (?, ?, '', '')",
		"tok",
		future,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	newPassword := "new-password-456"
	t.Setenv("ADMIN_PASSWORD", newPassword)
	if err := InitDB(db); err != nil {
		t.Fatalf("init db (second): %v", err)
	}

	var currentHash string
	var currentSet int
	if err := db.QueryRow("SELECT admin_password, admin_password_set FROM settings LIMIT 1").Scan(&currentHash, &currentSet); err != nil {
		t.Fatalf("query settings: %v", err)
	}
	if currentSet != 1 {
		t.Fatalf("expected admin_password_set=1, got %d", currentSet)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(newPassword)); err != nil {
		t.Fatalf("expected stored hash to match new env password: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(oldPassword)); err == nil {
		t.Fatalf("expected stored hash to no longer match old password")
	}

	var sessionCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM admin_sessions").Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 0 {
		t.Fatalf("expected sessions to be revoked after env reset, got %d", sessionCount)
	}
}

func TestInitDB_AdminPasswordFromEnvDoesNotRevokeSessionsWhenUnchanged(t *testing.T) {
	password := "stable-password-123"
	t.Setenv("ADMIN_PASSWORD", password)

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := InitDB(db); err != nil {
		t.Fatalf("init db (first): %v", err)
	}

	future := time.Now().Add(24 * time.Hour).Unix()
	if _, err := db.Exec(
		"INSERT INTO admin_sessions (token_hash, expires_at, user_agent, ip) VALUES (?, ?, '', '')",
		"tok",
		future,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	if err := InitDB(db); err != nil {
		t.Fatalf("init db (second): %v", err)
	}

	var sessionCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM admin_sessions").Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 1 {
		t.Fatalf("expected sessions to remain when env password unchanged, got %d", sessionCount)
	}
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
