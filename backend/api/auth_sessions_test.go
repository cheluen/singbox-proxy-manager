package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"sb-proxy/backend/services"

	"github.com/gin-gonic/gin"
)

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
