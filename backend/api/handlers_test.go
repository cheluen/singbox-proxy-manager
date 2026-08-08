package api

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func useRealSingBoxForChecks(t *testing.T) {
	t.Helper()
	realBinary := os.Getenv("SINGBOX_TEST_BINARY")
	if realBinary == "" {
		t.Skip("SINGBOX_TEST_BINARY not set")
	}

	wrapper := filepath.Join(t.TempDir(), "sing-box-check-wrapper")
	script := `#!/bin/sh
if [ "$1" = "check" ]; then
  exec "$REAL_SINGBOX_BINARY" "$@"
fi
sleep 300
`
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatalf("write sing-box wrapper: %v", err)
	}
	t.Setenv("REAL_SINGBOX_BINARY", realBinary)
	t.Setenv("SINGBOX_BINARY", wrapper)
}

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

	// The fake binary must answer `check` quickly (used by config validation)
	// and block on `run` like the real kernel does.
	fakeBinary := filepath.Join(t.TempDir(), "fake-sing-box")
	if err := os.WriteFile(fakeBinary, []byte("#!/bin/sh\nif [ \"$1\" = \"check\" ]; then exit 0; fi\nsleep 300\n"), 0o755); err != nil {
		t.Fatalf("write fake sing-box binary: %v", err)
	}
	t.Setenv("SINGBOX_BINARY", fakeBinary)
	t.Setenv("SBPM_SKIP_PORT_AVAILABILITY_CHECK", "1")
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

func insertTestNodeWithPortAndOrder(t *testing.T, db *sql.DB, name string, inboundPort int, sortOrder int) int {
	t.Helper()
	res, err := db.Exec(`
		INSERT INTO proxy_nodes (name, type, config, inbound_port, username, password, sort_order, latency, enabled)
		VALUES (?, 'ss', '{"server":"example.com","server_port":443,"method":"aes-128-gcm","password":"p"}', ?, 'user', 'pass', ?, 0, 1)
	`, name, inboundPort, sortOrder)
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

	if rec.Code != http.StatusBadGateway {
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

func TestCheckNodeIPAcceptsSocksFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return &services.IPInfo{
			IP:          "9.9.9.9",
			Location:    "Fallback",
			CountryCode: "XX",
			Latency:     40,
			Transport:   "socks5",
			HTTPError:   "http unavailable",
		}, nil
	})

	nodeID := insertTestNode(t, handler.db)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{gin.Param{Key: "id", Value: strconv.Itoa(nodeID)}}
	ctx.Request, _ = http.NewRequest(http.MethodGet, "/api/nodes/"+strconv.Itoa(nodeID)+"/check-ip", nil)

	handler.CheckNodeIP(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d", rec.Code)
	}
	var ip, location, countryCode string
	var latency int
	if err := handler.db.QueryRow(`
		SELECT node_ip, location, country_code, latency
		FROM proxy_nodes WHERE id = ?
	`, nodeID).Scan(&ip, &location, &countryCode, &latency); err != nil {
		t.Fatalf("query node: %v", err)
	}
	if ip != "9.9.9.9" || location != "Fallback" || countryCode != "XX" || latency != 40 {
		t.Fatalf("expected status updated after fallback, got ip=%s location=%s country=%s latency=%d", ip, location, countryCode, latency)
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

	if _, err := handler.db.Exec("UPDATE settings SET preserve_inbound_ports = 1"); err != nil {
		t.Fatalf("enable preserve_inbound_ports: %v", err)
	}

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

func TestUpdateNodeWhenPreserveDisabledForcesInboundPortToOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, fmt.Errorf("not used")
	})
	nodeID := insertTestNode(t, handler.db)

	// preserve_inbound_ports is disabled by default.
	payload := map[string]interface{}{
		"name":         "node1-updated",
		"remark":       "remark",
		"type":         "ss",
		"config":       `{"server":"example.com","server_port":443,"method":"aes-128-gcm","password":"p"}`,
		"inbound_port": 33055,
		"username":     "newuser",
		"password":     "newpass",
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
	if inboundPort != 30001 {
		t.Fatalf("expected inbound_port to remain 30001 when preserve disabled, got %d", inboundPort)
	}
}

