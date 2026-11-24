package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sb-proxy/backend/api"
	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
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
	mark := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><title>SingBox Proxy Manager</title></svg>`
	if err := os.WriteFile(filepath.Join(distDir, "brand-mark.svg"), []byte(mark), 0o644); err != nil {
		t.Fatalf("write brand-mark.svg: %v", err)
	}

	return distDir
}

func TestConfiguredBindAddressDefaultsToExternalWildcard(t *testing.T) {
	t.Setenv("BIND_ADDRESS", "")
	if got := configuredBindAddress(); got != defaultBindAddress {
		t.Fatalf("configuredBindAddress()=%q want=%q", got, defaultBindAddress)
	}
}

func TestPublicHealthEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service := services.NewSingBoxService(t.TempDir())
	router := gin.New()
	registerPublicRoutes(router.Group("/api"), api.NewHandler(db, service), nil)

	assertHealth := func(code int, body string) {
		t.Helper()
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/health", nil))
		if response.Code != code || response.Body.String() != body {
			t.Fatalf("health response = %d %s, want %d %s", response.Code, response.Body.String(), code, body)
		}
	}
	// Uninitialized schema must report unhealthy so broken DB connections are surfaced.
	assertHealth(http.StatusServiceUnavailable, `{"status":"unhealthy"}`)
	if err := models.InitDB(db); err != nil {
		t.Fatalf("init db: %v", err)
	}
	assertHealth(http.StatusOK, `{"runtime_degraded":false,"runtime_running":false,"status":"ok"}`)
	service.MarkDegraded(io.ErrUnexpectedEOF)
	assertHealth(http.StatusOK, `{"runtime_degraded":true,"runtime_running":false,"status":"ok"}`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	assertHealth(http.StatusServiceUnavailable, `{"status":"unhealthy"}`)
}

func TestHealthcheckArgumentsAndAddress(t *testing.T) {
	t.Setenv("PORT", "")
	for _, args := range [][]string{{"--healthcheck"}, {"-healthcheck"}} {
		if !isHealthcheck(args) {
			t.Fatalf("did not recognize healthcheck arguments %v", args)
		}
	}
	if isHealthcheck(nil) || isHealthcheck([]string{"--unknown"}) {
		t.Fatal("normal startup was mistaken for a healthcheck")
	}
	for host, expected := range map[string]string{
		"":              "127.0.0.1",
		"0.0.0.0":       "127.0.0.1",
		"::":            "127.0.0.1",
		"*":             "127.0.0.1",
		"192.0.2.1":     "192.0.2.1",
		"::1":           "[::1]",
		"[2001:db8::1]": "[2001:db8::1]",
	} {
		t.Setenv("BIND_ADDRESS", host)
		if got, want := healthcheckURL(configuredBindAddress(), configuredPort()), "http://"+expected+":30000/api/health"; got != want {
			t.Errorf("healthcheck for bind %q = %q, want %q", host, got, want)
		}
	}
	gotDualStack := healthcheckURLs("::", "30000")
	wantDualStack := []string{"http://127.0.0.1:30000/api/health", "http://[::1]:30000/api/health"}
	if len(gotDualStack) != len(wantDualStack) || gotDualStack[0] != wantDualStack[0] || gotDualStack[1] != wantDualStack[1] {
		t.Fatalf("healthcheckURLs(::, 30000) = %v, want %v", gotDualStack, wantDualStack)
	}
	t.Setenv("PORT", "38080")
	if got := healthcheckURL("::1", configuredPort()); got != "http://[::1]:38080/api/health" {
		t.Fatalf("custom healthcheck port not used: %s", got)
	}
}

func TestProbeHealthRequiresDirectHTTP200(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusFound, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodGet || req.URL.Path != "/api/health" {
					t.Errorf("unexpected probe %s %s", req.Method, req.URL.Path)
					w.WriteHeader(http.StatusOK)
					return
				}
				if status == http.StatusFound {
					w.Header().Set("Location", "/redirected")
				}
				w.WriteHeader(status)
			}))
			defer server.Close()
			_, port, err := net.SplitHostPort(server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			if err := probeHealth("0.0.0.0", port); (err == nil) != (status == http.StatusOK) {
				t.Fatalf("probe for HTTP %d returned %v", status, err)
			}
		})
	}

	t.Run("ipv6_only_wildcard_fallback", func(t *testing.T) {
		ln, err := net.Listen("tcp6", "[::1]:0")
		if err != nil {
			t.Skipf("IPv6 loopback unavailable: %v", err)
		}
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method != http.MethodGet || req.URL.Path != "/api/health" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		server.Listener = ln
		server.Start()
		defer server.Close()
		_, port, err := net.SplitHostPort(ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		if err := probeHealth("::", port); err != nil {
			t.Fatalf("probeHealth(::, %s) failed against IPv6-only listener: %v", port, err)
		}
	})
}

func TestNodeIPCheckRouteUsesPostOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerNodeIPCheckRoute(router.Group("/api"), api.NewHandler(nil, nil))

	postRecorder := httptest.NewRecorder()
	router.ServeHTTP(postRecorder, httptest.NewRequest(http.MethodPost, "/api/nodes/not-an-id/check-ip", nil))
	if postRecorder.Code != http.StatusBadRequest {
		t.Fatalf("POST route was not registered: status=%d", postRecorder.Code)
	}

	getRecorder := httptest.NewRecorder()
	router.ServeHTTP(getRecorder, httptest.NewRequest(http.MethodGet, "/api/nodes/not-an-id/check-ip", nil))
	if getRecorder.Code != http.StatusNotFound {
		t.Fatalf("legacy GET route remains active: status=%d", getRecorder.Code)
	}
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

func TestFrontendNodesVirtualThresholdMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct{ value, expected string }{
		{"", "120"}, {"0", "0"}, {"1", "1"}, {"9999", "9999"}, {"-1", "120"}, {"invalid", "120"},
	} {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv("SBPM_NODES_VIRTUAL_THRESHOLD", test.value)
			dist := writeTestFrontendDist(t)
			index := filepath.Join(dist, "index.html")
			content, err := os.ReadFile(index)
			if err != nil {
				t.Fatal(err)
			}
			content = []byte(strings.Replace(string(content), "</head>", `<meta name="sbpm-nodes-virtual-threshold" content="50" /></head>`, 1))
			if err := os.WriteFile(index, content, 0o600); err != nil {
				t.Fatal(err)
			}
			router := gin.New()
			if err := registerFrontendRoutes(router, dist, "1.2.3"); err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
			body := response.Body.String()
			if response.Code != http.StatusOK || strings.Count(body, `name="sbpm-nodes-virtual-threshold"`) != 1 {
				t.Fatalf("阈值元数据未正确替换：status=%d body=%s", response.Code, body)
			}
			if !strings.Contains(body, `name="sbpm-nodes-virtual-threshold" content="`+test.expected+`"`) {
				t.Fatalf("阈值不符：value=%q expected=%s body=%s", test.value, test.expected, body)
			}
		})
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

func TestRegisterFrontendRoutesServesBrandMark(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	distDir := writeTestFrontendDist(t)
	if err := registerFrontendRoutes(r, distDir, "1.2.4"); err != nil {
		t.Fatalf("register frontend routes: %v", err)
	}

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(method, "/brand-mark.svg", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("unexpected status: %d", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "image/svg+xml") {
				t.Fatalf("brand mark must be SVG, not the SPA fallback: %q", got)
			}
			if got := rec.Header().Get("Cache-Control"); got != assetsCacheControlHeader {
				t.Fatalf("unexpected cache-control: %q", got)
			}
			if got := rec.Header().Get("X-App-Version"); got != "1.2.4" {
				t.Fatalf("unexpected app version: %q", got)
			}
			if rec.Header().Get("X-Frontend-Fingerprint") == "" {
				t.Fatal("frontend fingerprint must not be empty")
			}
			if method == http.MethodGet && !strings.Contains(rec.Body.String(), "<title>SingBox Proxy Manager</title>") {
				t.Fatalf("brand mark body is incorrect: %q", rec.Body.String())
			}
		})
	}
}

func TestRegisterFrontendRoutesMissingBrandMarkReturnsNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	distDir := writeTestFrontendDist(t)
	if err := os.Remove(filepath.Join(distDir, "brand-mark.svg")); err != nil {
		t.Fatalf("remove temporary brand mark: %v", err)
	}
	if err := registerFrontendRoutes(r, distDir, "1.2.4"); err != nil {
		t.Fatalf("register frontend routes: %v", err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/brand-mark.svg", nil))
	if rec.Code != http.StatusNotFound || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("missing brand mark must not fall back to index.html: status=%d body=%q", rec.Code, rec.Body.String())
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

func TestLoadRuntimeEnvironmentRespectsExistingVariables(t *testing.T) {
	t.Setenv("TZ", "UTC")
	previousLocal := time.Local
	t.Cleanup(func() { time.Local = previousLocal })
	t.Setenv("TEST_EXISTING_KEY", "from-env")
	t.Setenv("TEST_NEW_KEY", "")
	if err := os.Unsetenv("TEST_NEW_KEY"); err != nil {
		t.Fatalf("unset TEST_NEW_KEY: %v", err)
	}

	t.Chdir(t.TempDir())
	envPath := ".env"
	content := "TEST_EXISTING_KEY=from-dotenv\nTEST_NEW_KEY=loaded-from-dotenv\n"
	if err := os.WriteFile(envPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	loadRuntimeEnvironment()

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
