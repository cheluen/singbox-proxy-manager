package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/proxy"

	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
)

func TestGroupUpstreamMutationAndCheckRevisions(t *testing.T) {
	client := newGroupTestClient(t)
	const globalConfig = `{"server":"192.0.2.1","server_port":1080}`
	const groupConfig = `{"server":"192.0.2.2","server_port":1080}`
	client.request("PUT", "/api/settings", gin.H{"global_upstream_enabled": true, "global_upstream_type": "socks5", "global_upstream_config": globalConfig}, 200, nil)
	first := client.createGroup("First", 31001, false)
	second := client.createGroup("Second", 32001, false)
	create := func(groupID int, mode string) models.ProxyNode {
		payload := gin.H{"name": mode, "group_id": groupID, "type": "direct", "config": "{}", "enabled": true, "upstream_mode": mode}
		if mode == "custom" {
			payload["upstream_type"], payload["upstream_config"] = "socks5", globalConfig
		}
		var node models.ProxyNode
		client.request("POST", "/api/nodes", payload, 201, &node)
		return node
	}
	follower := create(first.ID, "global")
	groupOnly := create(first.ID, "group")
	custom := create(first.ID, "custom")
	create(first.ID, "none")
	other := create(second.ID, "global")
	var policy models.ProxyNode
	policyInput := gin.H{"name": "policy", "enabled": true, "group_id": second.ID, "type": "failover", "config": fmt.Sprintf(`{"member_ids":[%d]}`, follower.ID)}
	policyInput["upstream_mode"] = "group"
	client.request("POST", "/api/nodes", policyInput, 400, nil)
	policyInput["upstream_mode"] = "none"
	client.request("POST", "/api/nodes", policyInput, 201, &policy)
	disabled := create(first.ID, "group")
	client.request("PATCH", testNodePath(disabled.ID), gin.H{"enabled": false}, 200, nil)
	policyInput["config"] = fmt.Sprintf(`{"member_ids":[%d]}`, disabled.ID)
	client.request("POST", "/api/nodes", policyInput, 201, nil)
	seed := func() {
		if _, err := client.db.Exec(`UPDATE proxy_nodes SET check_status='ok',check_revision=10,checked_at=CURRENT_TIMESTAMP,node_ip='192.0.2.90',latency=12`); err != nil {
			t.Fatal(err)
		}
	}
	assert := func(changed ...int) {
		t.Helper()
		reset := map[int]bool{}
		for _, id := range changed {
			reset[id] = true
		}
		nodes, err := models.LoadProxyNodes(t.Context(), client.db)
		if err != nil {
			t.Fatal(err)
		}
		for _, node := range nodes {
			if reset[node.ID] {
				if node.CheckRevision != 11 || node.CheckStatus != "untested" || node.CheckedAt != nil || node.NodeIP != "" || node.Latency != 0 {
					t.Fatalf("node %d kept a stale check", node.ID)
				}
			} else if node.CheckRevision != 10 || node.CheckStatus != "ok" || node.CheckedAt == nil {
				t.Fatalf("unaffected node %d lost its check", node.ID)
			}
		}
	}
	seed()
	var configured models.NodeGroup
	client.request("PATCH", testGroupPath(first.ID), gin.H{"upstream_enabled": true, "upstream_type": "socks5", "upstream_config": groupConfig}, 200, &configured)
	if !configured.UpstreamEnabled || configured.UpstreamConfig != groupConfig {
		t.Fatal("group settings were not saved")
	}
	assert(follower.ID, groupOnly.ID, policy.ID)
	for _, enabled := range []bool{false, true} {
		seed()
		client.request("PATCH", testGroupPath(first.ID), gin.H{"upstream_enabled": enabled}, 200, nil)
		assert(follower.ID, groupOnly.ID, policy.ID)
	}
	seed()
	client.request("PUT", "/api/settings", gin.H{"global_upstream_config": `{"server":"192.0.2.3","server_port":1080}`}, 200, nil)
	assert(other.ID)
	seed()
	client.request("POST", "/api/nodes/batch-move", gin.H{"ids": []int{follower.ID, custom.ID}, "group_id": second.ID}, 200, nil)
	assert(follower.ID, policy.ID)
	seed()
	client.request("DELETE", testGroupPath(first.ID)+"?mode=move&target_group_id="+strconv.Itoa(second.ID), nil, 200, nil)
	assert(groupOnly.ID)
}

