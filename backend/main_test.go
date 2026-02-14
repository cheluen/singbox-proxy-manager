package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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

	return distDir
}

func TestRegisterFrontendRoutesServesIndexWithRevalidateHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	distDir := writeTestFrontendDist(t)

	if err := registerFrontendRoutes(r, distDir, "1.2.2"); err != nil {
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
	if got := rec.Header().Get("X-App-Version"); got != "1.2.2" {
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
}

func TestRegisterFrontendRoutesServesAssetsWithImmutableCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	distDir := writeTestFrontendDist(t)

	if err := registerFrontendRoutes(r, distDir, "1.2.2"); err != nil {
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
	if got := rec.Header().Get("X-App-Version"); got != "1.2.2" {
		t.Fatalf("unexpected app version header: %s", got)
	}
}

func TestRegisterFrontendRoutesNoRouteBehavior(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	distDir := writeTestFrontendDist(t)

	if err := registerFrontendRoutes(r, distDir, "1.2.2"); err != nil {
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
