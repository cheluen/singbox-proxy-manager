package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
)

const testSubscriptionURL = "https://provider.invalid/private-subscription?token=secret-example"

func subscriptionItem(name, password string) services.ImportItem {
	return services.ImportItem{Name: name, Type: "ss", Config: models.SSConfig{
		Server: "192.0.2.10", ServerPort: 443, Method: "aes-128-gcm", Password: password,
	}}
}

func configureTestSubscription(t *testing.T, handler *Handler, allocate bool) int {
	t.Helper()
	handler.db.SetMaxOpenConns(1)
	group, err := models.LoadGroup(t.Context(), handler.db, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = handler.db.Exec(`UPDATE node_groups SET subscription_url=?,subscription_auto_update=TRUE,allocate_port=?,start_port=30000 WHERE id=?`, testSubscriptionURL, allocate, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	return group.ID
}

func subscriptionTestNodes(t *testing.T, handler *Handler, groupID int) map[string]models.ProxyNode {
	t.Helper()
	nodes, err := models.LoadGroupNodes(t.Context(), handler.db, groupID)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]models.ProxyNode)
	for _, node := range nodes {
		if node.SubscriptionKey != "" {
			result[node.SubscriptionName] = node
		}
	}
	return result
}

func TestSubscriptionRefreshCanonicalAliases(t *testing.T) {
	first := subscriptionItem("原名", "shared-key")
	second := subscriptionItem("另一别名", "shared-key")
	previous, _, err := subscriptionNodes([]services.ImportItem{first, second})
	if err != nil {
		t.Fatal(err)
	}
	for index := range previous {
		previous[index].ID, previous[index].GroupID, previous[index].UpstreamMode = index+100, 1, models.UpstreamModeNone
	}
	first.Name = "订阅改名"
	first.Config = json.RawMessage(`{ "password":"shared-key", "method":"aes-128-gcm", "server_port":443, "server":"192.0.2.10" }`)
	incoming, _, err := subscriptionNodes([]services.ImportItem{second, first})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planSubscription(previous, incoming, 1)
	if err != nil || plan.unchanged != 1 || len(plan.update) != 1 || len(plan.add)+len(plan.remove) != 0 || plan.update[0].ID != previous[0].ID {
		t.Fatal("相同出站的别名应通过原名和规范化签名确定改名项")
	}
}

func TestSubscriptionRefreshIncremental(t *testing.T) {
	for _, preserve := range []bool{false, true} {
		t.Run(strconv.FormatBool(preserve), func(t *testing.T) {
			testSubscriptionRefreshIncremental(t, preserve)
		})
	}
}

