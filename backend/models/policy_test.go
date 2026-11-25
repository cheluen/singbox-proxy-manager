package models

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

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
