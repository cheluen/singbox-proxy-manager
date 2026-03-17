package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	frontendassets "sb-proxy/frontend"

	"github.com/gin-gonic/gin"
)

func writeTestFrontendDist(t *testing.T) string {
	t.Helper()

	distDir := filepath.Join(t.TempDir(), "dist")
	assetsDir := filepath.Join(distDir, "assets")

	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}

	indexHTML := "<!doctype html><html><head><title>test</title></head><body><div id=\"root\"></div></body></html>"
	if err := os.WriteFile(filepath.Join(distDir, "index.html"), []byte(indexHTML), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assetsDir, "app-abc123.js"), []byte("console.log('ok')"), 0o644); err != nil {
		t.Fatalf("write app js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(distDir, "logo.svg"), []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"), 0o644); err != nil {
		t.Fatalf("write logo.svg: %v", err)
	}

	return distDir
}

func TestRegisterFrontendRoutesServesIndexWithRevalidateHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	distDir := writeTestFrontendDist(t)

	if err := registerFrontendRoutes(r, distDir, "1.2.4"); err != nil {
		t.Fatalf("register frontend routes: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != indexCacheControlHeader {
		t.Fatalf("unexpected cache-control: %s", got)
	}
	if got := rec.Header().Get("X-App-Version"); got != "1.2.4" {
		t.Fatalf("unexpected app version header: %s", got)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("etag header should not be empty")
	}
	fingerprint := rec.Header().Get("X-Frontend-Fingerprint")
	if fingerprint == "" {
		t.Fatalf("frontend fingerprint header should not be empty")
	}

	conditionalReq := httptest.NewRequest(http.MethodGet, "/", nil)
	conditionalReq.Header.Set("If-None-Match", etag)
	conditionalRec := httptest.NewRecorder()
	r.ServeHTTP(conditionalRec, conditionalReq)
	if conditionalRec.Code != http.StatusNotModified {
		t.Fatalf("expected 304 for matching etag, got %d", conditionalRec.Code)
	}

	weakReq := httptest.NewRequest(http.MethodGet, "/", nil)
	weakReq.Header.Set("If-None-Match", "W/"+etag)
	weakRec := httptest.NewRecorder()
	r.ServeHTTP(weakRec, weakReq)
	if weakRec.Code != http.StatusNotModified {
		t.Fatalf("expected 304 for weak etag match, got %d", weakRec.Code)
	}

	multiReq := httptest.NewRequest(http.MethodGet, "/", nil)
	multiReq.Header.Set("If-None-Match", `"other-tag", W/`+etag)
	multiRec := httptest.NewRecorder()
	r.ServeHTTP(multiRec, multiReq)
	if multiRec.Code != http.StatusNotModified {
		t.Fatalf("expected 304 for multi-etag match, got %d", multiRec.Code)
	}

	starReq := httptest.NewRequest(http.MethodGet, "/", nil)
	starReq.Header.Set("If-None-Match", "*")
	starRec := httptest.NewRecorder()
	r.ServeHTTP(starRec, starReq)
	if starRec.Code != http.StatusNotModified {
		t.Fatalf("expected 304 for wildcard if-none-match, got %d", starRec.Code)
	}
}

