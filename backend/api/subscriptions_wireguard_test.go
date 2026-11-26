package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"

	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
)

func legacyWireGuardSubscriptionConfig(privateKey, detour string) string {
	dialer := ""
	if detour != "" {
		dialer = fmt.Sprintf(`,"detour":%q,"domain_resolver":"local","routing_mark":123,"udp_fragment":false,"connect_timeout":"5s","bind_interface":"eth0","domain_resolver_options":{"strategy":"prefer_ipv4"},"network_type":["wifi"],"fallback_delay":"2s"`, detour)
	}
	return fmt.Sprintf(`{"server":"192.0.2.10","server_port":2408,"local_address":["10.0.0.2/32"],"private_key":%q,"peer_public_key":"peer-key","reserved":[1,2,3],"mtu":1280%s}`, privateKey, dialer)
}

func subscriptionTestKey(protocol, config string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(protocol+"\x00"+config)))
}

func TestSubscriptionNonWireGuardLegacyKeysRemainUnchanged(testingT *testing.T) {
	for _, test := range []struct{ protocol, config string }{
		{"ss", `{"server":"192.0.2.10","server_port":443,"method":"aes-128-gcm","password":"value"}`},
		{"vless", `{"server":"192.0.2.10","server_port":443,"uuid":"node-id","security":"tls"}`},
		{"vmess", `{"server":"192.0.2.10","server_port":443,"uuid":"node-id","alter_id":0,"security":"auto","tls":"tls"}`},
		{"trojan", `{"server":"192.0.2.10","server_port":443,"password":"value","security":"tls"}`},
	} {
		testingT.Run(test.protocol, func(testingT *testing.T) {
			previous := models.ProxyNode{
				ID: 100, GroupID: 1, Name: "原名", Type: test.protocol, Config: test.config,
				SubscriptionName: "原名", SubscriptionKey: subscriptionTestKey(test.protocol, test.config),
				UpstreamMode: models.UpstreamModeNone,
			}
			incoming, _, err := subscriptionNodes([]services.ImportItem{{Name: "原名", Type: test.protocol, Config: json.RawMessage(test.config)}})
			if err != nil {
				testingT.Fatal(err)
			}
			plan, err := planSubscription([]models.ProxyNode{previous}, incoming, 1)
			if err != nil || plan.unchanged != 1 || len(plan.add)+len(plan.update)+len(plan.remove) != 0 {
				testingT.Fatalf("非 WireGuard 旧订阅首次刷新应保持 Unchanged：%+v，%v", plan, err)
			}
		})
	}
}