func testSubscriptionRefreshIncremental(t *testing.T, preserve bool) {
	t.Helper()
	handler := newTestHandler(t, nil)
	t.Setenv("PORT", "30000")
	groupID := configureTestSubscription(t, handler, true)
	setTestGroupPreserveInboundPorts(t, handler.db, groupID, preserve)
	manualID := insertTestNode(t, handler.db)
	if _, err := handler.db.Exec(`INSERT INTO node_groups(name,sort_order,start_port,upstream_config) VALUES ('Other',1,30000,'')`); err != nil {
		t.Fatal(err)
	}
	var otherID int
	if err := handler.db.QueryRow("SELECT id FROM node_groups WHERE name='Other'").Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	alpha := subscriptionItem("原名", "first")
	beta := subscriptionItem("供应方改名", "second")
	steady := subscriptionItem("保持", "steady")
	entries, _, err := subscriptionNodes([]services.ImportItem{alpha})
	if err != nil {
		t.Fatal(err)
	}
	other := entries[0]
	other.GroupID, other.InboundPort, other.UpstreamMode = otherID, 30002, models.UpstreamModeNone
	if _, err := handler.db.Exec(insertNodeSQL, nodeInsertArgs(other)...); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.db.Exec("UPDATE proxy_nodes SET name=?,config=? WHERE id=?", other.Name, other.Config, manualID); err != nil {
		t.Fatal(err)
	}
	manual, err := models.LoadProxyNode(t.Context(), handler.db, manualID)
	if err != nil {
		t.Fatal(err)
	}
	otherBefore := subscriptionTestNodes(t, handler, otherID)
	feed := []services.ImportItem{alpha, beta, steady, subscriptionItem("消失", "removed")}
	handler.fetchSubscription = func(_ context.Context, urls []string, kind services.BatchImportSourceType) ([]services.ImportItem, []services.ImportFailure, error) {
		if !slices.Equal(urls, []string{testSubscriptionURL}) || kind != services.BatchImportSourceSubscription {
			t.Fatal("应按已保存的单一订阅地址和显式订阅类型抓取")
		}
		return feed, nil, nil
	}
	refresh := func() subscriptionResult {
		t.Helper()
		id := strconv.Itoa(groupID)
		response := postJSON(t, handler.RefreshSubscription, http.MethodPost, "/api/node-groups/"+id+"/subscription/refresh", nil, gin.Params{{Key: "id", Value: id}})
		var result subscriptionResult
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK || result.Warnings == nil {
			t.Fatalf("刷新响应 %d %s", response.Code, response.Body.String())
		}
		return result
	}
	if result := refresh(); !reflect.DeepEqual(result, subscriptionResult{Added: 4, Warnings: []string{}}) {
		t.Fatalf("首次同步计数 %+v", result)
	}
	before := subscriptionTestNodes(t, handler, groupID)
	policyConfig, err := json.Marshal(models.PolicyConfig{MemberIDs: []int{before[alpha.Name].ID, before["消失"].ID}, Interval: 30})
	if err != nil {
		t.Fatal(err)
	}
	policy := models.ProxyNode{GroupID: otherID, Name: "subscription-policy", Type: "failover", Config: string(policyConfig), Enabled: true, UpstreamMode: "none", SortOrder: 2, InboundPort: 35001}
	if _, err = handler.db.Exec(insertNodeSQL, nodeInsertArgs(policy)...); err != nil {
		t.Fatal(err)
	}
	owned := before[alpha.Name]
	if owned.InboundPort != 30003 || owned.Username == "" || owned.Password == "" {
		t.Fatal("新节点应跳过管理端口和两组已用端口，并获得独立凭据")
	}
	if _, err := handler.db.Exec(`UPDATE proxy_nodes SET name='本地别名',remark='保留备注',inbound_port_pinned=TRUE,sort_order=28,enabled=FALSE,tcp_reuse_enabled=FALSE WHERE id=?`, owned.ID); err != nil {
		t.Fatal(err)
	}
	alpha.Config = json.RawMessage(`{"server_port":8443,"password":"rotated","method":"aes-128-gcm","server":"192.0.2.10"}`)
	beta.Name = "订阅新名"
	feed = []services.ImportItem{steady, beta, alpha, subscriptionItem("新增", "new")}
	if result := refresh(); !reflect.DeepEqual(result, subscriptionResult{Added: 1, Updated: 2, Removed: 1, Unchanged: 1, Warnings: []string{}}) {
		t.Fatalf("增量同步计数 %+v", result)
	}
	after := subscriptionTestNodes(t, handler, groupID)
	var persistedPolicy string
	var pruned models.PolicyConfig
	if err = handler.db.QueryRow("SELECT config FROM proxy_nodes WHERE name='subscription-policy'").Scan(&persistedPolicy); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(persistedPolicy), &pruned); err != nil || !slices.Equal(pruned.MemberIDs, []int{owned.ID}) {
		t.Fatalf("subscription removal left stale policy members %v, %v", pruned.MemberIDs, err)
	}
	updated := after[alpha.Name]
	alpha.Config = json.RawMessage(`{"server_port":8443,"password":"rotated","method":"aes-128-gcm","server":"new-host.example"}`)
	feed[2] = alpha
	if result := refresh(); result.Updated != 1 || result.Unchanged != 3 || result.Added+result.Removed != 0 {
		t.Fatalf("换服务器的增量计数 %+v", result)
	}
	updated = subscriptionTestNodes(t, handler, groupID)[alpha.Name]
	if !strings.Contains(updated.Config, "new-host.example") {
		t.Fatal("订阅地址未更新")
	}
	var afterHostChange string
	if err := handler.db.QueryRow("SELECT config FROM proxy_nodes WHERE name='subscription-policy'").Scan(&afterHostChange); err != nil || persistedPolicy != afterHostChange {
		t.Fatal("更换服务器改变了策略引用")
	}
	if updated.ID != owned.ID || updated.InboundPort != owned.InboundPort || updated.Username != owned.Username || updated.Password != owned.Password || !updated.InboundPortPinned || updated.Name != "本地别名" || updated.Remark != "保留备注" || updated.SortOrder != 28 || updated.Enabled || updated.TCPReuseEnabled || updated.SubscriptionKey == owned.SubscriptionKey || !strings.Contains(updated.Config, "rotated") {
		t.Fatal("原地更新没有正确区分上游配置与本地字段")
	}
	if after[beta.Name].ID != before["供应方改名"].ID || after[beta.Name].Name != beta.Name || after["新增"].SortOrder <= updated.SortOrder || len(after) != 4 {
		t.Fatal("改名、移除或追加排序结果错误")
	}
	manualAfter, err := models.LoadProxyNode(t.Context(), handler.db, manualID)
	if err != nil || !reflect.DeepEqual(manualAfter, manual) || !reflect.DeepEqual(subscriptionTestNodes(t, handler, otherID), otherBefore) {
		t.Fatal("刷新修改了手动节点或其他分组的节点")
	}
	pid := handler.singBoxService.RuntimeStatus().PID
	feed = append(feed, steady)
	if result := refresh(); result.Unchanged != 4 || result.Added+result.Updated+result.Removed != 0 || len(result.Warnings) != 1 || pid == 0 || handler.singBoxService.RuntimeStatus().PID != pid {
		t.Fatalf("相同快照应跳过内核重启，并提示重复订阅条目 %+v", result)
	}
}

