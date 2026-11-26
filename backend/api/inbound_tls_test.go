package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
)

func writeAPITLSCertificate(t *testing.T, directory, name string) (string, string) {
	t.Helper()
	return writeAPITLSCertificateWithExpiry(t, directory, name, time.Now().Add(24*time.Hour))
}

func writeAPITLSCertificateWithExpiry(t *testing.T, directory, name string, notAfter time.Time) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name + ".example"}, DNSNames: []string{name + ".example"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(directory, name+".pem"), filepath.Join(directory, name+".key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func setAPITLSDirectories(t *testing.T, directory string) {
	t.Helper()
	t.Setenv("SBPM_INBOUND_TLS_CERT_DIR", directory)
	t.Setenv("SBPM_INBOUND_TLS_KEY_DIR", filepath.Join(directory, "missing"))
	t.Setenv("INBOUND_TLS_CERT_DIR", "")
	t.Setenv("INBOUND_TLS_KEY_DIR", "")
}

func inboundTLSTestClient(t *testing.T, handler *Handler) groupTestClient {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/nodes/:id", handler.GetNode)
	router.POST("/api/nodes", handler.CreateNode)
	router.PUT("/api/nodes/:id", handler.UpdateNode)
	router.PATCH("/api/nodes/:id", handler.UpdateNodeFlags)
	router.DELETE("/api/nodes/:id", handler.DeleteNode)
	router.POST("/api/nodes/batch-move", handler.BatchMoveNodes)
	router.POST("/api/node-groups", handler.CreateGroup)
	router.PATCH("/api/node-groups/:id", handler.UpdateGroup)
	router.DELETE("/api/node-groups/:id", handler.DeleteGroup)
	router.GET("/api/settings", handler.GetSettings)
	router.PUT("/api/settings", handler.UpdateSettings)
	return groupTestClient{t: t, db: handler.db, router: router, handler: handler}
}

func assertAPIRuntimeOmitsInbounds(t *testing.T, handler *Handler, nodes ...models.ProxyNode) {
	t.Helper()
	config, err := os.ReadFile(handler.singBoxService.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		if strings.Contains(string(config), fmt.Sprintf(`"node-%d-in"`, node.ID)) {
			t.Fatalf("失效 HTTPS 节点 %d 仍有入站监听或直连路由", node.ID)
		}
	}
}

func TestHTTPSNodesRecoverAfterCertificateLoss(t *testing.T) {
	for _, loss := range []string{"deleted", "expired"} {
		for _, recovery := range []string{"disable", "mixed", "delete"} {
			t.Run(loss+"/"+recovery, func(t *testing.T) {
				root := t.TempDir()
				setAPITLSDirectories(t, root)
				certPath, keyPath := writeAPITLSCertificate(t, root, "proxy")
				handler := newTestHandler(t, nil)
				client := inboundTLSTestClient(t, handler)
				firstGroup := client.createGroup("first", 31001, true)
				secondGroup := client.createGroup("second", 32001, true)
				var secure []models.ProxyNode
				for _, group := range []models.NodeGroup{firstGroup, secondGroup} {
					var node models.ProxyNode
					client.request(http.MethodPost, "/api/nodes", gin.H{"name": group.Name, "type": "direct", "config": "{}", "enabled": true, "group_id": group.ID, "inbound_mode": "https"}, 201, &node)
					secure = append(secure, node)
				}
				mixed := client.createNode("unrelated", 0, true)
				if loss == "deleted" {
					for _, path := range []string{certPath, keyPath} {
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
					}
				} else {
					writeAPITLSCertificateWithExpiry(t, root, "proxy", time.Now().Add(-time.Minute))
				}
				client.request(http.MethodPut, testNodePath(mixed.ID), gin.H{"name": "edited", "type": "direct", "config": "{}", "enabled": true}, 200, &mixed)
				assertAPIRuntimeOmitsInbounds(t, handler, secure...)
				client.request(http.MethodPut, "/api/settings", gin.H{"inbound_tls_cert_path": "", "inbound_tls_key_path": ""}, 400, nil)
				for _, enabled := range []bool{true, false} {
					payload := gin.H{"name": "blocked", "type": "direct", "config": "{}", "enabled": enabled, "inbound_mode": "https"}
					client.request(http.MethodPost, "/api/nodes", payload, 400, nil)
					client.request(http.MethodPut, testNodePath(mixed.ID), payload, 400, nil)
					client.request(http.MethodPatch, testNodePath(mixed.ID), gin.H{"inbound_mode": "https", "enabled": enabled}, 400, nil)
				}
				for index, node := range secure {
					switch recovery {
					case "disable":
						client.request(http.MethodPatch, testNodePath(node.ID), gin.H{"enabled": false}, 200, &node)
						if node.Enabled || node.InboundMode != models.InboundModeHTTPS {
							t.Fatal("未保留禁用节点的 HTTPS 模式")
						}
						client.request(http.MethodPatch, testNodePath(node.ID), gin.H{"enabled": true}, 400, nil)
					case "mixed":
						client.request(http.MethodPatch, testNodePath(node.ID), gin.H{"inbound_mode": "mixed"}, 200, &node)
						if !node.Enabled || node.InboundMode != models.InboundModeMixed {
							t.Fatal("未完成显式切回 mixed")
						}
						client.request(http.MethodPatch, testNodePath(node.ID), gin.H{"inbound_mode": "https"}, 400, nil)
					case "delete":
						client.request(http.MethodDelete, testNodePath(node.ID), nil, 200, nil)
					}
					assertAPIRuntimeOmitsInbounds(t, handler, secure[index+1:]...)
				}
			})
		}
	}
}

func TestHTTPSNodesWithoutPortsDoNotRequireCertificates(t *testing.T) {
	setAPITLSDirectories(t, t.TempDir())
	handler := newTestHandler(t, nil)
	client := inboundTLSTestClient(t, handler)
	group := client.createGroup("no-port", 31001, false)
	for _, enabled := range []bool{true, false} {
		var node models.ProxyNode
		client.request(http.MethodPost, "/api/nodes", gin.H{"name": "secure", "type": "direct", "config": "{}", "enabled": enabled, "inbound_mode": "https", "inbound_port": 31002, "group_id": group.ID}, 201, &node)
		if node.InboundPort != 0 || node.InboundMode != models.InboundModeHTTPS {
			t.Fatalf("无端口组未归一化 HTTPS 入站：%+v", node)
		}
		client.request(http.MethodPut, testNodePath(node.ID), gin.H{"name": "edited", "type": "direct", "config": "{}", "enabled": !enabled, "inbound_port": 31003}, 200, &node)
		for _, state := range []bool{false, true} {
			client.request(http.MethodPatch, testNodePath(node.ID), gin.H{"enabled": state}, 200, &node)
		}
		client.request(http.MethodPatch, testNodePath(node.ID), gin.H{"inbound_mode": "mixed"}, 200, &node)
		client.request(http.MethodPatch, testNodePath(node.ID), gin.H{"inbound_mode": "https"}, 200, &node)
		assertAPIRuntimeOmitsInbounds(t, handler, node)
	}
	settings, err := models.LoadRuntimeSettings(t.Context(), handler.db)
	if err != nil || settings.InboundTLSCertPath != "" || settings.InboundTLSKeyPath != "" {
		t.Fatalf("无公网监听器时意外保存了 TLS 选择：%+v，%v", settings, err)
	}
}

func TestHTTPSNodesCanLosePortsWithoutCertificate(t *testing.T) {
	for _, transition := range []string{"update", "batch-move", "group-update"} {
		t.Run(transition, func(t *testing.T) {
			root := t.TempDir()
			setAPITLSDirectories(t, root)
			certPath, keyPath := writeAPITLSCertificate(t, root, "proxy")
			handler := newTestHandler(t, nil)
			client := inboundTLSTestClient(t, handler)
			group := client.createGroup("with-port", 31001, true)
			noPort := client.createGroup("no-port", 32001, false)
			var node models.ProxyNode
			client.request(http.MethodPost, "/api/nodes", gin.H{"name": "secure", "type": "direct", "config": "{}", "enabled": true, "inbound_mode": "https", "group_id": group.ID}, 201, &node)
			for _, path := range []string{certPath, keyPath} {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			switch transition {
			case "update":
				client.request(http.MethodPut, testNodePath(node.ID), gin.H{"name": "moved", "type": "direct", "config": "{}", "enabled": true, "group_id": noPort.ID}, 200, &node)
			case "batch-move":
				client.request(http.MethodPost, "/api/nodes/batch-move", gin.H{"ids": []int{node.ID}, "group_id": noPort.ID}, 200, nil)
			case "group-update":
				client.request(http.MethodPatch, testGroupPath(group.ID), gin.H{"allocate_port": false}, 200, nil)
			}
			for _, enabled := range []bool{false, true} {
				client.request(http.MethodPut, testNodePath(node.ID), gin.H{"name": "edited", "type": "direct", "config": "{}", "enabled": enabled}, 200, &node)
				if node.InboundPort != 0 || node.InboundMode != models.InboundModeHTTPS || node.Enabled != enabled {
					t.Fatalf("移除公网端口后不能正常编辑 HTTPS 节点：%+v", node)
				}
				client.request(http.MethodPatch, testNodePath(node.ID), gin.H{"enabled": !enabled}, 200, nil)
			}
			assertAPIRuntimeOmitsInbounds(t, handler, node)
		})
	}
}

func TestDisabledHTTPSNodeCanBeEditedWithoutCertificate(t *testing.T) {
	root := t.TempDir()
	setAPITLSDirectories(t, root)
	certPath, keyPath := writeAPITLSCertificate(t, root, "proxy")
	handler := newTestHandler(t, nil)
	client := inboundTLSTestClient(t, handler)
	var node models.ProxyNode
	client.request(http.MethodPost, "/api/nodes", gin.H{"name": "secure", "type": "direct", "config": "{}", "enabled": true, "inbound_mode": "https"}, 201, &node)
	for _, path := range []string{certPath, keyPath} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	payload := gin.H{"name": "disabled-edited", "type": "direct", "config": "{}", "enabled": false}
	client.request(http.MethodPut, testNodePath(node.ID), payload, 200, &node)
	if node.Enabled || node.InboundMode != models.InboundModeHTTPS || node.Name != "disabled-edited" {
		t.Fatalf("失证后完整编辑并禁用失败：%+v", node)
	}
	client.request(http.MethodPut, testNodePath(node.ID), payload, 200, &node)
	client.request(http.MethodPatch, testNodePath(node.ID), gin.H{"inbound_mode": "https", "tcp_reuse_enabled": true}, 200, &node)
	assertAPIRuntimeOmitsInbounds(t, handler, node)
	payload["enabled"] = true
	client.request(http.MethodPut, testNodePath(node.ID), payload, 400, nil)
	client.request(http.MethodPatch, testNodePath(node.ID), gin.H{"enabled": true}, 400, nil)
	payload["enabled"], payload["inbound_mode"] = false, "mixed"
	client.request(http.MethodPut, testNodePath(node.ID), payload, 200, &node)
	payload["inbound_mode"] = "https"
	client.request(http.MethodPut, testNodePath(node.ID), payload, 400, nil)
	client.request(http.MethodPatch, testNodePath(node.ID), gin.H{"inbound_mode": "https"}, 400, nil)
}

func TestGroupPortAllocationValidatesActiveHTTPSListeners(t *testing.T) {
	for _, mutation := range []string{"group-update", "batch-move", "group-delete"} {
		for _, state := range []string{"none", "unselected", "disabled"} {
			t.Run(mutation+"/"+state, func(t *testing.T) {
				root := t.TempDir()
				setAPITLSDirectories(t, root)
				if state == "unselected" {
					writeAPITLSCertificate(t, root, "first")
					writeAPITLSCertificate(t, root, "second")
				}
				handler := newTestHandler(t, nil)
				client := inboundTLSTestClient(t, handler)
				source := client.createGroup("no-port", 31001, false)
				target := client.createGroup("with-port", 32001, true)
				enabled := state != "disabled"
				var node models.ProxyNode
				client.request(http.MethodPost, "/api/nodes", gin.H{"name": "secure", "type": "direct", "config": "{}", "enabled": enabled, "inbound_mode": "https", "group_id": source.ID}, 201, &node)
				previousPID := handler.singBoxService.RuntimeStatus().PID
				wantStatus := http.StatusBadRequest
				if !enabled {
					wantStatus = http.StatusOK
				}
				var response gin.H
				switch mutation {
				case "group-update":
					client.request(http.MethodPatch, testGroupPath(source.ID), gin.H{"allocate_port": true, "preserve_inbound_ports": true}, wantStatus, &response)
				case "batch-move":
					client.request(http.MethodPost, "/api/nodes/batch-move", gin.H{"ids": []int{node.ID}, "group_id": target.ID}, wantStatus, &response)
				case "group-delete":
					client.request(http.MethodDelete, testGroupPath(source.ID)+fmt.Sprintf("?mode=move&target_group_id=%d", target.ID), nil, wantStatus, &response)
				}
				client.request(http.MethodGet, testNodePath(node.ID), nil, 200, &node)
				if enabled {
					if node.GroupID != source.ID || node.InboundPort != 0 || !node.Enabled || node.InboundMode != models.InboundModeHTTPS {
						t.Fatalf("证书校验失败后节点分组和端口未回滚：%+v", node)
					}
					group, err := models.LoadGroup(t.Context(), handler.db, source.ID)
					if err != nil || group.AllocatePort || group.PreserveInboundPorts || group.Revision != source.Revision {
						t.Fatalf("证书校验失败后原分组未保留：%+v，%v", group, err)
					}
					if handler.singBoxService.RuntimeStatus().PID != previousPID || !strings.Contains(response["error"].(string), "TLS 证书") {
						t.Fatal("证书校验失败未保留旧进程或明确提示 TLS 证书错误")
					}
				} else {
					wantGroup := target.ID
					if mutation == "group-update" {
						wantGroup = source.ID
					}
					if node.GroupID != wantGroup || node.InboundPort <= 0 || node.Enabled || node.InboundMode != models.InboundModeHTTPS {
						t.Fatalf("禁用 HTTPS 节点的端口分配被错误阻止：%+v", node)
					}
					if mutation == "group-update" {
						group, err := models.LoadGroup(t.Context(), handler.db, source.ID)
						if err != nil || !group.PreserveInboundPorts {
							t.Fatal("重新分配禁用 HTTPS 节点端口时未保存保留策略", err)
						}
					}
				}
				settings, err := models.LoadRuntimeSettings(t.Context(), handler.db)
				if err != nil || settings.InboundTLSCertPath != "" || settings.InboundTLSKeyPath != "" {
					t.Fatalf("分组操作意外保存了 TLS 选择：%+v，%v", settings, err)
				}
				assertAPIRuntimeOmitsInbounds(t, handler, node)
			})
		}
	}
}

func TestHTTPSAutoSelectionPersistsLivePathsAcrossRenewal(t *testing.T) {
	for _, mutation := range []string{"create", "update", "flags", "group-update", "batch-move", "group-delete"} {
		t.Run(mutation, func(t *testing.T) {
			root := t.TempDir()
			setAPITLSDirectories(t, root)
			archive, live := filepath.Join(root, "archive", "renew.example"), filepath.Join(root, "live", "renew.example")
			for _, directory := range []string{archive, live} {
				if err := os.MkdirAll(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			renew := func(generation int) {
				certPath, keyPath := writeAPITLSCertificate(t, t.TempDir(), "renew")
				for _, file := range [][3]string{{certPath, fmt.Sprintf("fullchain%d.pem", generation), "fullchain.pem"}, {keyPath, fmt.Sprintf("privkey%d.pem", generation), "privkey.pem"}} {
					if err := os.Rename(file[0], filepath.Join(archive, file[1])); err != nil {
						t.Fatal(err)
					}
					link := filepath.Join(live, file[2])
					if generation > 1 {
						if err := os.Remove(link); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.Symlink(filepath.Join("..", "..", "archive", "renew.example", file[1]), link); err != nil {
						t.Fatal(err)
					}
				}
			}
			renew(1)
			handler := newTestHandler(t, nil)
			client := inboundTLSTestClient(t, handler)
			var node models.ProxyNode
			payload := gin.H{"name": "secure", "type": "direct", "config": "{}", "enabled": true, "inbound_mode": "https"}
			switch mutation {
			case "create":
				client.request(http.MethodPost, "/api/nodes", payload, 201, &node)
			case "update":
				node = client.createNode("mixed", 0, true)
				client.request(http.MethodPut, testNodePath(node.ID), payload, 200, &node)
			case "flags":
				node = client.createNode("mixed", 0, true)
				client.request(http.MethodPatch, testNodePath(node.ID), gin.H{"inbound_mode": "https"}, 200, &node)
			case "group-update", "batch-move", "group-delete":
				source := client.createGroup("no-port", 31001, false)
				target := client.createGroup("with-port", 32001, true)
				payload["group_id"] = source.ID
				client.request(http.MethodPost, "/api/nodes", payload, 201, &node)
				settings, err := models.LoadRuntimeSettings(t.Context(), handler.db)
				if err != nil || settings.InboundTLSCertPath != "" || settings.InboundTLSKeyPath != "" {
					t.Fatalf("无公网端口的 HTTPS 节点提前保存了 TLS 选择：%+v，%v", settings, err)
				}
				switch mutation {
				case "group-update":
					client.request(http.MethodPatch, testGroupPath(source.ID), gin.H{"allocate_port": true, "preserve_inbound_ports": true}, 200, nil)
					group, err := models.LoadGroup(t.Context(), handler.db, source.ID)
					if err != nil || !group.PreserveInboundPorts {
						t.Fatal("HTTPS 首次自动选证时未保存分组保留策略", err)
					}
				case "batch-move":
					client.request(http.MethodPost, "/api/nodes/batch-move", gin.H{"ids": []int{node.ID}, "group_id": target.ID}, 200, nil)
				case "group-delete":
					client.request(http.MethodDelete, testGroupPath(source.ID)+fmt.Sprintf("?mode=move&target_group_id=%d", target.ID), nil, 200, nil)
				}
				delete(payload, "group_id")
			}
			client.request(http.MethodGet, testNodePath(node.ID), nil, 200, &node)
			if !node.Enabled || node.InboundPort <= 0 || node.InboundMode != models.InboundModeHTTPS {
				t.Fatalf("分组操作未激活 HTTPS 监听器：%+v", node)
			}
			wantCert, wantKey := filepath.Join(live, "fullchain.pem"), filepath.Join(live, "privkey.pem")
			settings, err := models.LoadRuntimeSettings(t.Context(), handler.db)
			if err != nil || settings.InboundTLSCertPath != wantCert || settings.InboundTLSKeyPath != wantKey {
				t.Fatalf("首次自动选证未持久化 live 逻辑路径：%+v，%v", settings, err)
			}
			renew(2)
			status := services.ResolveInboundTLS(settings.InboundTLSCertPath, settings.InboundTLSKeyPath)
			if !status.Ready || status.SelectionMode != "selected" || len(status.Certificates) != 2 || status.Active.CertPath != wantCert || status.Active.KeyPath != wantKey {
				t.Fatalf("提前续签后未继续选中 live 路径：%+v", status)
			}
			var response struct {
				CertPath string                    `json:"inbound_tls_cert_path"`
				KeyPath  string                    `json:"inbound_tls_key_path"`
				TLS      services.InboundTLSStatus `json:"inbound_tls"`
			}
			client.request(http.MethodGet, "/api/settings", nil, 200, &response)
			if response.CertPath != wantCert || response.KeyPath != wantKey || !response.TLS.Ready || response.TLS.SelectionMode != "selected" || response.TLS.Active.CertPath != wantCert {
				t.Fatalf("GET settings 未反映续签后的已选证书：%+v", response)
			}
			delete(payload, "inbound_mode")
			client.request(http.MethodPut, testNodePath(node.ID), payload, 200, &node)
			config, err := os.ReadFile(handler.singBoxService.ConfigPath())
			if err != nil || !strings.Contains(string(config), wantCert) || !strings.Contains(string(config), wantKey) || !strings.Contains(string(config), `"type": "http"`) {
				t.Fatalf("续签后的运行时未继续使用 HTTPS 和 live 路径：%s，%v", config, err)
			}
		})
	}
}

func TestHTTPSAutoSelectionRollsBackWithRejectedNode(t *testing.T) {
	for _, mutation := range []string{"create", "update", "flags"} {
		t.Run(mutation, func(t *testing.T) {
			root := t.TempDir()
			setAPITLSDirectories(t, root)
			writeAPITLSCertificate(t, root, "BAD_MARKER")
			handler := newTestHandlerWithScriptedKernel(t)
			client := inboundTLSTestClient(t, handler)
			node := client.createNode("mixed", 0, true)
			previousPID := handler.singBoxService.RuntimeStatus().PID
			payload := gin.H{"name": "secure", "type": "direct", "config": "{}", "enabled": true, "inbound_mode": "https"}
			switch mutation {
			case "create":
				client.request(http.MethodPost, "/api/nodes", payload, 500, nil)
			case "update":
				client.request(http.MethodPut, testNodePath(node.ID), payload, 500, nil)
			case "flags":
				client.request(http.MethodPatch, testNodePath(node.ID), gin.H{"inbound_mode": "https"}, 500, nil)
			}
			settings, err := models.LoadRuntimeSettings(t.Context(), handler.db)
			if err != nil || settings.InboundTLSCertPath != "" || settings.InboundTLSKeyPath != "" || handler.singBoxService.RuntimeStatus().PID != previousPID {
				t.Fatalf("节点变更失败后自动选证或旧进程未回滚：%+v，%v", settings, err)
			}
			nodes, err := models.LoadProxyNodes(t.Context(), handler.db)
			if err != nil || len(nodes) != 1 || nodes[0].InboundMode != models.InboundModeMixed || nodes[0].Name != "mixed" {
				t.Fatalf("节点变更失败后节点未回滚：%+v，%v", nodes, err)
			}
		})
	}
}

func TestHTTPSAutoSelectionPreservesExistingSavedPaths(t *testing.T) {
	root := t.TempDir()
	setAPITLSDirectories(t, root)
	writeAPITLSCertificate(t, root, "proxy")
	handler := newTestHandler(t, nil)
	if _, err := handler.db.Exec("UPDATE settings SET inbound_tls_cert_path='previous-cert',inbound_tls_key_path='previous-key' WHERE singleton_key=1"); err != nil {
		t.Fatal(err)
	}
	client := inboundTLSTestClient(t, handler)
	client.request(http.MethodPost, "/api/nodes", gin.H{"name": "secure", "type": "direct", "config": "{}", "enabled": true, "inbound_mode": "https"}, 201, nil)
	settings, err := models.LoadRuntimeSettings(t.Context(), handler.db)
	if err != nil || settings.InboundTLSCertPath != "previous-cert" || settings.InboundTLSKeyPath != "previous-key" {
		t.Fatalf("自动选证覆盖了非空的旧选择：%+v，%v", settings, err)
	}
}

func TestHTTPSNodeModeValidationAndPreservation(t *testing.T) {
	root := t.TempDir()
	setAPITLSDirectories(t, root)
	client := inboundTLSTestClient(t, newTestHandler(t, nil))
	mixed := client.createNode("mixed", 0, true)
	if mixed.InboundMode != models.InboundModeMixed {
		t.Fatalf("新节点默认入站模式错误：%q", mixed.InboundMode)
	}
	var settings struct {
		CertPath string                    `json:"inbound_tls_cert_path"`
		KeyPath  string                    `json:"inbound_tls_key_path"`
		TLS      services.InboundTLSStatus `json:"inbound_tls"`
	}
	client.request(http.MethodGet, "/api/settings", nil, 200, &settings)
	if settings.CertPath != "" || settings.KeyPath != "" || settings.TLS.Ready || settings.TLS.SelectionMode != "none" {
		t.Fatalf("无证书设置状态错误：%+v", settings)
	}
	payload := gin.H{"name": "https", "type": "direct", "config": "{}", "enabled": true, "inbound_mode": "https"}
	var failure gin.H
	client.request(http.MethodPost, "/api/nodes", payload, 400, &failure)
	if !strings.Contains(failure["error"].(string), "TLS 证书") {
		t.Fatalf("缺少可操作的 TLS 证书错误提示：%+v", failure)
	}
	client.request(http.MethodPut, testNodePath(mixed.ID), payload, 400, nil)
	client.request(http.MethodPatch, testNodePath(mixed.ID), gin.H{"inbound_mode": "https"}, 400, nil)
	for _, invalid := range []any{"socks5", 1, nil} {
		client.request(http.MethodPatch, testNodePath(mixed.ID), gin.H{"inbound_mode": invalid}, 400, nil)
	}
	writeAPITLSCertificate(t, root, "first")
	client.request(http.MethodGet, "/api/settings", nil, 200, &settings)
	if !settings.TLS.Ready || settings.TLS.SelectionMode != "auto" {
		t.Fatalf("单证书未自动启用：%+v", settings.TLS)
	}
	client.request(http.MethodPatch, testNodePath(mixed.ID), gin.H{"inbound_mode": " HTTPS "}, 200, &mixed)
	if mixed.InboundMode != models.InboundModeHTTPS {
		t.Fatal("PATCH 未保存规范化后的 HTTPS 模式")
	}
	delete(payload, "inbound_mode")
	client.request(http.MethodPut, testNodePath(mixed.ID), payload, 200, &mixed)
	if mixed.InboundMode != models.InboundModeHTTPS {
		t.Fatal("完整编辑省略入站模式时没有保留旧值")
	}
	payload["inbound_mode"] = "https"
	var created models.ProxyNode
	client.request(http.MethodPost, "/api/nodes", payload, 201, &created)
	if created.InboundMode != models.InboundModeHTTPS {
		t.Fatal("POST 未保存 HTTPS 入站模式")
	}
	client.request(http.MethodPatch, testNodePath(created.ID), gin.H{"inbound_mode": "mixed", "tcp_reuse_enabled": false}, 200, &created)
	if created.InboundMode != models.InboundModeMixed || created.TCPReuseEnabled {
		t.Fatal("模式切换改变了预期之外的开关")
	}
	setAPITLSDirectories(t, filepath.Join(root, "missing-after-renewal"))
	client.request(http.MethodPatch, testNodePath(mixed.ID), gin.H{"enabled": false}, 200, &mixed)
	if mixed.Enabled || mixed.InboundMode != models.InboundModeHTTPS {
		t.Fatal("证书丢失后不能安全禁用 HTTPS 节点")
	}
	client.request(http.MethodPatch, testNodePath(mixed.ID), gin.H{"enabled": true}, 400, nil)
}

func TestInboundTLSSelectionAppliesRuntimeAndResetsOnlyHTTPSChecks(t *testing.T) {
	root := t.TempDir()
	setAPITLSDirectories(t, root)
	firstCert, firstKey := writeAPITLSCertificate(t, root, "first")
	handler := newTestHandler(t, nil)
	client := inboundTLSTestClient(t, handler)
	secure := client.createNode("secure", 0, true)
	mixed := client.createNode("mixed", 0, true)
	client.request(http.MethodPatch, testNodePath(secure.ID), gin.H{"inbound_mode": "https"}, 200, &secure)
	client.request(http.MethodPut, "/api/settings", gin.H{"inbound_tls_cert_path": firstCert, "inbound_tls_key_path": firstKey}, 200, nil)
	secondCert, secondKey := writeAPITLSCertificate(t, root, "second")
	outsideCert, outsideKey := writeAPITLSCertificate(t, t.TempDir(), "outside")
	for _, pair := range [][2]string{{outsideCert, outsideKey}, {firstCert, secondKey}, {firstCert, ""}} {
		client.request(http.MethodPut, "/api/settings", gin.H{"inbound_tls_cert_path": pair[0], "inbound_tls_key_path": pair[1]}, 400, nil)
	}
	if _, err := handler.db.Exec("UPDATE proxy_nodes SET check_status='ok',node_ip='192.0.2.1',latency=15,checked_at=CURRENT_TIMESTAMP"); err != nil {
		t.Fatal(err)
	}
	previousPID := handler.singBoxService.RuntimeStatus().PID
	client.request(http.MethodPut, "/api/settings", gin.H{"inbound_tls_cert_path": secondCert, "inbound_tls_key_path": secondKey}, 200, nil)
	if currentPID := handler.singBoxService.RuntimeStatus().PID; previousPID == 0 || currentPID == previousPID {
		t.Fatal("有效证书选择变更未重启运行时")
	}
	client.request(http.MethodGet, testNodePath(secure.ID), nil, 200, &secure)
	client.request(http.MethodGet, testNodePath(mixed.ID), nil, 200, &mixed)
	if secure.CheckStatus != "untested" || secure.NodeIP != "" || secure.CheckedAt != nil || mixed.CheckStatus != "ok" || mixed.NodeIP == "" {
		t.Fatalf("证书切换后的检测状态错误：HTTPS %+v，mixed %+v", secure, mixed)
	}
	var saved struct {
		CertPath string                    `json:"inbound_tls_cert_path"`
		KeyPath  string                    `json:"inbound_tls_key_path"`
		TLS      services.InboundTLSStatus `json:"inbound_tls"`
	}
	client.request(http.MethodGet, "/api/settings", nil, 200, &saved)
	if saved.CertPath != secondCert || saved.KeyPath != secondKey || !saved.TLS.Ready || saved.TLS.SelectionMode != "selected" || saved.TLS.Active.CertPath != secondCert {
		t.Fatalf("证书选择与实时扫描未对齐：%+v", saved)
	}
	config, err := os.ReadFile(handler.singBoxService.ConfigPath())
	if err != nil || !strings.Contains(string(config), secondCert) || strings.Contains(string(config), firstCert) {
		t.Fatalf("运行时 TLS 路径未更新：%s，%v", config, err)
	}
	client.request(http.MethodPut, "/api/settings", gin.H{"inbound_tls_cert_path": "", "inbound_tls_key_path": ""}, 400, nil)
	client.request(http.MethodPatch, testNodePath(secure.ID), gin.H{"enabled": false}, 200, nil)
	client.request(http.MethodPut, "/api/settings", gin.H{"inbound_tls_cert_path": "", "inbound_tls_key_path": ""}, 200, nil)
	var failure gin.H
	client.request(http.MethodPatch, testNodePath(secure.ID), gin.H{"enabled": true}, 400, &failure)
	if !strings.Contains(failure["error"].(string), "选择") {
		t.Fatalf("多证书未选择的错误未区分无证书：%+v", failure)
	}
}

func TestInboundTLSSelectionRollsBackWhenKernelRejectsConfig(t *testing.T) {
	root := t.TempDir()
	setAPITLSDirectories(t, root)
	goodCert, goodKey := writeAPITLSCertificate(t, root, "good")
	handler := newTestHandlerWithScriptedKernel(t)
	client := inboundTLSTestClient(t, handler)
	node := client.createNode("secure", 0, true)
	client.request(http.MethodPatch, testNodePath(node.ID), gin.H{"inbound_mode": "https"}, 200, nil)
	client.request(http.MethodPut, "/api/settings", gin.H{"inbound_tls_cert_path": goodCert, "inbound_tls_key_path": goodKey}, 200, nil)
	previousPID := handler.singBoxService.RuntimeStatus().PID
	badCert, badKey := writeAPITLSCertificate(t, root, "BAD_MARKER")
	client.request(http.MethodPut, "/api/settings", gin.H{"inbound_tls_cert_path": badCert, "inbound_tls_key_path": badKey}, 500, nil)
	settings, err := models.LoadRuntimeSettings(t.Context(), handler.db)
	if err != nil || settings.InboundTLSCertPath != goodCert || settings.InboundTLSKeyPath != goodKey || handler.singBoxService.RuntimeStatus().PID != previousPID {
		t.Fatalf("内核拒绝后没有保留数据库选择和旧进程：%+v，%v", settings, err)
	}
}

func TestHTTPSNodeExitIPUsesModeAwareChecker(t *testing.T) {
	handler := newTestHandler(t, nil)
	var mixedCalls, secureCalls int
	handler.checkProxyIP = func(context.Context, string, string, string) (*services.IPInfo, error) {
		mixedCalls++
		return &services.IPInfo{IP: "192.0.2.1", Transport: "http"}, nil
	}
	handler.checkProxyIPWithMode = func(_ context.Context, address, username, secret, mode string) (*services.IPInfo, error) {
		secureCalls++
		if address != "localhost:31001" || username != "user" || secret != "test-key" || mode != models.InboundModeHTTPS {
			t.Fatal("HTTPS 检测没有传递节点模式或认证参数")
		}
		return &services.IPInfo{IP: "192.0.2.2", Transport: "https"}, nil
	}
	node := models.ProxyNode{InboundPort: 31001, InboundMode: models.InboundModeHTTPS, Username: "user", Password: "test-key"}
	if info, err := handler.checkNodeExitIP(t.Context(), node); err != nil || info.Transport != "https" {
		t.Fatalf("HTTPS 检测未使用模式感知调用：%+v，%v", info, err)
	}
	node.InboundMode = models.InboundModeMixed
	if _, err := handler.checkNodeExitIP(t.Context(), node); err != nil || mixedCalls != 1 || secureCalls != 1 {
		t.Fatalf("既有 mixed 测试桩被破坏：mixed=%d、https=%d、%v", mixedCalls, secureCalls, err)
	}
}

func TestCheckPathIncludesEffectiveInboundTLSSelection(t *testing.T) {
	root := t.TempDir()
	setAPITLSDirectories(t, root)
	firstCert, firstKey := writeAPITLSCertificate(t, root, "first")
	secondCert, secondKey := writeAPITLSCertificate(t, root, "second")
	node := models.ProxyNode{ID: 1, Type: "direct", Config: "{}", Enabled: true, InboundPort: 31001, InboundMode: models.InboundModeHTTPS}
	first := models.Settings{InboundTLSCertPath: firstCert, InboundTLSKeyPath: firstKey}
	second := models.Settings{InboundTLSCertPath: secondCert, InboundTLSKeyPath: secondKey}
	firstPath, secondPath := nodeCheckPaths([]models.ProxyNode{node}, first)[node.ID], nodeCheckPaths([]models.ProxyNode{node}, second)[node.ID]
	if firstPath == secondPath || firstPath.inboundMode != "https" || firstPath.certPath != firstCert || secondPath.keyPath != secondKey {
		t.Fatal("有效 TLS 证书路径未纳入检测路径")
	}
	node.InboundMode = models.InboundModeMixed
	if nodeCheckPaths([]models.ProxyNode{node}, first)[node.ID] != nodeCheckPaths([]models.ProxyNode{node}, second)[node.ID] {
		t.Fatal("不使用 TLS 的 mixed 节点受到证书选择影响")
	}
	node.InboundPort = 0
	mixedPath := nodeCheckPaths([]models.ProxyNode{node}, first)[node.ID]
	node.InboundMode = models.InboundModeHTTPS
	if nodeCheckPaths([]models.ProxyNode{node}, second)[node.ID] != mixedPath {
		t.Fatal("无入站端口时闲置的模式或 TLS 设置影响了隔离检测路径")
	}
}