func TestSubscriptionWireGuardLegacyRefreshPreservesNodes(testingT *testing.T) {
	type entry struct{ name, privateKey, detour string }
	for _, test := range []struct {
		name       string
		previous   []entry
		incoming   []entry
		wantNames  []string
		updated    int
		duplicates int
		localEdit  bool
		customName bool
		upstream   string
		ambiguous  bool
	}{
		{
			name:     "无拨号字段旧节点首次刷新无需更新",
			previous: []entry{{"原名", "first-key", ""}}, incoming: []entry{{"原名", "first-key", ""}},
			wantNames: []string{"原名"},
		},
		{
			name:     "旧拨号字段顺序首次刷新平滑迁移",
			previous: []entry{{"原名", "first-key", "direct"}}, incoming: []entry{{"原名", "first-key", "direct"}},
			wantNames: []string{"原名"}, updated: 1,
		},
		{
			name:     "旧拨号节点改名保留自定义名称和上游模式",
			previous: []entry{{"原名", "first-key", "direct"}}, incoming: []entry{{"订阅新名", "first-key", "direct"}},
			wantNames: []string{"订阅新名"}, updated: 1, customName: true, upstream: models.UpstreamModeNone,
		},
		{
			name:      "相同出站别名改名并忽略重复条目",
			previous:  []entry{{"甲", "shared-key", "direct"}, {"乙", "shared-key", "direct"}},
			incoming:  []entry{{"乙", "shared-key", "direct"}, {"改名甲", "shared-key", "direct"}, {"乙", "shared-key", "direct"}},
			wantNames: []string{"改名甲", "乙"}, updated: 2, duplicates: 1,
		},
		{
			name:      "同服同名不同配置保持独立身份",
			previous:  []entry{{"同名", "first-key", "direct"}, {"同名", "second-key", "direct"}},
			incoming:  []entry{{"同名", "second-key", "direct"}, {"同名", "first-key", "direct"}, {"同名", "second-key", "direct"}},
			wantNames: []string{"同名", "同名"}, updated: 2, duplicates: 1,
		},
		{
			name:      "同服同名节点改名不替换原节点",
			previous:  []entry{{"同名", "first-key", "direct"}, {"同名", "second-key", "direct"}},
			incoming:  []entry{{"同名", "second-key", "direct"}, {"改名", "first-key", "direct"}},
			wantNames: []string{"改名", "同名"}, updated: 2,
		},
		{
			name:      "仅 detour 不同也保持独立身份",
			previous:  []entry{{"同名", "shared-key", "direct"}, {"同名", "shared-key", "relay"}},
			incoming:  []entry{{"同名", "shared-key", "relay"}, {"同名", "shared-key", "direct"}},
			wantNames: []string{"同名", "同名"}, updated: 2,
		},
		{
			name:     "本地编辑后旧拨号节点通过标签匹配",
			previous: []entry{{"原名", "first-key", "direct"}}, incoming: []entry{{"原名", "first-key", "direct"}},
			wantNames: []string{"原名"}, updated: 1, localEdit: true,
		},
		{
			name:      "本地编辑不冒充另一同名节点订阅身份",
			previous:  []entry{{"同名", "first-key", "direct"}, {"同名", "locally-edited-key", "direct"}},
			incoming:  []entry{{"同名", "locally-edited-key", "direct"}, {"同名", "first-key", "direct"}},
			localEdit: true, ambiguous: true,
		},
		{
			name:      "本地编辑仍按未变的原订阅键识别改名",
			previous:  []entry{{"原名", "first-key", ""}, {"其他", "locally-edited-key", ""}},
			incoming:  []entry{{"订阅新名", "first-key", ""}, {"其他", "locally-edited-key", ""}},
			wantNames: []string{"订阅新名", "其他"}, updated: 1, localEdit: true,
		},
		{
			name:     "无法区分的历史重复记录拒绝覆盖",
			previous: []entry{{"同名", "shared-key", "direct"}, {"同名", "shared-key", "direct"}},
			incoming: []entry{{"同名", "shared-key", "direct"}}, ambiguous: true,
		},
	} {
		testingT.Run(test.name, func(testingT *testing.T) {
			handler := newTestHandler(testingT, nil)
			groupID := configureTestSubscription(testingT, handler, true)
			for index, item := range test.previous {
				legacy := legacyWireGuardSubscriptionConfig(item.privateKey, item.detour)
				node := models.ProxyNode{
					GroupID: groupID, Name: item.name, Remark: "保留备注", Type: "wireguard", Config: legacy,
					SubscriptionName: item.name, SubscriptionKey: subscriptionTestKey("wireguard", legacy),
					InboundPort: 32001 + index, InboundMode: models.InboundModeMixed, InboundPortPinned: true,
					Username: fmt.Sprintf("user-%d", index), Password: fmt.Sprintf("credential-%d", index),
					SortOrder: index, UpstreamMode: models.UpstreamModeLegacy,
				}
				if test.upstream != "" {
					node.UpstreamMode = test.upstream
				}
				if (test.localEdit || test.customName) && index == 0 {
					node.Name = "本地别名"
				}
				if test.localEdit && index == 0 {
					node.Config = legacyWireGuardSubscriptionConfig("locally-edited-key", item.detour)
				}
				if _, err := handler.db.Exec(insertNodeSQL, nodeInsertArgs(node)...); err != nil {
					testingT.Fatal(err)
				}
			}
			before, err := models.LoadGroupNodes(testingT.Context(), handler.db, groupID)
			if err != nil {
				testingT.Fatal(err)
			}
			feed := make([]services.ImportItem, 0, len(test.incoming))
			canonicalConfigs := make(map[[2]string]string)
			for _, item := range test.incoming {
				config, err := (models.ProxyDefinition{Type: "wireguard", Config: legacyWireGuardSubscriptionConfig(item.privateKey, item.detour)}).ParseConfig()
				if err != nil {
					testingT.Fatal(err)
				}
				canonical, err := json.Marshal(config)
				if err != nil {
					testingT.Fatal(err)
				}
				canonicalConfigs[[2]string{item.privateKey, item.detour}] = string(canonical)
				feed = append(feed, services.ImportItem{Name: item.name, Type: "wireguard", Config: config})
			}
			handler.fetchSubscription = func(context.Context, []string, services.BatchImportSourceType) ([]services.ImportItem, []services.ImportFailure, error) {
				return feed, nil, nil
			}
			result, refreshErr := handler.refreshGroupSubscription(testingT.Context(), groupID, false)
			after, err := models.LoadGroupNodes(testingT.Context(), handler.db, groupID)
			if err != nil {
				testingT.Fatal(err)
			}
			if test.ambiguous {
				if !errors.Is(refreshErr, errSubscriptionAmbiguous) || !reflect.DeepEqual(before, after) {
					testingT.Fatalf("模糊身份应保留原节点快照：%+v，%v", result, refreshErr)
				}
				return
			}
			if refreshErr != nil || result.Added != 0 || result.Removed != 0 || result.Updated != test.updated || result.Unchanged != len(test.previous)-test.updated || len(result.Warnings) != min(test.duplicates, 1) {
				testingT.Fatalf("旧 WireGuard 订阅刷新结果错误：%+v，%v", result, refreshErr)
			}
			if len(after) != len(before) {
				testingT.Fatal("刷新增加或删除了原节点")
			}
			for index, node := range after {
				want := before[index]
				want.SubscriptionName = test.wantNames[index]
				if want.Name == before[index].SubscriptionName {
					want.Name = want.SubscriptionName
				}
				want.Config = canonicalConfigs[[2]string{test.previous[index].privateKey, test.previous[index].detour}]
				want.SubscriptionKey = subscriptionTestKey("wireguard", want.Config)
				want.UpdatedAt = node.UpdatedAt
				if test.localEdit && index == 0 {
					want.CheckRevision++
				}
				if !reflect.DeepEqual(node, want) {
					actualFields, expectedFields := reflect.ValueOf(node), reflect.ValueOf(want)
					for fieldIndex := 0; fieldIndex < actualFields.NumField(); fieldIndex++ {
						actual, expected := actualFields.Field(fieldIndex).Interface(), expectedFields.Field(fieldIndex).Interface()
						if !reflect.DeepEqual(actual, expected) {
							testingT.Logf("%s: node=%#v want=%#v", actualFields.Type().Field(fieldIndex).Name, actual, expected)
						}
					}
					testingT.Fatalf("刷新没有保留 ID、认证、固定端口及本地字段：节点 %d", before[index].ID)
				}
			}
			result, refreshErr = handler.refreshGroupSubscription(testingT.Context(), groupID, false)
			if refreshErr != nil || result.Added != 0 || result.Removed != 0 || result.Updated != 0 || result.Unchanged != len(after) {
				testingT.Fatalf("迁移完成后的再次刷新应全部保持 Unchanged：%+v，%v", result, refreshErr)
			}
		})
	}
}