func TestSubscriptionRefreshRetainsLastCompleteSnapshot(t *testing.T) {
	handler := newTestHandlerWithScriptedKernel(t)
	groupID := configureTestSubscription(t, handler, false)
	good := subscriptionItem("同名", "first")
	feed := []services.ImportItem{good, subscriptionItem("同名", "second")}
	var failures []services.ImportFailure
	var fetchErr error
	handler.fetchSubscription = func(context.Context, []string, services.BatchImportSourceType) ([]services.ImportItem, []services.ImportFailure, error) {
		return feed, failures, fetchErr
	}
	if result, err := handler.refreshGroupSubscription(t.Context(), groupID, false); err != nil || result.Added != 2 {
		t.Fatalf("建立无端口分组快照 %+v %v", result, err)
	}
	before, err := models.LoadGroupNodes(t.Context(), handler.db, groupID)
	if err != nil || len(before) != 2 || before[0].InboundPort != 0 || before[1].InboundPort != 0 {
		t.Fatalf("无端口节点创建失败 %v", err)
	}
	initial, err := models.LoadGroup(t.Context(), handler.db, groupID)
	if err != nil || initial.SubscriptionLastUpdatedAt == nil {
		t.Fatalf("缺少最后成功时间 %v", err)
	}
	for _, test := range []struct {
		name     string
		feed     []services.ImportItem
		failures []services.ImportFailure
		err      error
		status   int
	}{
		{"抓取失败", nil, nil, errors.New(testSubscriptionURL), http.StatusBadGateway},
		{"空集合", nil, nil, nil, http.StatusUnprocessableEntity},
		{"部分失败", []services.ImportItem{good}, []services.ImportFailure{{Source: testSubscriptionURL, Error: "secret-example"}}, nil, http.StatusUnprocessableEntity},
		{"无法解析", []services.ImportItem{{Name: "秘密", Type: "ss", Config: "secret-example"}}, nil, nil, http.StatusUnprocessableEntity},
		{"模糊身份", []services.ImportItem{subscriptionItem("同名", "third"), subscriptionItem("同名", "fourth")}, nil, nil, http.StatusConflict},
		{"内核拒绝", []services.ImportItem{subscriptionItem("新节点", "BAD_MARKER-secret-example")}, nil, nil, http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			feed, failures, fetchErr = test.feed, test.failures, test.err
			response := postJSON(t, handler.RefreshSubscription, http.MethodPost, "/refresh", nil, gin.Params{{Key: "id", Value: strconv.Itoa(groupID)}})
			group, err := models.LoadGroup(t.Context(), handler.db, groupID)
			if err != nil || response.Code != test.status || group.SubscriptionLastError == "" || strings.Contains(group.SubscriptionLastError+response.Body.String(), "secret-example") || !reflect.DeepEqual(group.SubscriptionLastUpdatedAt, initial.SubscriptionLastUpdatedAt) {
				t.Fatalf("失败状态、错误脱敏或最后成功时间错误，HTTP %d", response.Code)
			}
			after, err := models.LoadGroupNodes(t.Context(), handler.db, groupID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("失败刷新修改了之前的节点快照")
			}
			ids, err := handler.dueSubscriptionIDs(t.Context(), time.Now())
			if err != nil || len(ids) != 0 {
				t.Fatal("失败后应按配置间隔重试")
			}
			ids, err = handler.dueSubscriptionIDs(t.Context(), group.UpdatedAt.Add(time.Duration(group.SubscriptionIntervalMinutes)*time.Minute))
			if err != nil || !slices.Equal(ids, []int{groupID}) {
				t.Fatal("失败的订阅到达间隔后应重新进入调度")
			}
		})
	}
	feed, failures, fetchErr = []services.ImportItem{good}, nil, nil
	if result, err := handler.refreshGroupSubscription(t.Context(), groupID, false); err != nil || result.Removed != 1 || result.Unchanged != 1 {
		t.Fatalf("有效快照未恢复同步 %+v %v", result, err)
	}
	group, err := models.LoadGroup(t.Context(), handler.db, groupID)
	if err != nil || group.SubscriptionLastError != "" {
		t.Fatal("成功后应清除上次错误")
	}
}

