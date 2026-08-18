package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sb-proxy/backend/services"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

func TestLoginAndPanelPasswordChangeUseDatabaseWhenEnvironmentPasswordExists(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ADMIN_PASSWORD", "environment-password-123")
	h := newTestHandler(t, nil)

	panelPassword := "panel-password-456"
	hash, err := bcrypt.GenerateFromPassword([]byte(panelPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash panel password: %v", err)
	}
	if _, err := h.db.Exec(
		"UPDATE settings SET admin_password = ? WHERE singleton_key = 1",
		string(hash),
	); err != nil {
		t.Fatalf("seed panel password: %v", err)
	}

	login := postJSON(t, h.Login, http.MethodPost, "/api/login", map[string]string{"password": panelPassword}, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("database password login failed: status=%d body=%s", login.Code, login.Body.String())
	}
	environmentLogin := postJSON(t, h.Login, http.MethodPost, "/api/login", map[string]string{"password": "environment-password-123"}, nil)
	if environmentLogin.Code != http.StatusUnauthorized {
		t.Fatalf("environment password bypassed database password: status=%d body=%s", environmentLogin.Code, environmentLogin.Body.String())
	}

	newPanelPassword := "new-panel-password-789"
	update := postJSON(t, h.UpdateSettings, http.MethodPut, "/api/settings", map[string]string{"admin_password": newPanelPassword}, nil)
	if update.Code != http.StatusOK {
		t.Fatalf("panel password change was blocked by environment: status=%d body=%s", update.Code, update.Body.String())
	}
	var storedHash string
	if err := h.db.QueryRow("SELECT admin_password FROM settings WHERE singleton_key = 1").Scan(&storedHash); err != nil {
		t.Fatalf("query changed password: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(newPanelPassword)); err != nil {
		t.Fatalf("panel password was not persisted: %v", err)
	}
}

func TestSettingsPasswordRejectsBcryptOverflow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newTestHandler(t, nil)
	overflow := strings.Repeat("x", 73)

	update := postJSON(t, h.UpdateSettings, http.MethodPut, "/api/settings", map[string]string{"admin_password": overflow}, nil)
	if update.Code != http.StatusBadRequest {
		t.Fatalf("settings accepted bcrypt overflow: status=%d body=%s", update.Code, update.Body.String())
	}
}

func TestSettingsPasswordCountsUnicodeCharacters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newTestHandler(t, nil)
	shortUnicodePassword := strings.Repeat("\U0001F600", 4)

	update := postJSON(t, h.UpdateSettings, http.MethodPut, "/api/settings", map[string]string{"admin_password": shortUnicodePassword}, nil)
	if update.Code != http.StatusBadRequest {
		t.Fatalf("settings accepted four Unicode characters: status=%d body=%s", update.Code, update.Body.String())
	}

	validUnicodePassword := strings.Repeat("\u754c", 8)
	update = postJSON(t, h.UpdateSettings, http.MethodPut, "/api/settings", map[string]string{"admin_password": validUnicodePassword}, nil)
	if update.Code != http.StatusOK {
		t.Fatalf("settings rejected eight Unicode characters: status=%d body=%s", update.Code, update.Body.String())
	}
}

func TestCreateAdminSession_DefaultTTLIs168Hours(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ADMIN_SESSION_TTL_HOURS", "")

	h := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, nil
	})

	_, expiry, err := h.createAdminSession(nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	dur := time.Until(expiry)
	if dur > 168*time.Hour || dur < 167*time.Hour {
		t.Fatalf("unexpected ttl: %v", dur)
	}
}

func TestCreateAdminSession_TTLFromEnvIsUsed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ADMIN_SESSION_TTL_HOURS", "2")

	h := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, nil
	})

	_, expiry, err := h.createAdminSession(nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	dur := time.Until(expiry)
	if dur > 2*time.Hour || dur < (2*time.Hour-2*time.Minute) {
		t.Fatalf("unexpected ttl: %v", dur)
	}
}

func TestCreateAdminSession_InvalidTTLFromEnvFallsBackToDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ADMIN_SESSION_TTL_HOURS", "0")

	h := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, nil
	})

	_, expiry, err := h.createAdminSession(nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	dur := time.Until(expiry)
	if dur > 168*time.Hour || dur < 167*time.Hour {
		t.Fatalf("expected fallback ttl around 168h, got %v", dur)
	}
}

func TestLogoutRevokesCurrentSession(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, nil
	})

	token, _, err := h.createAdminSession(nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	req.Header.Set("Authorization", token)
	ctx.Request = req

	h.Logout(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	ok, err := h.isValidAdminSession(token)
	if err != nil {
		t.Fatalf("validate session: %v", err)
	}
	if ok {
		t.Fatalf("expected session to be revoked")
	}
}

func TestCreateAdminSessionCleansExpiredAndInvalidGenerationSessions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newTestHandler(t, nil)

	var generation int64
	if err := h.db.QueryRow("SELECT auth_generation FROM settings WHERE singleton_key = 1").Scan(&generation); err != nil {
		t.Fatalf("query auth generation: %v", err)
	}
	now := time.Now().Unix()
	for _, session := range []struct {
		hash       string
		generation int64
		expiresAt  int64
	}{
		{hash: "expired", generation: generation, expiresAt: now - 1},
		{hash: "stale-generation", generation: generation + 1, expiresAt: now + 3600},
		{hash: "current", generation: generation, expiresAt: now + 3600},
	} {
		if _, err := h.db.Exec(
			"INSERT INTO admin_sessions (token_hash, auth_generation, expires_at) VALUES (?, ?, ?)",
			session.hash,
			session.generation,
			session.expiresAt,
		); err != nil {
			t.Fatalf("seed session %q: %v", session.hash, err)
		}
	}

	if _, _, err := h.createAdminSession(nil); err != nil {
		t.Fatalf("create session: %v", err)
	}
	var count int
	if err := h.db.QueryRow("SELECT COUNT(*) FROM admin_sessions").Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected current seed plus new session, got %d rows", count)
	}
}

func TestAdminSessionExpiresAtBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newTestHandler(t, nil)
	var generation int64
	if err := h.db.QueryRow("SELECT auth_generation FROM settings WHERE singleton_key = 1").Scan(&generation); err != nil {
		t.Fatalf("query auth generation: %v", err)
	}
	token := "expires-now"
	if _, err := h.db.Exec(
		"INSERT INTO admin_sessions (token_hash, auth_generation, expires_at) VALUES (?, ?, ?)",
		hashSessionToken(token),
		generation,
		time.Now().Unix(),
	); err != nil {
		t.Fatalf("seed boundary session: %v", err)
	}
	valid, err := h.isValidAdminSession(token)
	if err != nil {
		t.Fatalf("validate boundary session: %v", err)
	}
	if valid {
		t.Fatalf("session remained valid at its expiration timestamp")
	}
}