func TestUpdateNodeInboundPortZeroUsesStartPortWhenAvailable(t *testing.T) {
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

func TestCreateNodeAutoAssignSkipsUsedInboundPorts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, fmt.Errorf("not used")
	})

	if _, err := handler.db.Exec("UPDATE settings SET start_port = 30001, preserve_inbound_ports = 1"); err != nil {
		t.Fatalf("set start_port: %v", err)
	}
	insertTestNodeWithPortAndOrder(t, handler.db, "node-a", 30001, 0)
	insertTestNodeWithPortAndOrder(t, handler.db, "node-b", 30005, 1)

	payload := map[string]interface{}{
		"name":         "node-new",
		"remark":       "",
		"type":         "ss",
		"config":       `{"server":"example.com","server_port":443,"method":"aes-128-gcm","password":"p"}`,
		"inbound_port": 0,
		"username":     "",
		"password":     "",
		"enabled":      true,
	}
	body, _ := json.Marshal(payload)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req, _ := http.NewRequest(http.MethodPost, "/api/nodes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	handler.CreateNode(ctx)
	if rec.Code != http.StatusCreated {
		t.Fatalf("unexpected status %d body=%s", rec.Code, rec.Body.String())
	}

	var inboundPort int
	if err := handler.db.QueryRow("SELECT inbound_port FROM proxy_nodes WHERE name = ?", "node-new").Scan(&inboundPort); err != nil {
		t.Fatalf("query inbound_port: %v", err)
	}
	if inboundPort != 30002 {
		t.Fatalf("expected inbound_port=30002, got %d", inboundPort)
	}
}

func TestBatchImportNodesAutoAssignSkipsUsedInboundPorts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, fmt.Errorf("not used")
	})

	if _, err := handler.db.Exec("UPDATE settings SET start_port = 30001, preserve_inbound_ports = 1"); err != nil {
		t.Fatalf("set settings: %v", err)
	}
	insertTestNodeWithPortAndOrder(t, handler.db, "node-a", 30001, 0)
	insertTestNodeWithPortAndOrder(t, handler.db, "node-b", 30005, 1)

	payload := map[string]interface{}{
		"links": []string{
			"trojan://pass123@example.com:443?type=ws&host=example.com&path=%2Fws&sni=sni.example.com&alpn=h2,http/1.1&insecure=1&fp=firefox#Batch-1",
			"trojan://pass123@example.com:443?type=ws&host=example.com&path=%2Fws&sni=sni.example.com&alpn=h2,http/1.1&insecure=1&fp=firefox#Batch-2",
			"trojan://pass123@example.com:443?type=ws&host=example.com&path=%2Fws&sni=sni.example.com&alpn=h2,http/1.1&insecure=1&fp=firefox#Batch-3",
			"trojan://pass123@example.com:443?type=ws&host=example.com&path=%2Fws&sni=sni.example.com&alpn=h2,http/1.1&insecure=1&fp=firefox#Batch-4",
			"trojan://pass123@example.com:443?type=ws&host=example.com&path=%2Fws&sni=sni.example.com&alpn=h2,http/1.1&insecure=1&fp=firefox#Batch-5",
		},
		"enabled": true,
	}
	body, _ := json.Marshal(payload)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req, _ := http.NewRequest(http.MethodPost, "/api/nodes/batch-import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	handler.BatchImportNodes(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d body=%s", rec.Code, rec.Body.String())
	}

	rows, err := handler.db.Query("SELECT inbound_port FROM proxy_nodes WHERE name LIKE 'Batch-%' ORDER BY sort_order")
	if err != nil {
		t.Fatalf("query imported ports: %v", err)
	}
	defer rows.Close()

	var got []int
	for rows.Next() {
		var inboundPort int
		if err := rows.Scan(&inboundPort); err != nil {
			t.Fatalf("scan imported port: %v", err)
		}
		got = append(got, inboundPort)
	}
	want := []int{30002, 30003, 30004, 30006, 30007}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("unexpected imported ports: got=%v want=%v", got, want)
	}
}

func TestCreateNodeWireGuardPersistsConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, fmt.Errorf("not used")
	})

	payload := map[string]interface{}{
		"name":    "warp-node",
		"remark":  "cf",
		"type":    "wireguard",
		"enabled": true,
		"config": `{
			"server":"engage.cloudflareclient.com",
			"server_port":2408,
			"local_address":["172.16.0.2/32","2606:4700:110:8765::2/128"],
			"private_key":"private-key",
			"peer_public_key":"peer-public-key",
			"allowed_ips":["0.0.0.0/0","::/0"],
			"reserved":[162,104,222],
			"domain_resolver":"local",
			"domain_resolver_strategy":"prefer_ipv4",
			"udp_fragment":true,
			"connect_timeout":"5s"
		}`,
	}
	body, _ := json.Marshal(payload)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req, _ := http.NewRequest(http.MethodPost, "/api/nodes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	handler.CreateNode(ctx)
	if rec.Code != http.StatusCreated {
		t.Fatalf("unexpected status %d body=%s", rec.Code, rec.Body.String())
	}

	var (
		nodeType   string
		configJSON string
	)
	if err := handler.db.QueryRow(
		"SELECT type, config FROM proxy_nodes WHERE name = ?",
		"warp-node",
	).Scan(&nodeType, &configJSON); err != nil {
		t.Fatalf("query node: %v", err)
	}
	if nodeType != "wireguard" {
		t.Fatalf("expected wireguard type, got %q", nodeType)
	}

	var cfg models.WireGuardConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if cfg.Server != "engage.cloudflareclient.com" || cfg.ServerPort != 2408 {
		t.Fatalf("unexpected endpoint: %+v", cfg)
	}
	if len(cfg.LocalAddress) != 2 || cfg.LocalAddress[1] != "2606:4700:110:8765::2/128" {
		t.Fatalf("unexpected local_address: %+v", cfg.LocalAddress)
	}
	if cfg.PeerPublicKey != "peer-public-key" || cfg.Detour != "" {
		t.Fatalf("unexpected wireguard config: %+v", cfg)
	}
	if cfg.UDPFragment == nil || !*cfg.UDPFragment {
		t.Fatalf("expected udp_fragment=true, got %+v", cfg.UDPFragment)
	}
}

func TestParseShareLinkWireGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, fmt.Errorf("not used")
	})

	link := "wireguard://private-key@engage.cloudflareclient.com:2408?publickey=peer-public-key&ip=172.16.0.2/32&ipv6=2606:4700:110:8765::2/128&allowedips=0.0.0.0/0,::/0&reserved=162,104,222&mtu=1280&workers=2&detour=warp-selector&domain_resolver=local&domain_resolver_strategy=prefer_ipv4&udp_fragment=1#WARP"
	body := bytes.NewBufferString(fmt.Sprintf(`{"link":%q}`, link))

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req, _ := http.NewRequest(http.MethodPost, "/api/parse-link", body)
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	handler.ParseShareLink(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Type   string `json:"type"`
		Name   string `json:"name"`
		Config string `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Type != "wireguard" || resp.Name != "WARP" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	var cfg models.WireGuardConfig
	if err := json.Unmarshal([]byte(resp.Config), &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if cfg.PrivateKey != "private-key" || cfg.Workers != 2 || cfg.MTU != 1280 {
		t.Fatalf("unexpected parsed config: %+v", cfg)
	}
	if len(cfg.Reserved) != 3 || cfg.Reserved[0] != 162 {
		t.Fatalf("unexpected reserved bytes: %+v", cfg.Reserved)
	}
}

func TestReplaceNodeWithWireGuardLinkClearsIPAndPreservesNameWhenRequested(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, fmt.Errorf("not used")
	})

	nodeID := insertTestNode(t, handler.db)
	if _, err := handler.db.Exec(`
		UPDATE proxy_nodes
		SET name = 'old-name', node_ip = '8.8.8.8', location = 'Old', country_code = 'US', latency = 99
		WHERE id = ?
	`, nodeID); err != nil {
		t.Fatalf("seed node: %v", err)
	}

	link := "wireguard://private-key@engage.cloudflareclient.com:2408?publickey=peer-public-key&ip=172.16.0.2/32&ipv6=2606:4700:110:8765::2/128&allowedips=0.0.0.0/0,::/0&reserved=162,104,222#WARP"
	body := bytes.NewBufferString(fmt.Sprintf(`{"link":%q,"update_name":false}`, link))

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{gin.Param{Key: "id", Value: strconv.Itoa(nodeID)}}
	req, _ := http.NewRequest(http.MethodPut, "/api/nodes/"+strconv.Itoa(nodeID)+"/replace", body)
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	handler.ReplaceNode(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d body=%s", rec.Code, rec.Body.String())
	}

	var (
		name        string
		nodeType    string
		configJSON  string
		nodeIP      string
		location    string
		countryCode string
		latency     int
	)
	if err := handler.db.QueryRow(`
		SELECT name, type, config, node_ip, location, country_code, latency
		FROM proxy_nodes WHERE id = ?
	`, nodeID).Scan(&name, &nodeType, &configJSON, &nodeIP, &location, &countryCode, &latency); err != nil {
		t.Fatalf("query node: %v", err)
	}
	if name != "old-name" || nodeType != "wireguard" {
		t.Fatalf("unexpected node identity: name=%q type=%q", name, nodeType)
	}
	if nodeIP != "" || location != "" || countryCode != "" || latency != 0 {
		t.Fatalf("expected IP status cleared, got ip=%q location=%q country=%q latency=%d", nodeIP, location, countryCode, latency)
	}

	var cfg models.WireGuardConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if cfg.Server != "engage.cloudflareclient.com" || cfg.PeerPublicKey != "peer-public-key" {
		t.Fatalf("unexpected replaced config: %+v", cfg)
	}
}

func TestBatchImportNodesAcceptsWireGuardYAMLContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, fmt.Errorf("not used")
	})

	yaml := `
proxies:
  - name: "WARP"
    type: wireguard
    server: engage.cloudflareclient.com
    port: 2408
    ip: 172.16.0.2
    ipv6: "2606:4700:110:8765::2"
    private-key: private-key
    public-key: peer-public-key
    allowed-ips: ["0.0.0.0/0", "::/0"]
    reserved: [162, 104, 222]
    mtu: 1280
    udp: true
`
	payload := map[string]interface{}{
		"content": yaml,
		"enabled": true,
	}
	body, _ := json.Marshal(payload)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req, _ := http.NewRequest(http.MethodPost, "/api/nodes/batch-import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	handler.BatchImportNodes(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Success int `json:"success"`
		Failed  int `json:"failed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Success != 1 || resp.Failed != 0 {
		t.Fatalf("unexpected import response: %+v", resp)
	}

	var configJSON string
	if err := handler.db.QueryRow(
		"SELECT config FROM proxy_nodes WHERE name = ?",
		"WARP",
	).Scan(&configJSON); err != nil {
		t.Fatalf("query node: %v", err)
	}

	var cfg models.WireGuardConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if cfg.Network != "udp" || cfg.Detour != "" {
		t.Fatalf("unexpected batch-import config: %+v", cfg)
	}
	if len(cfg.LocalAddress) != 2 || cfg.LocalAddress[0] != "172.16.0.2/32" {
		t.Fatalf("unexpected local_address: %+v", cfg.LocalAddress)
	}
}