func TestSubscriptionRejectsConflictingIdentityBeforeNameFallback(t *testing.T) {
	for _, changedServer := range []bool{false, true} {
		t.Run(strconv.FormatBool(changedServer), func(t *testing.T) {
			handler := newTestHandlerWithScriptedKernel(t)
			group := configureTestSubscription(t, handler, true)
			third := subscriptionItem("丙", "distinct")
			if changedServer {
				cfg := third.Config.(models.SSConfig)
				cfg.Server = "192.0.2.11"
				third.Config = cfg
			}
			feed := []services.ImportItem{subscriptionItem("甲", "shared"), subscriptionItem("乙", "shared"), third}
			handler.fetchSubscription = func(context.Context, []string, services.BatchImportSourceType) ([]services.ImportItem, []services.ImportFailure, error) {
				return feed, nil, nil
			}
			if result, err := handler.refreshGroupSubscription(t.Context(), group, false); err != nil || result.Added != 3 {
				t.Fatalf("initial snapshot: %+v %v", result, err)
			}
			before, err := models.LoadGroupNodes(t.Context(), handler.db, group)
			if err != nil {
				t.Fatal(err)
			}
			feed = []services.ImportItem{subscriptionItem("丙", "shared")}
			res := postJSON(t, handler.RefreshSubscription, http.MethodPost, "/refresh", nil, gin.Params{{Key: "id", Value: strconv.Itoa(group)}})
			after, err := models.LoadGroupNodes(t.Context(), handler.db, group)
			if res.Code != http.StatusConflict || err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("conflicting identity changed the saved set: HTTP %d, %v", res.Code, err)
			}
		})
	}
}

