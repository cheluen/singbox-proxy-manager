package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

	fakeBinary := filepath.Join(t.TempDir(), "fake-sing-box")
	if err := os.WriteFile(fakeBinary, []byte("#!/bin/sh\nsleep 300\n"), 0o755); err != nil {
		t.Fatalf("write fake sing-box binary: %v", err)
	}
	t.Setenv("SINGBOX_BINARY", fakeBinary)
	svc := services.NewSingBoxService(t.TempDir())
	h := NewHandler(db, svc)
	h.checkProxyIP = checker
	t.Cleanup(func() {
		_ = svc.Stop()
		_ = db.Close()
	})
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

func TestUpdateNodeRemarkUpdatesRemark(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, fmt.Errorf("not used")
	})

	nodeID := insertTestNode(t, handler.db)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{gin.Param{Key: "id", Value: strconv.Itoa(nodeID)}}

	req, _ := http.NewRequest(
		http.MethodPut,
		"/api/nodes/"+strconv.Itoa(nodeID)+"/remark",
		strings.NewReader(`{"remark":"hello"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	handler.UpdateNodeRemark(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", rec.Code)
	}

	var remark string
	if err := handler.db.QueryRow("SELECT remark FROM proxy_nodes WHERE id = ?", nodeID).Scan(&remark); err != nil {
		t.Fatalf("query node: %v", err)
	}
	if remark != "hello" {
		t.Fatalf("expected remark to be updated, got %q", remark)
	}
}

func TestUpdateNodeAppliesInboundPortAndTCPReuseFlag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, fmt.Errorf("not used")
	})
	nodeID := insertTestNode(t, handler.db)

	payload := map[string]interface{}{
		"name":              "node1-updated",
		"remark":            "remark",
		"type":              "ss",
		"config":            `{"server":"example.com","server_port":443,"method":"aes-128-gcm","password":"p"}`,
		"inbound_port":      33055,
		"username":          "newuser",
		"password":          "newpass",
		"enabled":           true,
		"tcp_reuse_enabled": false,
	}
	body, _ := json.Marshal(payload)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{gin.Param{Key: "id", Value: strconv.Itoa(nodeID)}}
	req, _ := http.NewRequest(http.MethodPut, "/api/nodes/"+strconv.Itoa(nodeID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	handler.UpdateNode(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d body=%s", rec.Code, rec.Body.String())
	}

	var inboundPort int
	var username string
	var password string
	var tcpReuseEnabled bool
	if err := handler.db.QueryRow(
		"SELECT inbound_port, username, password, tcp_reuse_enabled FROM proxy_nodes WHERE id = ?",
		nodeID,
	).Scan(&inboundPort, &username, &password, &tcpReuseEnabled); err != nil {
		t.Fatalf("query node: %v", err)
	}

	if inboundPort != 33055 {
		t.Fatalf("expected inbound_port=33055, got %d", inboundPort)
	}
	if username != "newuser" || password != "newpass" {
		t.Fatalf("expected updated auth, got %s/%s", username, password)
	}
	if tcpReuseEnabled {
		t.Fatalf("expected tcp_reuse_enabled=false after update")
	}
}

func TestUpdateNodeInboundPortZeroUsesStartPortAndSortOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, fmt.Errorf("not used")
	})
	nodeID := insertTestNode(t, handler.db)

	if _, err := handler.db.Exec("UPDATE settings SET start_port = 41000"); err != nil {
		t.Fatalf("set start_port: %v", err)
	}

	payload := map[string]interface{}{
		"name":         "node1-updated",
		"remark":       "",
		"type":         "ss",
		"config":       `{"server":"example.com","server_port":443,"method":"aes-128-gcm","password":"p"}`,
		"inbound_port": 0,
		"username":     "user",
		"password":     "pass",
		"enabled":      true,
	}
	body, _ := json.Marshal(payload)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{gin.Param{Key: "id", Value: strconv.Itoa(nodeID)}}
	req, _ := http.NewRequest(http.MethodPut, "/api/nodes/"+strconv.Itoa(nodeID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	handler.UpdateNode(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d body=%s", rec.Code, rec.Body.String())
	}

	var inboundPort int
	if err := handler.db.QueryRow("SELECT inbound_port FROM proxy_nodes WHERE id = ?", nodeID).Scan(&inboundPort); err != nil {
		t.Fatalf("query inbound_port: %v", err)
	}
	if inboundPort != 41000 {
		t.Fatalf("expected inbound_port=41000, got %d", inboundPort)
	}
}

func TestCreateNodeRejectsUsernameWithPlus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, fmt.Errorf("not used")
	})

	payload := map[string]interface{}{
		"name":              "node-with-plus",
		"remark":            "",
		"type":              "ss",
		"config":            `{"server":"example.com","server_port":443,"method":"aes-128-gcm","password":"p"}`,
		"inbound_port":      32001,
		"username":          "bad+name",
		"password":          "pass",
		"enabled":           true,
		"tcp_reuse_enabled": true,
	}
	body, _ := json.Marshal(payload)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req, _ := http.NewRequest(http.MethodPost, "/api/nodes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	handler.CreateNode(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBatchSetAuthRejectsUsernameWithPlus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, fmt.Errorf("not used")
	})
	nodeID := insertTestNode(t, handler.db)

	payload := map[string]interface{}{
		"node_ids": []int{nodeID},
		"username": "bad+name",
		"password": "newpass",
	}
	body, _ := json.Marshal(payload)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req, _ := http.NewRequest(http.MethodPost, "/api/nodes/batch-auth", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	handler.BatchSetAuth(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}

	var username string
	if err := handler.db.QueryRow("SELECT username FROM proxy_nodes WHERE id = ?", nodeID).Scan(&username); err != nil {
		t.Fatalf("query username: %v", err)
	}
	if username != "user" {
		t.Fatalf("username should remain unchanged, got %q", username)
	}
}