func TestSubscriptionVMessJSONLegacyRefreshPreservesIdentityAndLocalFields(testingT *testing.T) {
	const legacyPrefix = `{"server":"192.0.2.10","server_port":443,"uuid":"00000000-0000-0000-0000-000000000303","alter_id":0,"security":"auto","network":"tcp"`
	for _, test := range []struct {
		name   string
		fields map[string]any
		suffix string
	}{
		{name: "TLS 缺省"},
		{name: "TLS 空字符串", fields: map[string]any{"tls": ""}},
		{name: "TLS none", fields: map[string]any{"tls": "none"}},
		{name: "TLS insecure false", fields: map[string]any{"tls": "tls", "allowInsecure": false}, suffix: `,"tls":"tls","tls_options":{"enabled":true}`},
		{name: "TLS insecure 字符串零", fields: map[string]any{"tls": "tls", "allowInsecure": "0"}, suffix: `,"tls":"tls","tls_options":{"enabled":true}`},
		{name: "缺省 TLS 推断 Reality", fields: map[string]any{"pbk": "jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0"}, suffix: `,"tls_options":{"enabled":false,"reality":{"enabled":true,"public_key":"jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0","short_id":""},"utls":{"enabled":true,"fingerprint":"chrome"}}`},
	} {
		for _, customName := range []bool{false, true} {
			testingT.Run(fmt.Sprintf("%s/自定义名称=%t", test.name, customName), func(testingT *testing.T) {
				handler := newTestHandler(testingT, nil)
				groupID := configureTestSubscription(testingT, handler, true)
				fields := map[string]any{"add": "192.0.2.10", "port": 443, "id": "00000000-0000-0000-0000-000000000303", "net": "tcp", "scy": "auto", "ps": "订阅原名"}
				for key, value := range test.fields {
					fields[key] = value
				}
				link := func() string {
					encoded, err := json.Marshal(fields)
					if err != nil {
						testingT.Fatal(err)
					}
					return "vmess://" + base64.StdEncoding.EncodeToString(encoded)
				}
				handler.fetchSubscription = func(ctx context.Context, urls []string, kind services.BatchImportSourceType) ([]services.ImportItem, []services.ImportFailure, error) {
					if len(urls) != 1 || urls[0] != testSubscriptionURL || kind != services.BatchImportSourceSubscription {
						testingT.Fatal("刷新没有使用已保存的订阅地址和类型")
					}
					return services.ExpandBatchImportSourcesWithType(ctx, []string{link()}, services.BatchImportSourceAuto)
				}
				refresh := func() subscriptionResult {
					testingT.Helper()
					id := strconv.Itoa(groupID)
					response := postJSON(testingT, handler.RefreshSubscription, http.MethodPost, "/api/node-groups/"+id+"/subscription/refresh", nil, gin.Params{{Key: "id", Value: id}})
					if response.Code != http.StatusOK {
						testingT.Fatalf("订阅刷新失败：status=%d body=%s", response.Code, response.Body.String())
					}
					var result subscriptionResult
					if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
						testingT.Fatal(err)
					}
					return result
				}
				if result := refresh(); result.Added != 1 || result.Removed+result.Updated != 0 {
					testingT.Fatalf("真实 VMess JSON 链接首次导入失败：%+v", result)
				}
				nodes, err := models.LoadGroupNodes(testingT.Context(), handler.db, groupID)
				if err != nil || len(nodes) != 1 {
					testingT.Fatalf("初始节点快照错误：count=%d err=%v", len(nodes), err)
				}
				legacyConfig := legacyPrefix + test.suffix + "}"
				legacyKey := subscriptionTestKey("vmess", legacyConfig)
				name := "订阅原名"
				if customName {
					name = "本地自定义名称"
				}
				if _, err := handler.db.Exec(`UPDATE proxy_nodes SET config=?,subscription_key=?,name=?,remark=?,username=?,password=?,inbound_port=32001,inbound_mode='mixed',inbound_port_pinned=TRUE,sort_order=17,tcp_reuse_enabled=FALSE WHERE id=?`, legacyConfig, legacyKey, name, "保留备注", "fixed-user", "fixed-credential", nodes[0].ID); err != nil {
					testingT.Fatal(err)
				}
				before, err := models.LoadProxyNode(testingT.Context(), handler.db, nodes[0].ID)
				if err != nil || before.Config != legacyConfig || before.SubscriptionKey != legacyKey {
					testingT.Fatalf("未能建立清理前 JSON/Key 快照：%v", err)
				}
				fields["ps"] = "供应方升级后改名"
				if result := refresh(); result.Updated != 1 || result.Added+result.Removed+result.Unchanged != 0 {
					testingT.Fatalf("首次升级刷新只能原地改名，不能删旧增新：%+v", result)
				}
				nodes, err = models.LoadGroupNodes(testingT.Context(), handler.db, groupID)
				if err != nil || len(nodes) != 1 {
					testingT.Fatalf("升级刷新改变了节点数量：count=%d err=%v", len(nodes), err)
				}
				want := before
				want.SubscriptionName = fields["ps"].(string)
				if !customName {
					want.Name = want.SubscriptionName
				}
				want.UpdatedAt = nodes[0].UpdatedAt
				if !reflect.DeepEqual(nodes[0], want) {
					testingT.Fatalf("升级刷新没有完整保留 ID、JSON/Key、认证、固定端口、备注和本地名称：实际=%+v 预期=%+v", nodes[0], want)
				}
				if result := refresh(); result.Unchanged != 1 || result.Added+result.Updated+result.Removed != 0 {
					testingT.Fatalf("相同 VMess 订阅再次刷新必须幂等：%+v", result)
				}
			})
		}
	}
}