func TestReorderNodesPreserveInboundPortsKeepsPorts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, fmt.Errorf("not used")
	})

	if _, err := handler.db.Exec("UPDATE settings SET preserve_inbound_ports = 1"); err != nil {
		t.Fatalf("set preserve_inbound_ports: %v", err)
	}
	firstID := insertTestNodeWithPortAndOrder(t, handler.db, "node-a", 30001, 0)
	secondID := insertTestNodeWithPortAndOrder(t, handler.db, "node-b", 30005, 1)

	payload := map[string]interface{}{
		"nodes": []map[string]int{
			{"id": secondID, "sort_order": 0},
			{"id": firstID, "sort_order": 1},
		},
	}
	body, _ := json.Marshal(payload)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req, _ := http.NewRequest(http.MethodPost, "/api/nodes/reorder", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	handler.ReorderNodes(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d body=%s", rec.Code, rec.Body.String())
	}

	rows, err := handler.db.Query("SELECT name, inbound_port, sort_order FROM proxy_nodes ORDER BY sort_order")
	if err != nil {
		t.Fatalf("query nodes: %v", err)
	}
	defer rows.Close()

	type row struct {
		name        string
		inboundPort int
		sortOrder   int
	}
	var got []row
	for rows.Next() {
		var current row
		if err := rows.Scan(&current.name, &current.inboundPort, &current.sortOrder); err != nil {
			t.Fatalf("scan node: %v", err)
		}
		got = append(got, current)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(got))
	}
	if got[0].name != "node-b" || got[0].inboundPort != 30005 || got[0].sortOrder != 0 {
		t.Fatalf("unexpected first row: %+v", got[0])
	}
	if got[1].name != "node-a" || got[1].inboundPort != 30001 || got[1].sortOrder != 1 {
		t.Fatalf("unexpected second row: %+v", got[1])
	}
}