func TestGroupUpstreamRollbackAndValidation(t *testing.T) {
	client := newGroupTestClient(t)
	entry := client.createNode("inbound", 0, true)
	if _, err := client.db.Exec("UPDATE proxy_nodes SET tcp_reuse_enabled=TRUE WHERE id=?", entry.ID); err != nil {
		t.Fatal(err)
	}
	loop := gin.H{"name": "unused group", "upstream_enabled": true, "upstream_type": "http", "upstream_config": fmt.Sprintf(`{"server":"127.0.0.1","server_port":%d}`, entry.InboundPort)}
	client.request("POST", "/api/node-groups", loop, 400, nil)
	var total int
	if err := client.db.QueryRow("SELECT COUNT(*) FROM node_groups").Scan(&total); err != nil || total != 1 {
		t.Fatalf("failed create left a group: %d, %v", total, err)
	}
	path := testGroupPath(entry.GroupID)
	for _, invalid := range []gin.H{{"upstream_enabled": true}, {"upstream_type": "failover", "upstream_config": `{"member_ids":[1]}`}, {"upstream_type": "direct", "upstream_config": `{"detour":"node-1-out"}`}} {
		client.request("PATCH", path, invalid, 400, nil)
	}
	client.request("PUT", testNodePath(entry.ID)+"/upstream", gin.H{"upstream_mode": "group"}, 200, nil)
	client.request("PATCH", path, gin.H{"upstream_enabled": true, "upstream_type": "direct", "upstream_config": "{}"}, 400, nil)
	var group models.NodeGroup
	client.request("GET", path, nil, 200, &group)
	if group.UpstreamEnabled || group.UpstreamType != "" {
		t.Fatal("failed upstream saved")
	}
	if _, err := client.db.Exec("UPDATE node_groups SET upstream_enabled=TRUE,upstream_type='invalid',upstream_config='malformed',upstream_ip='192.0.2.2',upstream_location='City',upstream_country_code='ZZ',upstream_latency=30,upstream_error='previous' WHERE id=?", entry.GroupID); err != nil {
		t.Fatal(err)
	}
	client.request("PATCH", path, gin.H{"upstream_enabled": false}, 200, &group)
	if group.UpstreamEnabled || group.UpstreamIP != "192.0.2.2" || group.UpstreamLatency != 30 || group.UpstreamError != "previous" {
		t.Fatal("disabling malformed definition lost its check")
	}
	client.request("PATCH", path, gin.H{"upstream_type": "http", "upstream_config": `{"server":"192.0.2.2","server_port":8080}`}, 200, &group)
	if group.UpstreamIP != "" || group.UpstreamLocation != "" || group.UpstreamCountryCode != "" || group.UpstreamLatency != 0 || group.UpstreamError != "" {
		t.Fatal("new definition retained old check")
	}
}

func TestCheckGroupUpstreamIP(t *testing.T) {
	handler := newTestHandler(t, nil)
	group := firstTestGroup(t, handler.db)
	check := func(id, want int) string {
		t.Helper()
		response := postJSON(t, handler.CheckGroupUpstreamIP, http.MethodPost, testGroupPath(id)+"/upstream/check-ip", nil, ginParams("id", strconv.Itoa(id)))
		if response.Code != want {
			t.Fatalf("check status %d want %d: %s", response.Code, want, response.Body.String())
		}
		return response.Body.String()
	}
	check(group.ID+1, 404)
	check(group.ID, 400)
	const credential = "test-group-probe-credential"
	config := fmt.Sprintf(`{"server":"192.0.2.2","server_port":8080,"password":%q}`, credential)
	if _, err := handler.db.Exec("UPDATE node_groups SET upstream_type='http',upstream_config=? WHERE id=?", config, group.ID); err != nil {
		t.Fatal(err)
	}
	read := func() models.NodeGroup {
		t.Helper()
		result, err := models.LoadGroup(t.Context(), handler.db, group.ID)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	handler.checkUpstreamIP = func(_ context.Context, def models.ProxyDefinition) (*services.IPInfo, error) {
		if def.Type != "http" || def.Config != config {
			t.Fatal("incorrect saved definition")
		}
		return &services.IPInfo{IP: "192.0.2.90", Location: "City", CountryCode: "ZZ", Latency: 24}, nil
	}
	check(group.ID, 200)
	if stored := read(); stored.UpstreamEnabled || stored.UpstreamIP != "192.0.2.90" || stored.UpstreamLatency != 24 || stored.UpstreamLocation != "City" || stored.UpstreamCountryCode != "ZZ" || stored.UpstreamError != "" {
		t.Fatal("successful check not persisted")
	}
	handler.checkUpstreamIP = func(context.Context, models.ProxyDefinition) (*services.IPInfo, error) {
		return nil, errors.New("rejected " + credential)
	}
	if strings.Contains(check(group.ID, 502), credential) {
		t.Fatal("response exposed credentials")
	}
	if stored := read(); stored.UpstreamIP != "" || stored.UpstreamLatency != 0 || stored.UpstreamError == "" || strings.Contains(stored.UpstreamError, credential) {
		t.Fatal("failed check not safely persisted")
	}
	handler.checkUpstreamIP = func(context.Context, models.ProxyDefinition) (*services.IPInfo, error) {
		return nil, &services.IPCheckHTTPStatusError{StatusCode: http.StatusTooManyRequests, RetryAfter: "31"}
	}
	response := postJSON(t, handler.CheckGroupUpstreamIP, http.MethodPost, testGroupPath(group.ID)+"/upstream/check-ip", nil, ginParams("id", strconv.Itoa(group.ID)))
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "31" || !strings.Contains(read().UpstreamError, "HTTP 429") {
		t.Fatalf("group rate limit: HTTP %d, Retry-After %q", response.Code, response.Header().Get("Retry-After"))
	}
	handler.checkUpstreamIP = func(context.Context, models.ProxyDefinition) (*services.IPInfo, error) {
		if _, err := handler.db.Exec("UPDATE node_groups SET upstream_config='{}',upstream_error='' WHERE id=?", group.ID); err != nil {
			t.Fatal(err)
		}
		return &services.IPInfo{IP: "192.0.2.99"}, nil
	}
	check(group.ID, 409)
	if stored := read(); stored.UpstreamIP != "" || stored.UpstreamError != "" {
		t.Fatal("outdated check was persisted")
	}
}

func TestGroupUpstreamDisabledDraftSkipsRuntime(t *testing.T) {
	handler := newTestHandlerWithKernelScript(t, "#!/bin/sh\nexit 1\n")
	group := firstTestGroup(t, handler.db)
	if _, err := handler.db.Exec(`UPDATE node_groups SET upstream_ip='192.0.2.8',upstream_latency=12,upstream_error='previous' WHERE id=?`, group.ID); err != nil {
		t.Fatal(err)
	}
	params := ginParams("id", strconv.Itoa(group.ID))
	path := testGroupPath(group.ID)
	const config = `{"server":"192.0.2.2","server_port":8080}`
	response := postJSON(t, handler.UpdateGroup, http.MethodPatch, path, gin.H{"upstream_type": "http", "upstream_config": config}, params)
	if response.Code != http.StatusOK {
		t.Fatalf("save disabled draft: HTTP %d, %s", response.Code, response.Body.String())
	}
	stored, err := models.LoadGroup(t.Context(), handler.db, group.ID)
	if err != nil || stored.UpstreamEnabled || stored.UpstreamType != "http" || stored.UpstreamConfig != config || stored.UpstreamIP != "" || stored.UpstreamLatency != 0 || stored.UpstreamError != "" {
		t.Fatalf("draft or its check result was not updated: %v", err)
	}
	response = postJSON(t, handler.UpdateGroup, http.MethodPatch, path, gin.H{"upstream_config": `{"detour":"unavailable"}`}, params)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid draft: HTTP %d", response.Code)
	}
}

