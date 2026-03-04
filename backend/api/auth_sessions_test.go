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