func TestUpdateSettingsPreserveModeStartPortDoesNotRewritePorts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, fmt.Errorf("not used")
	})

	if _, err := handler.db.Exec("UPDATE settings SET start_port = 30001, preserve_inbound_ports = 1"); err != nil {
		t.Fatalf("set settings: %v", err)
	}
	nodeID := insertTestNodeWithPortAndOrder(t, handler.db, "node-a", 30010, 0)

	payload := map[string]interface{}{
		"start_port": 35000,
	}
	body, _ := json.Marshal(payload)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req, _ := http.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	handler.UpdateSettings(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d body=%s", rec.Code, rec.Body.String())
	}

	var inboundPort int
	var startPort int
	if err := handler.db.QueryRow("SELECT inbound_port FROM proxy_nodes WHERE id = ?", nodeID).Scan(&inboundPort); err != nil {
		t.Fatalf("query inbound_port: %v", err)
	}
	if err := handler.db.QueryRow("SELECT start_port FROM settings LIMIT 1").Scan(&startPort); err != nil {
		t.Fatalf("query start_port: %v", err)
	}
	if inboundPort != 30010 {
		t.Fatalf("expected inbound_port to stay 30010, got %d", inboundPort)
	}
	if startPort != 35000 {
		t.Fatalf("expected start_port=35000, got %d", startPort)
	}
}

func TestUpdateSettingsDisablingPreserveModeRealignsPorts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, fmt.Errorf("not used")
	})

	if _, err := handler.db.Exec("UPDATE settings SET start_port = 30001, preserve_inbound_ports = 1"); err != nil {
		t.Fatalf("set settings: %v", err)
	}
	insertTestNodeWithPortAndOrder(t, handler.db, "node-a", 30010, 0)
	insertTestNodeWithPortAndOrder(t, handler.db, "node-b", 30050, 1)

	payload := map[string]interface{}{
		"start_port":             32000,
		"preserve_inbound_ports": false,
	}
	body, _ := json.Marshal(payload)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req, _ := http.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	handler.UpdateSettings(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d body=%s", rec.Code, rec.Body.String())
	}

	rows, err := handler.db.Query("SELECT name, inbound_port FROM proxy_nodes ORDER BY sort_order")
	if err != nil {
		t.Fatalf("query nodes: %v", err)
	}
	defer rows.Close()

	got := map[string]int{}
	for rows.Next() {
		var name string
		var inboundPort int
		if err := rows.Scan(&name, &inboundPort); err != nil {
			t.Fatalf("scan node: %v", err)
		}
		got[name] = inboundPort
	}
	if got["node-a"] != 32000 || got["node-b"] != 32001 {
		t.Fatalf("unexpected realigned ports: %+v", got)
	}

	var preserveInboundPorts bool
	if err := handler.db.QueryRow("SELECT preserve_inbound_ports FROM settings LIMIT 1").Scan(&preserveInboundPorts); err != nil {
		t.Fatalf("query preserve_inbound_ports: %v", err)
	}
	if preserveInboundPorts {
		t.Fatalf("expected preserve_inbound_ports=false after update")
	}
}

func TestDeleteNodePreserveInboundPortsKeepsRemainingPorts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, fmt.Errorf("not used")
	})

	if _, err := handler.db.Exec("UPDATE settings SET preserve_inbound_ports = 1"); err != nil {
		t.Fatalf("set preserve_inbound_ports: %v", err)
	}
	firstID := insertTestNodeWithPortAndOrder(t, handler.db, "node-a", 30001, 0)
	deleteID := insertTestNodeWithPortAndOrder(t, handler.db, "node-b", 30005, 1)
	thirdID := insertTestNodeWithPortAndOrder(t, handler.db, "node-c", 30009, 2)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{gin.Param{Key: "id", Value: strconv.Itoa(deleteID)}}
	ctx.Request, _ = http.NewRequest(http.MethodDelete, "/api/nodes/"+strconv.Itoa(deleteID), nil)

	handler.DeleteNode(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d body=%s", rec.Code, rec.Body.String())
	}

	type row struct {
		id          int
		name        string
		inboundPort int
		sortOrder   int
	}
	rows, err := handler.db.Query("SELECT id, name, inbound_port, sort_order FROM proxy_nodes ORDER BY sort_order")
	if err != nil {
		t.Fatalf("query nodes: %v", err)
	}
	defer rows.Close()

	var got []row
	for rows.Next() {
		var current row
		if err := rows.Scan(&current.id, &current.name, &current.inboundPort, &current.sortOrder); err != nil {
			t.Fatalf("scan node: %v", err)
		}
		got = append(got, current)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(got))
	}
	if got[0].id != firstID || got[0].inboundPort != 30001 || got[0].sortOrder != 0 {
		t.Fatalf("unexpected first row after delete: %+v", got[0])
	}
	if got[1].id != thirdID || got[1].inboundPort != 30009 || got[1].sortOrder != 1 {
		t.Fatalf("unexpected second row after delete: %+v", got[1])
	}
}