func TestGroupUpstreamEmptyGroupRuntimeLifecycle(t *testing.T) {
	const credential = "fixture-for-kernel-rejection"
	script := `#!/bin/sh
if [ "$1" = check ]; then
  if grep -q BAD_MARKER "$3"; then echo ` + credential + ` >&2; exit 1; fi
  exit 0
fi
exec sleep 300
`
	handler := newTestHandlerWithKernelScript(t, script)
	insertTestNode(t, handler.db)
	if err := handler.nodeMutations.reapplyRuntime(t.Context()); err != nil {
		t.Fatal(err)
	}
	pid := handler.singBoxService.RuntimeStatus().PID
	beforeConfig, err := os.ReadFile(handler.singBoxService.ConfigPath())
	if err != nil || pid == 0 {
		t.Fatalf("initial runtime: pid=%d err=%v", pid, err)
	}
	response := postJSON(t, handler.CreateGroup, "POST", "/api/node-groups", gin.H{"name": "empty", "upstream_enabled": true, "upstream_type": "http", "upstream_config": `{"server":"192.0.2.3","server_port":80}`}, nil)
	var created models.NodeGroup
	if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &created) != nil || !created.UpstreamEnabled {
		t.Fatalf("create HTTP %d", response.Code)
	}
	readConfig := func() []byte {
		t.Helper()
		data, err := os.ReadFile(handler.singBoxService.ConfigPath())
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	tag := []byte(fmt.Sprintf("managed-upstream-group-%d", created.ID))
	assertRuntime := func() {
		t.Helper()
		if handler.singBoxService.RuntimeStatus().PID != pid || !bytes.Equal(beforeConfig, readConfig()) || bytes.Contains(readConfig(), tag) {
			t.Error("unused group changed active configuration or PID")
		}
	}
	assertRuntime()
	if _, err := handler.db.Exec(`UPDATE node_groups SET upstream_ip='192.0.2.3',upstream_location='City',upstream_country_code='ZZ',upstream_latency=5,upstream_error='saved' WHERE id=?`, created.ID); err != nil {
		t.Fatal(err)
	}
	before, err := models.LoadGroup(t.Context(), handler.db, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	params := ginParams("id", strconv.Itoa(created.ID))
	path := testGroupPath(created.ID)
	response = postJSON(t, handler.UpdateGroup, "PATCH", path, gin.H{"upstream_config": fmt.Sprintf(`{"server":"BAD_MARKER","server_port":80,"username":"demo","password":%q}`, credential)}, params)
	if response.Code != 400 || strings.Contains(response.Body.String(), credential) {
		t.Fatalf("unsafe kernel validation response HTTP %d", response.Code)
	}
	after, err := models.LoadGroup(t.Context(), handler.db, created.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("kernel rejection changed the group or active configuration", err)
	}
	assertRuntime()
	response = postJSON(t, handler.UpdateGroup, "PATCH", path, gin.H{"upstream_config": `{"server":"192.0.2.4","server_port":80}`}, params)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	assertRuntime()
	response = postJSON(t, handler.DeleteGroup, "DELETE", path, nil, params)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	assertRuntime()
	response = postJSON(t, handler.CreateGroup, "POST", "/api/node-groups", gin.H{"name": "bad", "upstream_enabled": true, "upstream_type": "http", "upstream_config": fmt.Sprintf(`{"server":"BAD_MARKER","server_port":80,"username":"demo","password":%q}`, credential)}, nil)
	var count int
	if response.Code != 400 || strings.Contains(response.Body.String(), credential) || handler.db.QueryRow("SELECT COUNT(*) FROM node_groups").Scan(&count) != nil || count != 1 {
		t.Fatalf("failed creation: status=%d groups=%d", response.Code, count)
	}
	assertRuntime()
	group := firstTestGroup(t, handler.db)
	response = postJSON(t, handler.UpdateGroup, "PATCH", testGroupPath(group.ID), gin.H{"upstream_enabled": true, "upstream_type": "http", "upstream_config": `{"server":"192.0.2.3","server_port":80}`}, ginParams("id", strconv.Itoa(group.ID)))
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	assertRuntime()
	response = postJSON(t, handler.CreateNode, "POST", "/api/nodes", gin.H{"name": "follower", "enabled": true, "type": "direct", "config": "{}", "upstream_mode": "group"}, nil)
	if response.Code != 201 || handler.singBoxService.RuntimeStatus().PID == pid || !bytes.Contains(readConfig(), []byte(fmt.Sprintf("managed-upstream-group-%d", group.ID))) {
		t.Fatalf("active consumer did not apply group upstream: HTTP %d", response.Code)
	}
	pid, beforeConfig = handler.singBoxService.RuntimeStatus().PID, readConfig()
	response = postJSON(t, handler.UpdateGroup, "PATCH", testGroupPath(group.ID), gin.H{"upstream_config": `{"server_port":80, "server":"192.0.2.3"}`}, ginParams("id", strconv.Itoa(group.ID)))
	if response.Code != 200 || handler.singBoxService.RuntimeStatus().PID != pid || !bytes.Equal(beforeConfig, readConfig()) {
		t.Fatal("reordering fields of an active group upstream restarted the kernel")
	}
}

func TestSavedUpstreamCheckABA(t *testing.T) {
	for _, scope := range []string{"group", "global"} {
		t.Run(scope, func(t *testing.T) {
			handler := newTestHandler(t, nil)
			id := firstTestGroup(t, handler.db).ID
			update, check := handler.UpdateGroup, handler.CheckGroupUpstreamIP
			method, path, prefix := "PATCH", testGroupPath(id), ""
			params := ginParams("id", strconv.Itoa(id))
			query := "SELECT upstream_ip FROM node_groups WHERE id=?"
			if scope == "global" {
				update, check = handler.UpdateSettings, handler.CheckGlobalUpstreamIP
				method, path, prefix, params, id = "PUT", "/api/settings", "global_", nil, 1
				query = "SELECT global_upstream_ip FROM settings WHERE singleton_key=?"
			}
			save := func(address string) {
				t.Helper()
				res := postJSON(t, update, method, path, gin.H{prefix + "upstream_type": "http", prefix + "upstream_config": fmt.Sprintf(`{"server":%q,"server_port":8080}`, address)}, params)
				if res.Code != 200 {
					t.Fatal(res.Body.String())
				}
			}
			save("192.0.2.1")
			started, release := make(chan struct{}), make(chan struct{})
			first := true
			handler.checkUpstreamIP = func(context.Context, models.ProxyDefinition) (*services.IPInfo, error) {
				if first {
					first = false
					close(started)
					<-release
					return &services.IPInfo{IP: "192.0.2.10"}, nil
				}
				return &services.IPInfo{IP: "192.0.2.20"}, nil
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- postJSON(t, check, "POST", path+"/upstream/check-ip", nil, params) }()
			select {
			case <-started:
			case res := <-done:
				t.Fatalf("probe returned before the update: HTTP %d", res.Code)
			}
			defer func() {
				if release != nil {
					close(release)
					<-done
				}
			}()
			save("192.0.2.2")
			save("192.0.2.1")
			if res := postJSON(t, check, "POST", path+"/upstream/check-ip", nil, params); res.Code != 200 {
				t.Fatal(res.Body.String())
			}
			close(release)
			res := <-done
			release = nil
			var ip string
			if err := handler.db.QueryRow(query, id).Scan(&ip); err != nil || res.Code != 409 || ip != "192.0.2.20" {
				t.Fatalf("old check HTTP %d, saved IP %q, err %v", res.Code, ip, err)
			}
			handler.checkUpstreamIP = func(context.Context, models.ProxyDefinition) (*services.IPInfo, error) {
				payload := gin.H{"name": "renamed group"}
				if scope == "global" {
					payload = gin.H{"node_list_column_widths": gin.H{"name": 140}}
				}
				if res := postJSON(t, update, method, path, payload, params); res.Code != 200 {
					t.Fatal(res.Body.String())
				}
				return &services.IPInfo{IP: "192.0.2.30"}, nil
			}
			if res := postJSON(t, check, "POST", path+"/upstream/check-ip", nil, params); res.Code != 200 {
				t.Fatalf("unrelated edit invalidated the check: HTTP %d", res.Code)
			}
		})
	}
}

func TestUnusedGroupUpstreamKeepsLiveConnection(t *testing.T) {
	binary := os.Getenv("SINGBOX_TEST_BINARY")
	if binary == "" {
		t.Skip("SINGBOX_TEST_BINARY is not set")
	}
	handler := newTestHandler(t, nil)
	t.Setenv("SINGBOX_BINARY", binary)
	t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "100ms")
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reservation.Addr().(*net.TCPAddr).Port
	reservation.Close()
	id := insertTestNodeWithPortAndOrder(t, handler.db, "existing", port, 0)
	if _, err := handler.db.Exec(`UPDATE proxy_nodes SET type='direct',config='{}' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := handler.nodeMutations.reapplyRuntime(t.Context()); err != nil {
		t.Fatal(err)
	}
	pid := handler.singBoxService.RuntimeStatus().PID
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, r.URL.Path) }))
	defer target.Close()
	dialer, err := proxy.SOCKS5("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), &proxy.Auth{User: "user", Password: "pass"}, &net.Dialer{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialer.Dial("tcp", target.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	exchange := func(path string) {
		t.Helper()
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(conn, "GET /%s HTTP/1.1\r\nHost: %s\r\n\r\n", path, target.Listener.Addr()); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || string(body) != "/"+path || handler.singBoxService.RuntimeStatus().PID != pid {
			t.Fatalf("live exchange %s failed: %v", path, err)
		}
	}
	exchange("before")
	groups := "/api/node-groups"
	bad := postJSON(t, handler.CreateGroup, "POST", groups, gin.H{"name": "invalid", "upstream_enabled": true, "upstream_type": "ss", "upstream_config": `{"server":"192.0.2.1","server_port":1080,"method":"unknown-review-cipher","password":"review-cipher-credential"}`}, nil)
	if bad.Code != 400 || !strings.Contains(bad.Body.String(), services.ErrConfigRejected.Error()) || strings.Contains(bad.Body.String(), "review-cipher-credential") {
		t.Fatalf("official kernel rejection: HTTP %d", bad.Code)
	}
	exchange("after-rejection")
	res := postJSON(t, handler.CreateGroup, "POST", groups, gin.H{"name": "unused", "upstream_enabled": true, "upstream_type": "http", "upstream_config": `{"server":"192.0.2.1","server_port":8080}`}, nil)
	var group models.NodeGroup
	if res.Code != 201 || json.Unmarshal(res.Body.Bytes(), &group) != nil {
		t.Fatalf("create: %d", res.Code)
	}
	exchange("created")
	path, params := testGroupPath(group.ID), ginParams("id", strconv.Itoa(group.ID))
	if res := postJSON(t, handler.UpdateGroup, "PATCH", path, gin.H{"upstream_config": `{"server":"192.0.2.2","server_port":8080}`}, params); res.Code != 200 {
		t.Fatal(res.Body.String())
	}
	exchange("updated")
	if res := postJSON(t, handler.DeleteGroup, "DELETE", path, nil, params); res.Code != 200 {
		t.Fatal(res.Body.String())
	}
	exchange("deleted")
}

func TestSavedUpstreamDraftChecksRejectActiveInbounds(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	handler := newTestHandlerWithKernelScript(t, "#!/bin/sh\ntouch '"+marker+"'\nexit 1\n")
	insertTestNode(t, handler.db)
	config := `{"server":"127.0.0.1","server_port":30001}`
	for _, group := range []bool{true, false} {
		query, path, check := `UPDATE settings SET global_upstream_type='http',global_upstream_config=?`, "/api/settings", handler.CheckGlobalUpstreamIP
		var params gin.Params
		if group {
			id := firstTestGroup(t, handler.db).ID
			query, path, check, params = `UPDATE node_groups SET upstream_type='http',upstream_config=?`, testGroupPath(id), handler.CheckGroupUpstreamIP, ginParams("id", strconv.Itoa(id))
		}
		if _, err := handler.db.Exec(query, config); err != nil {
			t.Fatal(err)
		}
		res := postJSON(t, check, "POST", path+"/upstream/check-ip", nil, params)
		if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "inbound port 30001") {
			t.Errorf("%s: %d %s", path, res.Code, res.Body.String())
		}
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("collision reached the kernel")
	}
}

func TestShareLinkParseErrorsExcludeCredentials(t *testing.T) {
	handler := newTestHandler(t, nil)
	for _, fixture := range []struct{ link, reason string }{
		{"socks5://fixture-user:fixture-credential@192.0.2.1:bad", "invalid port"},
		{"http://fixture-user:fixture-credential%zz@192.0.2.1:80", "invalid URL escape"},
	} {
		for _, endpoint := range []struct {
			path    string
			handle  gin.HandlerFunc
			payload gin.H
		}{
			{"/api/parse-link", handler.ParseShareLink, gin.H{"link": fixture.link}},
			{"/api/nodes/1/replace", handler.ReplaceNode, gin.H{"link": fixture.link}},
			{"/api/nodes/batch-import", handler.BatchImportNodes, gin.H{"links": []string{fixture.link}}},
		} {
			res := postJSON(t, endpoint.handle, "POST", endpoint.path, endpoint.payload, ginParams("id", "1"))
			body := res.Body.String()
			if res.Code != 400 || !strings.Contains(body, fixture.reason) || strings.Contains(body, "fixture-user") || strings.Contains(body, "fixture-credential") || strings.Contains(body, fixture.link) {
				t.Errorf("%s: status=%d reason present=%t credentials exposed=%t", endpoint.path, res.Code, strings.Contains(body, fixture.reason), strings.Contains(body, "fixture-credential"))
			}
		}
	}
}

func TestHysteriaPortDiagnosticsAcrossAPIs(t *testing.T) {
	handler := newTestHandler(t, nil)
	const fieldValue = "fixtureInvalidPortValue"
	for _, port := range []string{fieldValue, "443-" + fieldValue} {
		link := "hy2://test@192.0.2.1:" + port
		for _, endpoint := range []struct {
			method, path string
			handle       gin.HandlerFunc
			payload      gin.H
		}{
			{"POST", "/api/parse-link", handler.ParseShareLink, gin.H{"link": link}},
			{"PUT", "/api/nodes/1/replace", handler.ReplaceNode, gin.H{"link": link}},
			{"POST", "/api/nodes/batch-import", handler.BatchImportNodes, gin.H{"links": []string{link}}},
		} {
			res := postJSON(t, endpoint.handle, endpoint.method, endpoint.path, endpoint.payload, ginParams("id", "1"))
			body := res.Body.String()
			if res.Code != http.StatusBadRequest || !strings.Contains(body, "invalid hysteria2 port") || strings.Contains(body, fieldValue) || strings.Contains(body, link) {
				t.Errorf("%s: expected a safe port diagnostic, HTTP %d", endpoint.path, res.Code)
			}
		}
	}
}

func TestWireGuardReservedDiagnosticsAcrossAPIs(testContext *testing.T) {
	handler := newTestHandler(testContext, nil)
	nodeID := insertTestNode(testContext, handler.db)
	before, err := models.LoadProxyNodes(testContext.Context(), handler.db)
	if err != nil {
		testContext.Fatal(err)
	}
	const credential = "wireguardReservedSensitiveCredential"
	const privateKey = "wireguardLinkPrivateKeyFixture"
	const numericCredential = "987654321098765432109876543210"
	for _, fixture := range []struct {
		name, reserved, sensitive string
	}{
		{"comma", "1," + credential + ",3", credential},
		{"semicolon", "1;" + credential + ";3", credential},
		{"whitespace", "1\t" + credential + "\n3", credential},
		{"json-string", `[1,"` + credential + `",3]`, credential},
		{"json-number-overflow", "[1," + numericCredential + ",3]", numericCredential},
		{"out-of-range", "1,987654321,3", "987654321"},
		{"invalid-encoding", credential, credential},
	} {
		testContext.Run(fixture.name, func(testContext *testing.T) {
			query := url.Values{
				"address":   {"10.0.0.2/32"},
				"publickey": {"wireguardPeerPublicKeyFixture"},
				"reserved":  {fixture.reserved},
			}
			link := "wireguard://" + privateKey + "@192.0.2.1:51820?" + query.Encode()
			for _, endpoint := range []struct {
				method, path string
				handle       gin.HandlerFunc
				payload      gin.H
			}{
				{http.MethodPost, "/api/parse-link", handler.ParseShareLink, gin.H{"link": link}},
				{http.MethodPut, testNodePath(nodeID) + "/replace", handler.ReplaceNode, gin.H{"link": link}},
				{http.MethodPost, "/api/nodes/batch-import", handler.BatchImportNodes, gin.H{"links": []string{link}}},
			} {
				testContext.Run(endpoint.path, func(testContext *testing.T) {
					response := postJSON(testContext, endpoint.handle, endpoint.method, endpoint.path, endpoint.payload, ginParams("id", strconv.Itoa(nodeID)))
					var result struct {
						Error string `json:"error"`
					}
					if response.Code != http.StatusBadRequest || json.Unmarshal(response.Body.Bytes(), &result) != nil || !strings.Contains(result.Error, "reserved") {
						testContext.Fatalf("expected a reserved diagnostic, HTTP %d", response.Code)
					}
					for _, sensitive := range []string{fixture.sensitive, fixture.reserved, url.QueryEscape(fixture.reserved), privateKey, link} {
						if strings.Contains(response.Body.String(), sensitive) {
							testContext.Error("failed share-link response exposed sensitive input")
						}
					}
				})
			}
		})
	}
	after, err := models.LoadProxyNodes(testContext.Context(), handler.db)
	if err != nil || !reflect.DeepEqual(before, after) {
		testContext.Fatal("invalid reserved input changed saved nodes", err)
	}
}

func TestSubscriptionFetchFailureAcrossAPIs(t *testing.T) {
	handler := newTestHandler(t, nil)
	const pathValue, queryValue = "fixtureSubscriptionPath", "fixtureSubscriptionQuery"
	source := "http://localhost/" + pathValue + "?key=" + queryValue
	id := firstTestGroup(t, handler.db).ID
	if _, err := handler.db.Exec("UPDATE node_groups SET subscription_url=? WHERE id=?", source, id); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []struct {
		path    string
		handle  gin.HandlerFunc
		payload gin.H
		status  int
	}{
		{"/api/nodes/batch-import", handler.BatchImportNodes, gin.H{"links": []string{source}, "source_type": "subscription"}, http.StatusBadRequest},
		{testGroupPath(id) + "/subscription/refresh", handler.RefreshSubscription, nil, http.StatusBadGateway},
	} {
		res := postJSON(t, endpoint.handle, "POST", endpoint.path, endpoint.payload, ginParams("id", strconv.Itoa(id)))
		body := res.Body.String()
		if res.Code != endpoint.status || strings.Contains(body, pathValue) || strings.Contains(body, queryValue) || !strings.Contains(body, "input 1") || !strings.Contains(body, "non-public") {
			t.Errorf("%s returned incomplete or unsafe diagnostics, HTTP %d", endpoint.path, res.Code)
		}
	}
	group, err := models.LoadGroup(t.Context(), handler.db, id)
	if err != nil || !strings.Contains(group.SubscriptionLastError, "non-public") || strings.Contains(group.SubscriptionLastError, pathValue) || strings.Contains(group.SubscriptionLastError, queryValue) {
		t.Error("unsafe or missing saved fetch diagnostic", err)
	}
}

func TestGroupUpstreamEditPreconditions(t *testing.T) {
	client := newGroupTestClient(t)
	path := testGroupPath(firstTestGroup(t, client.db).ID)
	const config = `{"server":"192.0.2.2","server_port":1080}`
	var current gin.H
	client.request("PATCH", path, gin.H{"upstream_type": "socks5", "upstream_config": config, "upstream_enabled": true, "expected_upstream_enabled": false, "expected_upstream_revision": 0}, 200, &current)
	if current["upstream_revision"] != float64(1) {
		t.Error("group did not expose the saved definition revision")
	}
	for _, baseline := range []gin.H{{"expected_upstream_revision": 0}, {"expected_upstream_enabled": false}} {
		baseline["upstream_enabled"] = false
		var conflict gin.H
		client.request("PATCH", path, baseline, 409, &conflict)
		if conflict["code"] != "upstream_conflict" || conflict["error"] != "upstream_conflict" || conflict["upstream_enabled"] != true || conflict["upstream_revision"] != float64(1) {
			t.Fatal("missing current edit baseline", conflict)
		}
	}
	client.request("PATCH", path, gin.H{"name": "renamed"}, 200, &current)
	if current["name"] != "renamed" || current["upstream_enabled"] != true || current["upstream_config"] != config || current["upstream_revision"] != float64(1) {
		t.Fatal("an unrelated edit overwrote upstream settings")
	}
	client.request("PATCH", path, gin.H{"upstream_enabled": false}, 200, &current)
	if current["upstream_enabled"] != false || current["upstream_revision"] != float64(1) {
		t.Fatal("unconditional override did not preserve the definition revision")
	}
}

func TestSavedUpstreamDraftChecksRunningPorts(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "probe")
	handler := newTestHandlerWithKernelScript(t, "#!/bin/sh\ncase \"$3\" in */sbpm-upstream-check-*/config.json) touch '"+marker+"'; exit 1;; esac\nif [ \"$1\" = check ]; then exit 0; fi\nexec sleep 300\n")
	service := handler.singBoxService
	const port = 38127
	data, err := service.BuildGlobalConfigContext(t.Context(), []models.ProxyNode{{ID: 17, Name: "preserved", Enabled: true, Type: "direct", Config: "{}", InboundPort: port}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(service.ConfigPath()+".last-good", data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := service.StartPreservedConfig(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(service.ConfigPath(), []byte(`{"inbounds":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if !service.ActiveInboundPorts()[port] {
		t.Error("active runtime port lost when on-disk candidate changed")
	}
	config := fmt.Sprintf(`{"server":"127.0.0.1","server_port":%d}`, port)
	for _, scope := range []string{"group", "global"} {
		query, path, check := `UPDATE settings SET global_upstream_type='http',global_upstream_config=?`, "/api/settings", handler.CheckGlobalUpstreamIP
		var params gin.Params
		if scope == "group" {
			id := firstTestGroup(t, handler.db).ID
			query, path, check, params = `UPDATE node_groups SET upstream_type='http',upstream_config=?`, testGroupPath(id), handler.CheckGroupUpstreamIP, ginParams("id", strconv.Itoa(id))
		}
		if _, err := handler.db.Exec(query, config); err != nil {
			t.Fatal(err)
		}
		res := postJSON(t, check, "POST", path+"/upstream/check-ip", nil, params)
		if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "inbound port 38127") {
			t.Errorf("%s: %d %s", scope, res.Code, res.Body.String())
		}
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Error("runtime collision reached probe process")
	}
	if err := service.Stop(); err != nil || len(service.ActiveInboundPorts()) != 0 {
		t.Fatalf("stale ports after stop: %v", err)
	}
}

func TestGroupUpstreamConcurrentEditors(t *testing.T) {
	handler := newTestHandler(t, nil)
	id := firstTestGroup(t, handler.db).ID
	path, params := testGroupPath(id), ginParams("id", strconv.Itoa(id))
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 3)
	for _, patch := range []gin.H{
		{"upstream_type": "http", "upstream_config": `{"server":"192.0.2.1","server_port":1080}`, "expected_upstream_revision": 0},
		{"upstream_type": "http", "upstream_config": `{"server":"192.0.2.2","server_port":1080}`, "expected_upstream_revision": 0},
		{"name": "concurrent rename"},
	} {
		go func() { <-start; responses <- postJSON(t, handler.UpdateGroup, "PATCH", path, patch, params) }()
	}
	close(start)
	var conflicts int
	var saved string
	for range 3 {
		response := <-responses
		switch response.Code {
		case http.StatusConflict:
			conflicts++
			if !strings.Contains(response.Body.String(), `"code":"upstream_conflict"`) {
				t.Error("unrelated conflict response")
			}
		case http.StatusOK:
			var group models.NodeGroup
			if err := json.Unmarshal(response.Body.Bytes(), &group); err != nil {
				t.Error(err)
			}
			if group.UpstreamConfig != "" {
				saved = group.UpstreamConfig
			}
		default:
			t.Errorf("edit returned HTTP %d", response.Code)
		}
	}
	group, err := models.LoadGroup(t.Context(), handler.db, id)
	if err != nil || conflicts != 1 || saved == "" || group.UpstreamConfig != saved || group.UpstreamRevision != 1 || group.Name != "concurrent rename" {
		t.Fatalf("concurrent edits: conflicts=%d revision=%d err=%v", conflicts, group.UpstreamRevision, err)
	}
}