func TestSubscriptionSharedIncomingKeyPreservesPolicyMember(t *testing.T) {
	handler := newTestHandlerWithScriptedKernel(t)
	group := configureTestSubscription(t, handler, false)
	feed := []services.ImportItem{subscriptionItem("甲", "original")}
	handler.fetchSubscription = func(context.Context, []string, services.BatchImportSourceType) ([]services.ImportItem, []services.ImportFailure, error) {
		return feed, nil, nil
	}
	if _, err := handler.refreshGroupSubscription(t.Context(), group, false); err != nil {
		t.Fatal(err)
	}
	member := subscriptionTestNodes(t, handler, group)["甲"]
	policy := gin.H{"name": "priority", "type": "failover", "enabled": true, "config": fmt.Sprintf(`{"member_ids":[%d]}`, member.ID)}
	if response := postJSON(t, handler.CreateNode, http.MethodPost, "/nodes", policy, nil); response.Code != http.StatusCreated {
		t.Fatalf("create policy: HTTP %d", response.Code)
	}
	before, err := models.LoadGroupNodes(t.Context(), handler.db, group)
	if err != nil || len(before) != 2 {
		t.Fatalf("expected member and referring policy: %v", err)
	}
	feed = []services.ImportItem{subscriptionItem("甲", "replacement"), subscriptionItem("乙", "original"), subscriptionItem("丙", "original")}
	response := postJSON(t, handler.RefreshSubscription, http.MethodPost, "/refresh", nil, gin.Params{{Key: "id", Value: strconv.Itoa(group)}})
	after, err := models.LoadGroupNodes(t.Context(), handler.db, group)
	if response.Code != http.StatusConflict || err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("shared identity changed the member or policy: HTTP %d, %v", response.Code, err)
	}
}

func TestSubscriptionRefreshRejectsStaleFetchAndFailure(t *testing.T) {
	for _, test := range []struct {
		name     string
		fail     bool
		preserve bool
		payload  map[string]any
	}{
		{"subscription success", false, false, map[string]any{"subscription_url": "", "subscription_auto_update": false}},
		{"subscription failure", true, false, map[string]any{"subscription_url": "", "subscription_auto_update": false}},
		{"port policy success", false, true, map[string]any{"preserve_inbound_ports": true}},
		{"port policy failure", true, true, map[string]any{"preserve_inbound_ports": true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := newTestHandler(t, nil)
			groupID := configureTestSubscription(t, handler, true)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			started, resume := make(chan struct{}), make(chan struct{})
			handler.fetchSubscription = func(ctx context.Context, _ []string, _ services.BatchImportSourceType) ([]services.ImportItem, []services.ImportFailure, error) {
				close(started)
				select {
				case <-resume:
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				}
				if test.fail {
					return nil, nil, errors.New("secret-example")
				}
				return []services.ImportItem{subscriptionItem("过期", "first")}, nil, nil
			}
			finished := make(chan error, 1)
			go func() {
				_, err := handler.refreshGroupSubscription(ctx, groupID, false)
				finished <- err
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("刷新未开始抓取")
			}
			var conflict groupConflict
			if _, err := handler.refreshGroupSubscription(ctx, groupID, true); !errors.As(err, &conflict) || conflict.code != errSubscriptionBusy.code {
				t.Fatal("手动和自动刷新必须共享组锁")
			}
			if !handler.nodeWriteMu.TryLock() {
				t.Fatal("网络抓取期间持有节点写锁")
			}
			handler.nodeWriteMu.Unlock()
			id := strconv.Itoa(groupID)
			response := postJSON(t, handler.UpdateGroup, http.MethodPatch, "/api/node-groups/"+id, test.payload, ginParams("id", id))
			if response.Code != http.StatusOK {
				t.Fatalf("抓取期间修改分组失败：%d %s", response.Code, response.Body.String())
			}
			close(resume)
			select {
			case err := <-finished:
				if !errors.As(err, &conflict) || conflict.code != errSubscriptionChanged.code {
					t.Fatalf("预期组修订冲突 %v", err)
				}
			case <-ctx.Done():
				t.Fatal("过期刷新未结束")
			}
			group, err := models.LoadGroup(ctx, handler.db, groupID)
			if err != nil || group.PreserveInboundPorts != test.preserve || group.NodeCount != 0 || group.SubscriptionLastError != "" || group.SubscriptionLastUpdatedAt != nil {
				t.Fatal("抓取中的旧任务覆盖了新订阅状态")
			}
		})
	}
}