func TestBatchDeleteNodesPreserveInboundPortsKeepsRemainingPorts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, func(proxyAddr, username, password string) (*services.IPInfo, error) {
		return nil, fmt.Errorf("not used")
	})

	if _, err := handler.db.Exec("UPDATE settings SET preserve_inbound_ports = 1"); err != nil {
		t.Fatalf("set preserve_inbound_ports: %v", err)
	}
	deleteFirstID := insertTestNodeWithPortAndOrder(t, handler.db, "node-a", 30001, 0)
	keepID := insertTestNodeWithPortAndOrder(t, handler.db, "node-b", 30005, 1)
	deleteLastID := insertTestNodeWithPortAndOrder(t, handler.db, "node-c", 30009, 2)

	payload := map[string]interface{}{
		"ids": []int{deleteFirstID, deleteLastID},
	}
	body, _ := json.Marshal(payload)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req, _ := http.NewRequest(http.MethodPost, "/api/nodes/batch-delete", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	handler.BatchDeleteNodes(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d body=%s", rec.Code, rec.Body.String())
	}

	var inboundPort int
	var sortOrder int
	if err := handler.db.QueryRow("SELECT inbound_port, sort_order FROM proxy_nodes WHERE id = ?", keepID).Scan(&inboundPort, &sortOrder); err != nil {
		t.Fatalf("query kept node: %v", err)
	}
	if inboundPort != 30005 || sortOrder != 0 {
		t.Fatalf("expected kept node to stay on port 30005 with sort_order 0, got port=%d sort=%d", inboundPort, sortOrder)
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

// newTestHandlerWithScriptedKernel builds a handler whose fake sing-box
// rejects `check` for configs containing BAD_MARKER, mirroring how the real
// kernel rejects unsupported node parameters.
func newTestHandlerWithScriptedKernel(t *testing.T) *Handler {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := models.InitDB(db); err != nil {
		t.Fatalf("init db: %v", err)
	}

	script := `#!/bin/sh
if [ "$1" = "check" ]; then
  if grep -q "BAD_MARKER\|unsupported-flow" "$3"; then
    echo "FATAL[0000] decode config: unknown field" >&2
    exit 1
  fi
  exit 0
fi
sleep 300
`
	fakeBinary := filepath.Join(t.TempDir(), "fake-sing-box")
	if err := os.WriteFile(fakeBinary, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sing-box binary: %v", err)
	}
	t.Setenv("SINGBOX_BINARY", fakeBinary)
	t.Setenv("SBPM_SKIP_PORT_AVAILABILITY_CHECK", "1")
	svc := services.NewSingBoxService(t.TempDir())
	h := NewHandler(db, svc)
	t.Cleanup(func() {
		_ = svc.Stop()
		_ = db.Close()
	})
	return h
}

func postJSON(t *testing.T, handlerFn gin.HandlerFunc, method, path string, payload interface{}, params gin.Params) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(payload)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req, _ := http.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	ctx.Params = params
	handlerFn(ctx)
	return rec
}

func TestCreateNodeRevertsWhenKernelRejectsConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandlerWithScriptedKernel(t)

	badPayload := map[string]interface{}{
		"name":    "bad-node",
		"type":    "ss",
		"config":  `{"server":"BAD_MARKER.example.com","server_port":443,"method":"aes-128-gcm","password":"p"}`,
		"enabled": true,
	}

	rec := postJSON(t, handler.CreateNode, http.MethodPost, "/api/nodes", badPayload, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for kernel-rejected node, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "decode config: unknown field") {
		t.Fatalf("kernel error must be surfaced to the client, got %s", rec.Body.String())
	}

	var count int
	if err := handler.db.QueryRow("SELECT COUNT(*) FROM proxy_nodes").Scan(&count); err != nil {
		t.Fatalf("count nodes: %v", err)
	}
	if count != 0 {
		t.Fatalf("rejected node must be reverted from the database, found %d rows", count)
	}

	// The panel must stay fully usable: a valid node right after must succeed.
	goodPayload := map[string]interface{}{
		"name":    "good-node",
		"type":    "ss",
		"config":  `{"server":"good.example.com","server_port":443,"method":"aes-128-gcm","password":"p"}`,
		"enabled": true,
	}
	rec = postJSON(t, handler.CreateNode, http.MethodPost, "/api/nodes", goodPayload, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 after revert, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUpdateNodeRevertsWhenKernelRejectsConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandlerWithScriptedKernel(t)
	nodeID := insertTestNode(t, handler.db)

	badPayload := map[string]interface{}{
		"name":    "node1",
		"type":    "ss",
		"config":  `{"server":"BAD_MARKER.example.com","server_port":443,"method":"aes-128-gcm","password":"p"}`,
		"enabled": true,
	}

	rec := postJSON(t, handler.UpdateNode, http.MethodPut, "/api/nodes/"+strconv.Itoa(nodeID), badPayload,
		gin.Params{gin.Param{Key: "id", Value: strconv.Itoa(nodeID)}})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for kernel-rejected update, got %d body=%s", rec.Code, rec.Body.String())
	}

	var config string
	if err := handler.db.QueryRow("SELECT config FROM proxy_nodes WHERE id = ?", nodeID).Scan(&config); err != nil {
		t.Fatalf("query config: %v", err)
	}
	if strings.Contains(config, "BAD_MARKER") {
		t.Fatalf("rejected update must be reverted, config still contains bad data: %s", config)
	}
	if !strings.Contains(config, "example.com") {
		t.Fatalf("previous config must be restored, got: %s", config)
	}
}