func TestUnusedGroupUpstreamNodeMutations(t *testing.T) {
	client := newGroupTestClient(t)
	first := firstTestGroup(t, client.db)
	var second models.NodeGroup
	client.request("POST", "/api/node-groups", gin.H{"name": "unused", "allocate_port": false, "upstream_enabled": true, "upstream_type": "http", "upstream_config": fmt.Sprintf(`{"server":"127.0.0.1","server_port":%d}`, first.StartPort)}, 201, &second)
	firstNode := client.createNode("existing", first.ID, true)
	if firstNode.InboundPort != first.StartPort {
		t.Fatal("fixture did not allocate the colliding port")
	}
	client.request("PATCH", testGroupPath(first.ID), gin.H{"upstream_type": "socks5", "upstream_config": `{"server":"192.0.2.1","server_port":1080}`, "upstream_enabled": true}, 200, nil)
	follower := gin.H{"name": "follower", "type": "direct", "config": "{}", "enabled": true, "upstream_mode": "global", "group_id": first.ID}
	var added models.ProxyNode
	client.request("POST", "/api/nodes", follower, 201, &added)
	client.request("POST", "/api/nodes/batch-move", gin.H{"ids": []int{added.ID}, "group_id": second.ID}, 400, nil)
	follower["group_id"] = second.ID
	client.request("POST", "/api/nodes", follower, 400, nil)
	nodes, err := models.LoadProxyNodes(t.Context(), client.db)
	if err != nil || len(nodes) != 2 || nodes[0].GroupID != first.ID || nodes[1].GroupID != first.ID {
		t.Fatal("rejected first use did not preserve nodes", err)
	}
}