func TestSubscriptionSchedulerCancellation(t *testing.T) {
	handler := newTestHandler(t, nil)
	id := configureTestSubscription(t, handler, true)
	if _, err := handler.db.Exec(`INSERT INTO node_groups(name,sort_order,start_port,subscription_url,upstream_config) VALUES ('disabled',1,31000,?,'')`, testSubscriptionURL); err != nil {
		t.Fatal(err)
	}
	ids, err := handler.dueSubscriptionIDs(t.Context(), time.Now())
	if err != nil || !slices.Equal(ids, []int{id}) {
		t.Fatalf("启用且到期的组 %v %v", ids, err)
	}
	started := make(chan struct{})
	handler.fetchSubscription = func(ctx context.Context, _ []string, _ services.BatchImportSourceType) ([]services.ImportItem, []services.ImportFailure, error) {
		close(started)
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	done := handler.StartSubscriptionScheduler(ctx)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("到期组未被抓取")
	}
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("调度关闭没有取消在途请求")
	}
	group, err := models.LoadGroup(t.Context(), handler.db, id)
	if err != nil || group.SubscriptionLastError != "" || group.SubscriptionLastUpdatedAt != nil || group.Revision != 0 {
		t.Fatal("取消不应记为一次订阅尝试")
	}
}

func TestSubscriptionRemoteDatabaseIntegration(t *testing.T) {
	db := openBatchImportIntegrationDatabase(t)
	if db == nil {
		t.Skip("需要配置订阅集成测试数据库")
	}
	t.Cleanup(func() { _ = db.Close() })
	useRealSingBoxForChecks(t)
	resetBatchImportIntegrationTables(t, db)
	t.Cleanup(func() { resetBatchImportIntegrationTables(t, db) })
	if err := models.InitDB(db); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "10ms")
	service := services.NewSingBoxService(t.TempDir())
	t.Cleanup(func() { _ = service.Stop() })
	handler := NewHandler(db, service)
	groupID := configureTestSubscription(t, handler, false)
	first := subscriptionItem("保留", "original")
	feed := []services.ImportItem{first, subscriptionItem("移除", "other")}
	handler.fetchSubscription = func(context.Context, []string, services.BatchImportSourceType) ([]services.ImportItem, []services.ImportFailure, error) {
		return feed, nil, nil
	}
	if result, err := handler.refreshGroupSubscription(t.Context(), groupID, false); err != nil || result.Added != 2 {
		t.Fatalf("建立远程快照 %+v %v", result, err)
	}
	before := subscriptionTestNodes(t, handler, groupID)[first.Name]
	feed = []services.ImportItem{subscriptionItem(first.Name, "rotated")}
	if result, err := handler.refreshGroupSubscription(t.Context(), groupID, false); err != nil || result.Updated != 1 || result.Removed != 1 {
		t.Fatalf("更新远程快照 %+v %v", result, err)
	}
	after := subscriptionTestNodes(t, handler, groupID)[first.Name]
	if before.ID == 0 || before.ID != after.ID || after.InboundPort != 0 || after.SubscriptionKey == before.SubscriptionKey || after.Username != before.Username || after.Password != before.Password {
		t.Fatal("远程数据库没有原样保留节点身份、入站和凭据")
	}
	ids, err := handler.dueSubscriptionIDs(t.Context(), time.Now())
	if err != nil || len(ids) != 0 {
		t.Fatalf("远程数据库未读取到刚完成的刷新时间 %v", err)
	}
}
