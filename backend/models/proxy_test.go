package models

import (
	"database/sql"
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
