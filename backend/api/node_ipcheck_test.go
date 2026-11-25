package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
)

func TestNodeIPCheckPersistsPortlessResultsAndSafeDiagnostics(t *testing.T) {
	handler := newTestHandler(t, nil)
	nodeID := insertTestNode(t, handler.db)
	if _, err := handler.db.Exec(`UPDATE proxy_nodes SET inbound_port = NULL, password = ?,
		upstream_mode = 'custom', upstream_type = 'socks5', upstream_config = ? WHERE id = ?`,
		"stored-ingress-key", `{"server":"192.0.2.8","server_port":1080,"password":"upstream-key"}`, nodeID); err != nil {
		t.Fatal(err)
	}
	handler.checkUpstreamIP = func(context.Context, models.ProxyDefinition) (*services.IPInfo, error) {
		return nil, errors.New("handshake failed for upstream-key")
	}
	longError := "dial failed at https://account:pw@sub.example/feed/private?token=remote-token stored-ingress-key " + strings.Repeat("错", 850)
	var failure error
	var addresses []string
	var previousUser, previousPassword string
	handler.checkProxyIP = func(_ context.Context, address, username, password string) (*services.IPInfo, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || host != "127.0.0.1" || port == "0" || username == "user" || password == "stored-ingress-key" ||
			username == previousUser || password == previousPassword {
			t.Error("isolated check did not use fresh loopback credentials and port")
		}
		addresses = append(addresses, address)
		previousUser, previousPassword = username, password
		if failure != nil {
			return nil, failure
		}
		return &services.IPInfo{IP: "203.0.113.4", Location: "Test City", CountryCode: "ZZ", Latency: 17, HTTPError: "retry via https://sub.example/fallback"}, nil
	}
	for _, test := range []struct {
		status string
		code   int
		err    error
	}{
		{status: "ok", code: http.StatusOK},
		{status: "failed", code: http.StatusBadGateway, err: errors.New(longError)},
		{status: "failed", code: http.StatusTooManyRequests, err: &services.IPCheckHTTPStatusError{StatusCode: 429, RetryAfter: "27"}},
	} {
		failure = test.err
		response := postJSON(t, handler.CheckNodeIP, http.MethodPost, fmt.Sprintf("/api/nodes/%d/check-ip", nodeID), nil, ginParams("id", strconv.Itoa(nodeID)))
		var body struct {
			Status    string    `json:"check_status"`
			Message   string    `json:"check_error"`
			CheckedAt time.Time `json:"checked_at"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		stored, err := models.LoadProxyNode(context.Background(), handler.db, nodeID)
		if err != nil {
			t.Fatal(err)
		}
		if response.Code != test.code || stored.CheckStatus != test.status || stored.CheckRevision != 0 || stored.CheckedAt == nil ||
			!body.CheckedAt.Equal(*stored.CheckedAt) || body.Status != stored.CheckStatus || body.Message != stored.CheckError {
			t.Fatalf("inconsistent check result: HTTP %d, response status %q, stored status %q", response.Code, body.Status, stored.CheckStatus)
		}
		if stored.InboundPort != 0 || stored.Username != "user" || stored.Password != "stored-ingress-key" || stored.UpstreamError != "handshake failed for [redacted]" {
			t.Fatal("node settings or custom upstream result changed unexpectedly")
		}
		if strings.Contains(response.Body.String(), "sub.example") || strings.Contains(response.Body.String(), "upstream-key") || strings.Contains(response.Body.String(), "stored-ingress-key") {
			t.Fatal("response exposed a credential or a subscription URL")
		}
		switch test.code {
		case http.StatusOK:
			if stored.NodeIP != "203.0.113.4" || stored.CountryCode != "ZZ" || stored.Location != "Test City" || stored.Latency != 17 || stored.CheckError != "" {
				t.Fatal("successful probe did not persist its IP, location and latency")
			}
		case http.StatusBadGateway:
			if !utf8.ValidString(stored.CheckError) || len(stored.CheckError) < 1997 || len(stored.CheckError) > 2000 || !strings.Contains(stored.CheckError, "dial failed") ||
				stored.NodeIP != "" || stored.Location != "" || stored.CountryCode != "" || stored.Latency != 0 {
				t.Fatal("failed probe did not clear stale values and persist a bounded diagnostic")
			}
		case http.StatusTooManyRequests:
			if response.Header().Get("Retry-After") != "27" || !strings.Contains(stored.CheckError, "HTTP 429") {
				t.Fatal("rate-limited isolated probe lost its status or retry header")
			}
		}
	}
	if len(addresses) != 3 {
		t.Fatalf("isolated probe called %d times", len(addresses))
	}
}

func TestNodeIPCheckRevisionAndCancellationDiscardBothResults(t *testing.T) {
	for _, action := range []string{"edit-success", "edit-failure", "delete", "cancel"} {
		t.Run(action, func(t *testing.T) {
			handler := newTestHandler(t, nil)
			nodeID := insertTestNode(t, handler.db)
			if _, err := handler.db.Exec(`UPDATE proxy_nodes SET upstream_mode='custom', upstream_type='direct', upstream_config='{}',
				upstream_ip='previous-upstream', node_ip='previous-final', check_status='ok', checked_at=CURRENT_TIMESTAMP WHERE id=?`, nodeID); err != nil {
				t.Fatal(err)
			}
			before, err := models.LoadProxyNode(context.Background(), handler.db, nodeID)
			if err != nil {
				t.Fatal(err)
			}
			requestCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			handler.checkUpstreamIP = func(context.Context, models.ProxyDefinition) (*services.IPInfo, error) {
				return &services.IPInfo{IP: "stale-upstream"}, nil
			}
			handler.checkProxyIP = func(context.Context, string, string, string) (*services.IPInfo, error) {
				switch action {
				case "cancel":
					cancel()
				case "delete":
					_, err = handler.db.Exec("DELETE FROM proxy_nodes WHERE id = ?", nodeID)
				default:
					_, err = handler.db.Exec("UPDATE proxy_nodes SET check_revision = check_revision + 1 WHERE id = ?", nodeID)
				}
				if err != nil {
					t.Fatal(err)
				}
				if action == "edit-failure" {
					return nil, &services.IPCheckHTTPStatusError{StatusCode: 429, RetryAfter: "30"}
				}
				return &services.IPInfo{IP: "stale-final"}, nil
			}
			recorder := httptest.NewRecorder()
			ginCtx, _ := gin.CreateTestContext(recorder)
			ginCtx.Params = ginParams("id", strconv.Itoa(nodeID))
			ginCtx.Request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/nodes/%d/check-ip", nodeID), nil).WithContext(requestCtx)
			handler.CheckNodeIP(ginCtx)
			if action != "cancel" && (recorder.Code != http.StatusConflict || recorder.Header().Get("Retry-After") != "") {
				t.Fatalf("stale result returned HTTP %d instead of a conflict", recorder.Code)
			}
			if action == "delete" {
				return
			}
			after, err := models.LoadProxyNode(context.Background(), handler.db, nodeID)
			if err != nil {
				t.Fatal(err)
			}
			if after.NodeIP != before.NodeIP || after.UpstreamIP != before.UpstreamIP || after.CheckStatus != before.CheckStatus ||
				after.CheckError != before.CheckError || after.UpstreamError != before.UpstreamError || after.CheckedAt == nil || !after.CheckedAt.Equal(*before.CheckedAt) {
				t.Fatal("stale or canceled request partially overwrote stored check results")
			}
		})
	}
}

func TestRealPortlessNodeIPCheck(t *testing.T) {
	binary := os.Getenv("SINGBOX_TEST_BINARY")
	if binary == "" {
		t.Skip("SINGBOX_TEST_BINARY not set")
	}
	service := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		host, _, err := net.SplitHostPort(request.RemoteAddr)
		if err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]string{"ip": host, "country": "Example", "cc": "ZZ"})
	}))
	t.Cleanup(service.Close)
	handler := newTestHandler(t, nil)
	t.Setenv("SINGBOX_BINARY", binary)
	t.Setenv("SBPM_IPCHECK_URLS", service.URL)
	nodeID := insertTestNode(t, handler.db)
	if _, err := handler.db.Exec("UPDATE proxy_nodes SET inbound_port=NULL, type='direct', config='{}' WHERE id=?", nodeID); err != nil {
		t.Fatal(err)
	}
	check := func(wantIP string) {
		t.Helper()
		response := postJSON(t, handler.CheckNodeIP, http.MethodPost, fmt.Sprintf("/api/nodes/%d/check-ip", nodeID), nil, ginParams("id", strconv.Itoa(nodeID)))
		stored, err := models.LoadProxyNode(context.Background(), handler.db, nodeID)
		if err != nil || response.Code != http.StatusOK || stored.CheckStatus != "ok" || stored.NodeIP != wantIP || stored.CountryCode != "ZZ" || stored.InboundPort != 0 {
			t.Fatalf("portless probe expected %s: HTTP %d, check %q, error %v, response %s", wantIP, response.Code, stored.CheckStatus, err, response.Body.String())
		}
	}
	check("127.0.0.1")
	if _, err := handler.db.Exec(`INSERT INTO proxy_nodes (group_id, name, type, config, sort_order)
		SELECT group_id, 'probe-egress', 'direct', '{"inet4_bind_address":"127.0.0.2"}', 1 FROM proxy_nodes WHERE id = ?`, nodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.db.Exec(`UPDATE proxy_nodes SET upstream_mode='legacy', config='{"detour":"probe-egress"}' WHERE id=?`, nodeID); err != nil {
		t.Fatal(err)
	}
	check("127.0.0.2")
	if _, err := handler.db.Exec(`UPDATE node_groups SET upstream_enabled=TRUE,upstream_type='direct',upstream_config='{"inet4_bind_address":"127.0.0.3"}' WHERE id=(SELECT group_id FROM proxy_nodes WHERE id=?)`, nodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.db.Exec(`UPDATE settings SET global_upstream_enabled=TRUE,global_upstream_type='direct',global_upstream_config='{"inet4_bind_address":"127.0.0.4"}' WHERE singleton_key=1`); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"global", "group"} {
		if _, err := handler.db.Exec(`UPDATE proxy_nodes SET upstream_mode=?,config='{}' WHERE id=?`, mode, nodeID); err != nil {
			t.Fatal(err)
		}
		check("127.0.0.3")
	}
}