func TestIPCheckWriteDeadline(t *testing.T) {
	t.Setenv("SBPM_UPSTREAM_CHECK_TIMEOUT", "5m")
	check := func() (*services.IPInfo, error) {
		time.Sleep(100 * time.Millisecond)
		return &services.IPInfo{IP: "192.0.2.17"}, nil
	}
	handler := newTestHandler(t, func(string, string, string) (*services.IPInfo, error) { return check() })
	handler.checkUpstreamIP = func(context.Context, models.ProxyDefinition) (*services.IPInfo, error) { return check() }
	if _, err := handler.db.Exec(`UPDATE node_groups SET upstream_type='direct',upstream_config='{}'`); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.db.Exec(`UPDATE settings SET global_upstream_type='direct',global_upstream_config='{}'`); err != nil {
		t.Fatal(err)
	}
	insertTestNode(t, handler.db)
	router := gin.New()
	router.POST("/groups/:id", handler.CheckGroupUpstreamIP)
	router.POST("/global", handler.CheckGlobalUpstreamIP)
	router.POST("/nodes/:id", handler.CheckNodeIP)
	server := httptest.NewUnstartedServer(router)
	server.Config.WriteTimeout = 25 * time.Millisecond
	server.Start()
	t.Cleanup(server.Close)
	for _, path := range []string{"/groups/1", "/global", "/nodes/1"} {
		res, err := server.Client().Post(server.URL+path, "application/json", nil)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil || res.StatusCode != 200 || !bytes.Contains(body, []byte("192.0.2.17")) {
			t.Errorf("%s: HTTP %d, %v", path, res.StatusCode, err)
		}
	}
}