func TestBatchImportKeepsGoodNodeWhenKernelRejectsPeer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandlerWithScriptedKernel(t)

	payload := map[string]interface{}{
		"content": strings.Join([]string{
			"socks5://user:pass@127.0.0.1:1080#valid-socks",
			"vless://00000000-0000-0000-0000-000000000000@127.0.0.1:443?security=tls&flow=unsupported-flow&detour=valid-socks#bad-vless",
		}, "\n"),
		"enabled": true,
	}
	rec := postJSON(t, handler.BatchImportNodes, http.MethodPost, "/api/nodes/batch-import", payload, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected partial-success status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var response struct {
		Total   int `json:"total"`
		Success int `json:"success"`
		Failed  int `json:"failed"`
		Results []struct {
			Success bool   `json:"success"`
			Name    string `json:"name"`
			Error   string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Total != 2 || response.Success != 1 || response.Failed != 1 {
		t.Fatalf("unexpected partial import summary: %+v body=%s", response, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "decode config: unknown field") {
		t.Fatalf("kernel rejection was not attributed to the bad node: %s", rec.Body.String())
	}

	var count int
	var proxyType, name string
	if err := handler.db.QueryRow("SELECT COUNT(*), MIN(type), MIN(name) FROM proxy_nodes").Scan(&count, &proxyType, &name); err != nil {
		t.Fatalf("query persisted batch: %v", err)
	}
	if count != 1 || proxyType != "socks5" || name != "valid-socks" {
		t.Fatalf("valid SOCKS node was not retained: count=%d type=%q name=%q", count, proxyType, name)
	}
}

func TestBatchImportKeepsGoodNodeWhenRealSingBoxRejectsFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, nil)
	useRealSingBoxForChecks(t)

	payload := map[string]interface{}{
		"content": strings.Join([]string{
			"socks5://user:pass@127.0.0.1:1080#real-valid-socks",
			"vless://00000000-0000-0000-0000-000000000000@127.0.0.1:443?security=tls&flow=unsupported-flow&detour=real-valid-socks#real-bad-vless",
		}, "\n"),
		"enabled": true,
	}
	rec := postJSON(t, handler.BatchImportNodes, http.MethodPost, "/api/nodes/batch-import", payload, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected real-kernel partial success, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"success":1`) || !strings.Contains(rec.Body.String(), `"failed":1`) {
		t.Fatalf("real kernel did not isolate unsupported flow: %s", rec.Body.String())
	}
	var count int
	var proxyType string
	if err := handler.db.QueryRow("SELECT COUNT(*), MIN(type) FROM proxy_nodes").Scan(&count, &proxyType); err != nil {
		t.Fatalf("query real-kernel import: %v", err)
	}
	if count != 1 || proxyType != "socks5" {
		t.Fatalf("valid node was lost after real kernel rejection: count=%d type=%q", count, proxyType)
	}
}

func TestBatchImportProtocolMatrixPassesRealSingBoxValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, nil)
	useRealSingBoxForChecks(t)

	vmessJSON, err := json.Marshal(map[string]interface{}{
		"v":    "2",
		"ps":   "VMess HTTP",
		"add":  "127.0.0.1",
		"port": "10082",
		"id":   "11111111-1111-1111-1111-111111111111",
		"aid":  "0",
		"scy":  "auto",
		"net":  "http",
		"host": "vmess.example.com",
		"path": "/vmess",
		"tls":  "",
	})
	if err != nil {
		t.Fatalf("marshal VMess fixture: %v", err)
	}

	ssPassword := "MDEyMzQ1Njc4OWFiY2RlZg=="
	privateKey := "YNXtAzepDqRv9H52osJVDQnznT5AM11eCK3ESpwSt04="
	publicKey := "Z1XXLsKYkYxuiYjJIkRvtIKFepCYHTgON+GwPq7SOV4="
	content := strings.Join([]string{
		"ss://2022-blake3-aes-128-gcm:" + url.QueryEscape(ssPassword) + "@127.0.0.1:10080/?plugin=" + url.QueryEscape("obfs-local;obfs=http;obfs-host=example.com") + "#SS-SIP002",
		"vless://22222222-2222-2222-2222-222222222222@[2001:db8::1]:443?security=tls&type=ws&path=%2Fvless&host=vless.example.com&sni=vless.example.com&allowInsecure=1#VLESS-IPv6",
		"vmess://" + base64.RawURLEncoding.EncodeToString(vmessJSON),
		"trojan://p%40ss@[2001:db8::2]:443?type=h2&host=trojan.example.com&path=%2Ftrojan&sni=trojan.example.com&insecure=1#Trojan-H2",
		"hysteria2://hy%3Apass@127.0.0.1:443,8443-8444/?obfs=salamander&obfs-password=obfs-pass&sni=hy2.example.com&insecure=1#HY2-Hop",
		"tuic://33333333-3333-3333-3333-333333333333:tuic%40pass@127.0.0.1:10443?congestion_control=bbr&udp_relay_mode=native&reduce_rtt=1&disable_sni=1&insecure=1#TUIC",
		"anytls://any%40pass@[2001:db8::3]:443?sni=anytls.example.com&alpn=h2,http/1.1&insecure=1#AnyTLS",
		"socks5h://user%40name:pass%3Aword@127.0.0.1:1080#SOCKS5H",
		"https://http%40user:http%3Apass@127.0.0.1:8443?proxy=1&sni=http.example.com&insecure=1#HTTPS",
		"wireguard://" + url.QueryEscape(privateKey) + "@127.0.0.1:2408?publickey=" + url.QueryEscape(publicKey) + "&ip=172.16.0.2/32&allowedips=0.0.0.0/0,::/0&reserved=1,2,3#WireGuard",
	}, "\n")

	payload := map[string]interface{}{
		"content": content,
		"enabled": true,
	}
	rec := postJSON(t, handler.BatchImportNodes, http.MethodPost, "/api/nodes/batch-import", payload, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("protocol batch import failed real sing-box validation: status=%d body=%s", rec.Code, rec.Body.String())
	}

	var response struct {
		Total   int `json:"total"`
		Success int `json:"success"`
		Failed  int `json:"failed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Total != 10 || response.Success != 10 || response.Failed != 0 {
		t.Fatalf("unexpected import summary: %+v body=%s", response, rec.Body.String())
	}

	var count int
	if err := handler.db.QueryRow("SELECT COUNT(*) FROM proxy_nodes").Scan(&count); err != nil {
		t.Fatalf("count imported nodes: %v", err)
	}
	if count != 10 {
		t.Fatalf("expected all protocol fixtures persisted, got %d", count)
	}
}

func TestCreateDisabledNodeRejectsUnknownProtocol(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, nil)

	payload := map[string]interface{}{
		"name":    "unsupported",
		"type":    "shadowtls",
		"config":  `{}`,
		"enabled": false,
	}
	rec := postJSON(t, handler.CreateNode, http.MethodPost, "/api/nodes", payload, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown protocol must be rejected before persistence, got %d body=%s", rec.Code, rec.Body.String())
	}

	var count int
	if err := handler.db.QueryRow("SELECT COUNT(*) FROM proxy_nodes").Scan(&count); err != nil {
		t.Fatalf("count nodes: %v", err)
	}
	if count != 0 {
		t.Fatalf("unknown disabled protocol must not be stored, found %d rows", count)
	}
}

func TestCreateDisabledNodeRejectsUnknownConfigField(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, nil)

	payload := map[string]interface{}{
		"name":    "unknown-field",
		"type":    "socks5",
		"config":  `{"server":"127.0.0.1","server_port":1080,"silently_dropped":true}`,
		"enabled": false,
	}
	rec := postJSON(t, handler.CreateNode, http.MethodPost, "/api/nodes", payload, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown config fields must not be silently accepted, got %d body=%s", rec.Code, rec.Body.String())
	}
}
