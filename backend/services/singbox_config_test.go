package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"sb-proxy/backend/models"
)

func loadGeneratedConfigMap(t *testing.T, dir string) map[string]interface{} {
	t.Helper()

	configPath := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("unmarshal generated config: %v", err)
	}

	return config
}

func routeRulesFromConfig(t *testing.T, config map[string]interface{}) []interface{} {
	t.Helper()

	route, ok := config["route"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing route section")
	}
	rules, ok := route["rules"].([]interface{})
	if !ok {
		t.Fatalf("missing route.rules")
	}
	return rules
}

func inboundsFromConfig(t *testing.T, config map[string]interface{}) []interface{} {
	t.Helper()

	inbounds, ok := config["inbounds"].([]interface{})
	if !ok {
		t.Fatalf("missing inbounds")
	}
	return inbounds
}

func findRuleIndexByAuthUser(rules []interface{}, authUser string) int {
	for idx, raw := range rules {
		rule, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		authUsers, ok := rule["auth_user"].([]interface{})
		if !ok {
			continue
		}
		for _, candidate := range authUsers {
			if candidateStr, ok := candidate.(string); ok && candidateStr == authUser {
				return idx
			}
		}
	}
	return -1
}

func findRuleIndexByInboundTag(rules []interface{}, inboundTag string) int {
	for idx, raw := range rules {
		rule, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		inbounds, ok := rule["inbound"].([]interface{})
		if !ok {
			continue
		}
		for _, candidate := range inbounds {
			if candidateStr, ok := candidate.(string); ok && candidateStr == inboundTag {
				return idx
			}
		}
	}
	return -1
}

func usersForInboundTag(t *testing.T, inbounds []interface{}, tag string) map[string]string {
	t.Helper()

	for _, rawInbound := range inbounds {
		inbound, ok := rawInbound.(map[string]interface{})
		if !ok {
			continue
		}
		inboundTag, _ := inbound["tag"].(string)
		if inboundTag != tag {
			continue
		}
		users := map[string]string{}
		rawUsers, hasUsers := inbound["users"].([]interface{})
		if !hasUsers {
			return users
		}
		for _, rawUser := range rawUsers {
			user, ok := rawUser.(map[string]interface{})
			if !ok {
				continue
			}
			username, _ := user["username"].(string)
			password, _ := user["password"].(string)
			users[username] = password
		}
		return users
	}

	t.Fatalf("inbound %s not found", tag)
	return nil
}

func TestGenerateGlobalConfigAddsTCPReuseRouting(t *testing.T) {
	configDir := t.TempDir()
	service := NewSingBoxService(configDir)

	nodes := []models.ProxyNode{
		{
			ID:              1,
			Name:            "node-1",
			Type:            "direct",
			Config:          "{}",
			InboundPort:     30001,
			Username:        "entry",
			Password:        "entry-pass",
			TCPReuseEnabled: true,
			Enabled:         true,
		},
		{
			ID:              2,
			Name:            "node-2",
			Type:            "direct",
			Config:          "{}",
			InboundPort:     30005,
			Username:        "target",
			Password:        "target-pass",
			TCPReuseEnabled: true,
			Enabled:         true,
		},
	}

	if err := service.GenerateGlobalConfig(nodes); err != nil {
		t.Fatalf("GenerateGlobalConfig failed: %v", err)
	}

	config := loadGeneratedConfigMap(t, configDir)
	rules := routeRulesFromConfig(t, config)
	inbounds := inboundsFromConfig(t, config)

	authRuleIdx := findRuleIndexByAuthUser(rules, "target+30005")
	if authRuleIdx < 0 {
		t.Fatalf("expected auth_user rule for target+30005")
	}
	directRuleIdx := findRuleIndexByInboundTag(rules, "node-2-in")
	if directRuleIdx < 0 {
		t.Fatalf("expected direct inbound rule for node-2-in")
	}
	if authRuleIdx >= directRuleIdx {
		t.Fatalf("auth_user rule should be before direct inbound rules, auth=%d direct=%d", authRuleIdx, directRuleIdx)
	}

	users := usersForInboundTag(t, inbounds, "node-1-in")
	if users["entry"] != "entry-pass" {
		t.Fatalf("expected inbound base auth for node-1")
	}
	if users["target+30005"] != "target-pass" {
		t.Fatalf("expected shared reuse auth target+30005 on node-1 inbound")
	}
}

func TestGenerateGlobalConfigRespectsTCPReuseSwitch(t *testing.T) {
	configDir := t.TempDir()
	service := NewSingBoxService(configDir)

	nodes := []models.ProxyNode{
		{
			ID:              1,
			Name:            "node-1",
			Type:            "direct",
			Config:          "{}",
			InboundPort:     30001,
			Username:        "entry",
			Password:        "entry-pass",
			TCPReuseEnabled: true,
			Enabled:         true,
		},
		{
			ID:              2,
			Name:            "node-2",
			Type:            "direct",
			Config:          "{}",
			InboundPort:     30005,
			Username:        "target",
			Password:        "target-pass",
			TCPReuseEnabled: false,
			Enabled:         true,
		},
	}

	if err := service.GenerateGlobalConfig(nodes); err != nil {
		t.Fatalf("GenerateGlobalConfig failed: %v", err)
	}

	config := loadGeneratedConfigMap(t, configDir)
	rules := routeRulesFromConfig(t, config)
	if idx := findRuleIndexByAuthUser(rules, "target+30005"); idx >= 0 {
		t.Fatalf("did not expect auth_user route for disabled tcp reuse node")
	}

	users := usersForInboundTag(t, inboundsFromConfig(t, config), "node-1-in")
	if _, ok := users["target+30005"]; ok {
		t.Fatalf("did not expect shared auth user for disabled tcp reuse node")
	}
}

func TestGenerateGlobalConfigRejectsUsernameWithPlus(t *testing.T) {
	configDir := t.TempDir()
	service := NewSingBoxService(configDir)

	nodes := []models.ProxyNode{
		{
			ID:              1,
			Name:            "node-1",
			Type:            "direct",
			Config:          "{}",
			InboundPort:     30001,
			Username:        "bad+name",
			Password:        "pass",
			TCPReuseEnabled: true,
			Enabled:         true,
		},
	}

	if err := service.GenerateGlobalConfig(nodes); err == nil {
		t.Fatalf("expected username with '+' to be rejected")
	}
}
