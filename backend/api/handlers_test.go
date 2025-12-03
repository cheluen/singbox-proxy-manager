package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"sb-proxy/backend/models"
	"sb-proxy/backend/services"

	"github.com/gin-gonic/gin"
	_ "modernc.org/sqlite"
)

// newTestHandler builds a handler with in-memory sqlite and stubbed proxy checker.
func newTestHandler(t *testing.T, checker func(string, string, string) (*services.IPInfo, error)) *Handler {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := models.InitDB(db); err != nil {
		t.Fatalf("init db: %v", err)
	}

	h := NewHandler(db, &services.SingBoxService{})
	h.checkProxyIP = checker
	return h
}

func insertTestNode(t *testing.T, db *sql.DB) int {
	t.Helper()
	res, err := db.Exec(`
		INSERT INTO proxy_nodes (name, type, config, inbound_port, username, password, sort_order, latency, enabled)
		VALUES ('node1', 'ss', '{"server":"example.com","server_port":443,"method":"aes-128-gcm","password":"p"}',
		        30001, 'user', 'pass', 0, 0, 1)
	`)
	if err != nil {
		t.Fatalf("insert node: %v", err)
	}
	id, _ := res.LastInsertId()
	return int(id)
}

func TestCheckNodeIPSuccessUpdatesNode(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return &services.IPInfo{
			IP:          "1.2.3.4",
			Country:     "Testland",
			CountryCode: "TL",
			City:        "Test City",
			Region:      "Test Region",
			Location:    "Test City, Testland",
			Latency:     123,
			Transport:   "http",
		}, nil
	})

	nodeID := insertTestNode(t, handler.db)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{gin.Param{Key: "id", Value: strconv.Itoa(nodeID)}}
	ctx.Request, _ = http.NewRequest(http.MethodGet, "/api/nodes/"+strconv.Itoa(nodeID)+"/check-ip", nil)

	handler.CheckNodeIP(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", rec.Code)
	}
	var capturedBody map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &capturedBody); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if capturedBody["ip"] != "1.2.3.4" {
		t.Fatalf("unexpected response ip: %v", capturedBody["ip"])
	}

	var ip, location, countryCode string
	var latency int
	if err := handler.db.QueryRow(`
		SELECT node_ip, location, country_code, latency
		FROM proxy_nodes WHERE id = ?
	`, nodeID).Scan(&ip, &location, &countryCode, &latency); err != nil {
		t.Fatalf("query node: %v", err)
	}
	if ip != "1.2.3.4" || location == "" || countryCode != "TL" || latency != 123 {
		t.Fatalf("unexpected node values: ip=%s location=%s country=%s latency=%d", ip, location, countryCode, latency)
	}
}

func TestCheckNodeIPFailureClearsStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, fmt.Errorf("dial failed")
	})

	nodeID := insertTestNode(t, handler.db)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{gin.Param{Key: "id", Value: strconv.Itoa(nodeID)}}
	ctx.Request, _ = http.NewRequest(http.MethodGet, "/api/nodes/"+strconv.Itoa(nodeID)+"/check-ip", nil)

	// Prime node with old values to ensure they get cleared.
	if _, err := handler.db.Exec(`
		UPDATE proxy_nodes SET node_ip='8.8.8.8', location='Old', country_code='US', latency=50 WHERE id=?
	`, nodeID); err != nil {
		t.Fatalf("seed node: %v", err)
	}

	handler.CheckNodeIP(ctx)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status %d", rec.Code)
	}
	var ip, location, countryCode string
	var latency int
	if err := handler.db.QueryRow(`
		SELECT node_ip, location, country_code, latency
		FROM proxy_nodes WHERE id = ?
	`, nodeID).Scan(&ip, &location, &countryCode, &latency); err != nil {
		t.Fatalf("query node: %v", err)
	}
	if ip != "" || location != "" || countryCode != "" || latency != 0 {
		t.Fatalf("expected cleared status, got ip=%s location=%s country=%s latency=%d", ip, location, countryCode, latency)
	}
}

func TestCheckNodeIPRejectsSocksFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return &services.IPInfo{
			IP:        "9.9.9.9",
			Location:  "Fallback",
			Latency:   40,
			Transport: "socks5",
			HTTPError: "http unavailable",
		}, nil
	})

	nodeID := insertTestNode(t, handler.db)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{gin.Param{Key: "id", Value: strconv.Itoa(nodeID)}}
	ctx.Request, _ = http.NewRequest(http.MethodGet, "/api/nodes/"+strconv.Itoa(nodeID)+"/check-ip", nil)

	handler.CheckNodeIP(ctx)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected bad gateway, got %d", rec.Code)
	}
	var ip, location, countryCode string
	var latency int
	if err := handler.db.QueryRow(`
		SELECT node_ip, location, country_code, latency
		FROM proxy_nodes WHERE id = ?
	`, nodeID).Scan(&ip, &location, &countryCode, &latency); err != nil {
		t.Fatalf("query node: %v", err)
	}
	if ip != "" || location != "" || countryCode != "" || latency != 0 {
		t.Fatalf("expected cleared status after fallback, got ip=%s location=%s country=%s latency=%d", ip, location, countryCode, latency)
	}
}