func TestRegisterFrontendRoutes_InjectsBatchCheckIPConcurrencyMeta(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	distDir := writeTestFrontendDist(t)

	t.Setenv("SBPM_BATCH_CHECK_IP_CONCURRENCY", "12")
	if err := registerFrontendRoutes(r, distDir, "1.2.4"); err != nil {
		t.Fatalf("register frontend routes: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `name="sbpm-batch-check-ip-concurrency"`) {
		t.Fatalf("missing batch check ip concurrency meta: %s", body)
	}
	if !strings.Contains(body, `content="12"`) {
		t.Fatalf("missing batch check ip concurrency value: %s", body)
	}
}

func TestRegisterFrontendRoutesServesAssetsWithImmutableCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	distDir := writeTestFrontendDist(t)

	if err := registerFrontendRoutes(r, distDir, "1.2.4"); err != nil {
		t.Fatalf("register frontend routes: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/assets/app-abc123.js", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != assetsCacheControlHeader {
		t.Fatalf("unexpected cache-control: %s", got)
	}
	if got := rec.Header().Get("X-App-Version"); got != "1.2.4" {
		t.Fatalf("unexpected app version header: %s", got)
	}
}

func TestRegisterFrontendRoutesNoRouteBehavior(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	distDir := writeTestFrontendDist(t)

	if err := registerFrontendRoutes(r, distDir, "1.2.4"); err != nil {
		t.Fatalf("register frontend routes: %v", err)
	}

	// SPA routes fall back to index.html
	spaReq := httptest.NewRequest(http.MethodGet, "/dashboard/nodes", nil)
	spaRec := httptest.NewRecorder()
	r.ServeHTTP(spaRec, spaReq)
	if spaRec.Code != http.StatusOK {
		t.Fatalf("unexpected status for spa route: %d", spaRec.Code)
	}

	// Unknown API routes must return JSON 404 instead of HTML.
	apiReq := httptest.NewRequest(http.MethodGet, "/api/not-found", nil)
	apiRec := httptest.NewRecorder()
	r.ServeHTTP(apiRec, apiReq)
	if apiRec.Code != http.StatusNotFound {
		t.Fatalf("unexpected status for api route: %d", apiRec.Code)
	}

	var body map[string]string
	if err := json.Unmarshal(apiRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body["error"] != "not found" {
		t.Fatalf("unexpected api error response: %+v", body)
	}
}

func TestRegisterFrontendRoutesServesLogoFile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	distDir := writeTestFrontendDist(t)

	if err := registerFrontendRoutes(r, distDir, "1.2.4"); err != nil {
		t.Fatalf("register frontend routes: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/logo.svg", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != assetsCacheControlHeader {
		t.Fatalf("unexpected cache-control: %s", got)
	}
}

func TestAPISecurityHeadersMiddlewareAddsHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(apiSecurityHeadersMiddleware())
	r.GET("/ping", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("missing security header X-Frame-Options: %q", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got == "" {
		t.Fatalf("missing security header Content-Security-Policy")
	}
}

func TestAPIRequestBodyLimitMiddlewareRejectsLargePayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(apiRequestBodyLimitMiddleware(8))
	r.POST("/api/echo", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.String(http.StatusOK, string(body))
	})

	req := httptest.NewRequest(http.MethodPost, "/api/echo", strings.NewReader("0123456789"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
}

func TestLoadDotEnvFilesRespectsExistingVariables(t *testing.T) {
	t.Setenv("TEST_EXISTING_KEY", "from-env")
	if err := os.Unsetenv("TEST_NEW_KEY"); err != nil {
		t.Fatalf("unset TEST_NEW_KEY: %v", err)
	}

	envPath := filepath.Join(t.TempDir(), ".env")
	content := "TEST_EXISTING_KEY=from-dotenv\nTEST_NEW_KEY=loaded-from-dotenv\n"
	if err := os.WriteFile(envPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	loaded, err := loadDotEnvFiles([]string{envPath})
	if err != nil {
		t.Fatalf("load .env: %v", err)
	}
	if len(loaded) != 1 || loaded[0] != envPath {
		t.Fatalf("unexpected loaded paths: %+v", loaded)
	}

	if got := os.Getenv("TEST_EXISTING_KEY"); got != "from-env" {
		t.Fatalf("existing env should take precedence, got %q", got)
	}
	if got := os.Getenv("TEST_NEW_KEY"); got != "loaded-from-dotenv" {
		t.Fatalf("new env should be loaded from .env, got %q", got)
	}
}

func TestApplyTimezoneFromEnvDefault(t *testing.T) {
	previousLocal := time.Local
	t.Cleanup(func() {
		time.Local = previousLocal
	})

	t.Setenv("TZ", "")
	locationName := applyTimezoneFromEnv()

	if locationName != defaultTimezoneLocationName {
		t.Fatalf("expected fallback timezone %s, got %s", defaultTimezoneLocationName, locationName)
	}
	if got := os.Getenv("TZ"); got != defaultTimezoneLocationName {
		t.Fatalf("expected canonical TZ=%s, got %s", defaultTimezoneLocationName, got)
	}
}

func TestResolveTimezoneParsesUTCOffset(t *testing.T) {
	loc, canonical, err := resolveTimezone("UTC-05:30")
	if err != nil {
		t.Fatalf("resolve timezone: %v", err)
	}
	if canonical != "UTC-05:30" {
		t.Fatalf("unexpected canonical timezone: %s", canonical)
	}
	if _, offset := time.Now().In(loc).Zone(); offset != -(5*3600 + 30*60) {
		t.Fatalf("unexpected offset: %d", offset)
	}
}

func TestRegisterFrontendRoutesFallsBackToEmbeddedAssets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	missingDistDir := filepath.Join(t.TempDir(), "missing-dist")
	if !frontendassets.HasEmbeddedAssets {
		if err := registerFrontendRoutes(r, missingDistDir, "1.2.4"); err == nil {
			t.Fatalf("expected error when embedded assets are disabled")
		}
		return
	}

	if err := registerFrontendRoutes(r, missingDistDir, "1.2.4"); err != nil {
		t.Fatalf("register frontend routes with embedded fallback: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<div id=\"root\">") {
		t.Fatalf("embedded index.html seems invalid")
	}
}

func TestIfNoneMatchMatchesCurrentETag(t *testing.T) {
	currentETag := `"sbpm-1234"`
	cases := []struct {
		name        string
		ifNoneMatch string
		want        bool
	}{
		{
			name:        "exact",
			ifNoneMatch: `"sbpm-1234"`,
			want:        true,
		},
		{
			name:        "weak",
			ifNoneMatch: `W/"sbpm-1234"`,
			want:        true,
		},
		{
			name:        "list",
			ifNoneMatch: `"other", W/"sbpm-1234"`,
			want:        true,
		},
		{
			name:        "wildcard",
			ifNoneMatch: `*`,
			want:        true,
		},
		{
			name:        "wildcard with spaces and list",
			ifNoneMatch: `* , "other"`,
			want:        true,
		},
		{
			name:        "invalid wildcard token",
			ifNoneMatch: `*foo`,
			want:        false,
		},
		{
			name:        "invalid",
			ifNoneMatch: `not-an-etag`,
			want:        false,
		},
		{
			name:        "invalid then valid",
			ifNoneMatch: `not-an-etag, W/"sbpm-1234"`,
			want:        true,
		},
		{
			name:        "no-match",
			ifNoneMatch: `"other"`,
			want:        false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := ifNoneMatchMatchesCurrentETag(tc.ifNoneMatch, currentETag)
			if got != tc.want {
				t.Fatalf("unexpected result for %q: got %v want %v", tc.ifNoneMatch, got, tc.want)
			}
		})
	}
}
