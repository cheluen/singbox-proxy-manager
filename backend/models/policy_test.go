package models

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestIsPolicy(testContext *testing.T) {
	for _, protocol := range []string{"failover", "loadbalance", "urltest"} {
		if !IsPolicy(protocol) {
			testContext.Fatalf("未识别策略类型 %s", protocol)
		}
	}
	for _, protocol := range []string{"direct", "http", "url-test", "URLTEST", ""} {
		if IsPolicy(protocol) {
			testContext.Fatalf("错误识别策略类型 %s", protocol)
		}
	}
}

func TestPolicyURLTestToleranceNormalization(testContext *testing.T) {
	for _, testCase := range []struct {
		name      string
		field     string
		tolerance int
	}{
		{name: "默认", tolerance: 50},
		{name: "null", field: `,"tolerance":null`, tolerance: 50},
		{name: "显式零", field: `,"tolerance":0`, tolerance: 0},
		{name: "合法正数", field: `,"tolerance":125`, tolerance: 125},
		{name: "上限", field: `,"tolerance":5000`, tolerance: 5000},
	} {
		testContext.Run(testCase.name, func(testContext *testing.T) {
			var config PolicyConfig
			if err := json.Unmarshal([]byte(`{"member_ids":[1],"strategy":"consistent-hashing"`+testCase.field+`}`), &config); err != nil {
				testContext.Fatal(err)
			}
			if config.EffectiveTolerance() != testCase.tolerance {
				testContext.Fatalf("归一化前容差错误：%d", config.EffectiveTolerance())
			}
			if err := config.normalize("urltest"); err != nil {
				testContext.Fatal(err)
			}
			if config.Tolerance == nil || *config.Tolerance != testCase.tolerance || config.Strategy != "" || config.Interval != 30 {
				testContext.Fatalf("urltest 默认值或字段清理错误：%+v", config)
			}
			encoded, err := json.Marshal(config)
			if err != nil || !strings.Contains(string(encoded), fmt.Sprintf(`"tolerance":%d`, testCase.tolerance)) || strings.Contains(string(encoded), "strategy") {
				testContext.Fatalf("容差未正确保留至 JSON：%s，%v", encoded, err)
			}
		})
	}
	for _, tolerance := range []int{-1, -50, 5001} {
		config := PolicyConfig{MemberIDs: []int{1}, Tolerance: &tolerance}
		if err := config.normalize("urltest"); err == nil || !strings.Contains(err.Error(), "tolerance must be between 0 and 5000") {
			testContext.Fatalf("非法容差 %d 未被清晰拒绝：%v", tolerance, err)
		}
	}
}

func TestPolicyStrategyAndToleranceFields(testContext *testing.T) {
	for _, testCase := range []struct {
		protocol string
		strategy string
		want     string
		invalid  bool
	}{
		{protocol: "failover", strategy: "invalid"},
		{protocol: "loadbalance", want: "round-robin"},
		{protocol: "loadbalance", strategy: "round-robin", want: "round-robin"},
		{protocol: "loadbalance", strategy: "consistent-hashing", want: "consistent-hashing"},
		{protocol: "loadbalance", strategy: "invalid", invalid: true},
	} {
		testContext.Run(testCase.protocol+"/"+testCase.strategy, func(testContext *testing.T) {
			tolerance := -1
			config := PolicyConfig{MemberIDs: []int{1}, Strategy: testCase.strategy, Tolerance: &tolerance}
			err := config.normalize(testCase.protocol)
			if (err != nil) != testCase.invalid || config.Tolerance != nil {
				testContext.Fatalf("原策略校验或容差清理改变：%+v，%v", config, err)
			}
			if testCase.invalid {
				return
			}
			encoded, err := json.Marshal(config)
			if err != nil || config.Strategy != testCase.want || strings.Contains(string(encoded), "tolerance") {
				testContext.Fatalf("原策略 JSON 或选路方式改变：%s，%v", encoded, err)
			}
		})
	}
	config := PolicyConfig{MemberIDs: []int{1}, Strategy: "invalid"}
	if err := config.normalize("urltest"); err != nil || config.Strategy != "" {
		testContext.Fatalf("urltest 未清空无关 strategy：%+v，%v", config, err)
	}
}

func TestPolicyMemberSourcesNormalization(t *testing.T) {
	config := PolicyConfig{MemberIDs: []int{2, 2, 1}, MemberSources: []PolicyMemberSource{
		{Type: PolicyMemberSourceGroup, ID: 1}, {Type: PolicyMemberSourceNode, ID: 2},
		{Type: PolicyMemberSourceGroup, ID: 1}, {Type: PolicyMemberSourceNode, ID: 2},
	}}
	if err := config.normalize("failover"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(config.MemberSources, []PolicyMemberSource{{Type: PolicyMemberSourceGroup, ID: 1}, {Type: PolicyMemberSourceNode, ID: 2}}) || !slices.Equal(config.MemberIDs, []int{2, 1}) {
		t.Fatalf("成员来源未按首次出现顺序去重：%+v", config)
	}
	for _, source := range []PolicyMemberSource{{Type: "invalid", ID: 1}, {Type: PolicyMemberSourceNode, ID: 0}, {Type: PolicyMemberSourceGroup, ID: -1}} {
		invalid := PolicyConfig{MemberSources: []PolicyMemberSource{source}}
		if err := invalid.normalize("failover"); err == nil {
			t.Fatalf("接受了非法成员来源：%+v", source)
		}
	}
}

func TestPolicyNodeOnlySourcesKeepLegacyJSON(t *testing.T) {
	for _, members := range [][]int{nil, {3, 2, 3}} {
		config := PolicyConfig{MemberIDs: members, MemberSources: []PolicyMemberSource{
			{Type: PolicyMemberSourceNode, ID: 3}, {Type: PolicyMemberSourceNode, ID: 2}, {Type: PolicyMemberSourceNode, ID: 3},
		}}
		if err := config.normalize("failover"); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(config)
		if err != nil || config.MemberSources != nil || !slices.Equal(config.MemberIDs, []int{3, 2}) || strings.Contains(string(encoded), "member_sources") {
			t.Fatalf("纯手动成员未恢复旧 JSON 格式：%s，%v", encoded, err)
		}
	}
}
