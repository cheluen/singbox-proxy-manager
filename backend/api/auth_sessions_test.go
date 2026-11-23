package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"sb-proxy/backend/services"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"modernc.org/sqlite"
)

func createTestAdminSession(t *testing.T, handler *Handler) (string, time.Time, error) {
	t.Helper()
	ctx := context.Background()
	var generation int64
	if err := handler.db.QueryRowContext(ctx, "SELECT auth_generation FROM settings WHERE singleton_key = 1").Scan(&generation); err != nil {
		t.Fatalf("query auth generation: %v", err)
	}
	return createAdminSessionWithGeneration(ctx, handler.db, generation, "", "")
}

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

	_, expiry, err := createTestAdminSession(t, h)
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

	_, expiry, err := createTestAdminSession(t, h)
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

	_, expiry, err := createTestAdminSession(t, h)
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

	token, _, err := createTestAdminSession(t, h)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	router := gin.New()
	router.POST("/api/logout", h.AuthMiddleware(), func(ctx *gin.Context) {
		ctx.Request.Header.Del("Authorization")
	}, h.Logout)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	hash, err := h.verifiedAdminSessionHash(context.Background(), token)
	if err != nil {
		t.Fatalf("validate session: %v", err)
	}
	if hash != "" {
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

	if _, _, err := createTestAdminSession(t, h); err != nil {
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
	hash, err := h.verifiedAdminSessionHash(context.Background(), token)
	if err != nil {
		t.Fatalf("validate boundary session: %v", err)
	}
	if hash != "" {
		t.Fatalf("session remained valid at its expiration timestamp")
	}
}

func TestConcurrentPasswordChangeRejectsStaleLoginAndKeepsNewSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, nil)
	handler.db.SetMaxOpenConns(1)
	handler.loginLimiter = newLoginRateLimiter(time.Minute, 10, time.Minute, 2)
	const oldPassword = "old-admin-fixture"
	const newPassword = "new-admin-fixture"
	oldHash, err := bcrypt.GenerateFromPassword([]byte(oldPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.db.Exec(`
		UPDATE settings SET admin_password = ?, auth_generation = 7 WHERE singleton_key = 1
	`, string(oldHash)); err != nil {
		t.Fatal(err)
	}

	oldComparisonStarted := make(chan struct{})
	releaseOldComparison := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseOldComparison) })
	handler.comparePassword = func(hash, candidate []byte) error {
		if string(candidate) == oldPassword {
			close(oldComparisonStarted)
			<-releaseOldComparison
		}
		return bcrypt.CompareHashAndPassword(hash, candidate)
	}
	oldResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		oldResult <- postJSON(t, handler.Login, http.MethodPost, "/api/login", map[string]string{"password": oldPassword}, nil)
	}()
	select {
	case <-oldComparisonStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("old login did not reach password comparison")
	}

	update := postJSON(t, handler.UpdateSettings, http.MethodPut, "/api/settings", map[string]string{"admin_password": newPassword}, nil)
	if update.Code != http.StatusOK {
		t.Fatalf("password update failed: %d %s", update.Code, update.Body.String())
	}
	newResult := postJSON(t, handler.Login, http.MethodPost, "/api/login", map[string]string{"password": newPassword}, nil)
	if newResult.Code != http.StatusOK {
		t.Fatalf("new login failed: %d %s", newResult.Code, newResult.Body.String())
	}
	var session struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(newResult.Body.Bytes(), &session); err != nil || session.Token == "" {
		t.Fatalf("new login did not issue a session: %v", err)
	}

	releaseOnce.Do(func() { close(releaseOldComparison) })
	select {
	case response := <-oldResult:
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("stale login returned %d, want 401", response.Code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale login did not finish")
	}
	if hash, err := handler.verifiedAdminSessionHash(context.Background(), session.Token); err != nil || hash == "" {
		t.Fatalf("stale login invalidated the new session: %v", err)
	}
	var count int
	if err := handler.db.QueryRow("SELECT COUNT(*) FROM admin_sessions").Scan(&count); err != nil || count != 1 {
		t.Fatalf("expected only the new session, got %d: %v", count, err)
	}
}

func TestCreateAdminSessionPreservesDatabaseError(t *testing.T) {
	handler := newTestHandler(t, nil)
	if _, err := handler.db.Exec("DROP TABLE admin_sessions"); err != nil {
		t.Fatal(err)
	}
	_, _, err := createTestAdminSession(t, handler)
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("expected original SQLite error, got %v", err)
	}
}
