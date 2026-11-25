package api

import (
	"testing"

	"sb-proxy/backend/models"
)

func TestCheckPathUsesEffectiveRoute(t *testing.T) {
	base := models.ProxyNode{ID: 5, Name: "public", Type: "direct", Config: `{}`, Enabled: true, UpstreamMode: "global", InboundPort: 34000, Username: "user", Password: "original"}
	global := models.Settings{GlobalUpstreamType: "socks5", GlobalUpstreamConfig: `{"server":"example.com","server_port":1080}`}
	identity := func(node models.ProxyNode, settings models.Settings) checkPath {
		return nodeCheckPaths([]models.ProxyNode{node}, settings)[node.ID]
	}
	initial := identity(base, global)
	changed := base
	changed.Name, changed.Remark, changed.GroupID, changed.SortOrder = "other", "label", 2, 10
	changed.TCPReuseEnabled, changed.InboundPortPinned = true, true
	changed.Config = `{"detour":"unused"}`
	changed.UpstreamType, changed.UpstreamConfig = "http", `{"server":"unused"}`
	if identity(changed, global) != initial {
		t.Fatal("metadata or inactive detours changed the probe path")
	}
	global.GlobalUpstreamEnabled = true
	if identity(base, global) == initial {
		t.Fatal("enabled global upstream was omitted from the path")
	}
	changed.UpstreamMode = "custom"
	changed.UpstreamType, changed.UpstreamConfig = "socks5", `{"server_port":1080, "server":"example.com"}`
	if identity(base, global) != identity(changed, global) {
		t.Fatal("equivalent custom and global routes have different identities")
	}
	base.Password = "changed"
	if identity(base, global) == identity(changed, global) {
		t.Fatal("inbound credentials did not change the path")
	}
	base.InboundPort, changed.InboundPort = 0, 0
	if identity(base, global) != identity(changed, global) {
		t.Fatal("unused credentials changed isolated probe path")
	}
}

func TestResetChecksAcrossLegacyDetourChain(t *testing.T) {
	handler := newTestHandler(t, nil)
	db := handler.db
	first := insertTestNodeWithPortAndOrder(t, db, "first", 34001, 0)
	second := insertTestNodeWithPortAndOrder(t, db, "second", 34002, 1)
	third := insertTestNodeWithPortAndOrder(t, db, "third", 34003, 2)
	independent := insertTestNodeWithPortAndOrder(t, db, "independent", 34004, 3)
	for id, config := range map[int]string{second: `{"detour":"first"}`, third: `{"detour":"second"}`} {
		if _, err := db.Exec("UPDATE proxy_nodes SET type='direct',config=?,upstream_mode='legacy' WHERE id=?", config, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE proxy_nodes SET check_status='failed',check_error='offline',checked_at=CURRENT_TIMESTAMP,location='old',node_ip='192.0.2.1',country_code='US',latency=300`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	nodes, err := models.LoadProxyNodes(t.Context(), tx)
	if err != nil {
		t.Fatal(err)
	}
	before := nodeCheckPaths(nodes, models.Settings{})
	for index := range nodes {
		if nodes[index].ID == second {
			nodes[index].InboundPort = 34567
		}
	}
	if err = resetChangedChecks(t.Context(), tx, before, nodes, models.Settings{}, first); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{first, second, third, independent} {
		node, err := models.LoadProxyNode(t.Context(), tx, id)
		if err != nil {
			t.Fatal(err)
		}
		if id == independent {
			if node.CheckRevision != 0 || node.CheckStatus != "failed" || node.CheckedAt == nil {
				t.Fatalf("independent node changed: %+v", node)
			}
			continue
		}
		if node.CheckRevision != 1 || node.CheckStatus != "untested" || node.CheckError != "" || node.CheckedAt != nil || node.NodeIP != "" || node.Location != "" || node.CountryCode != "" || node.Latency != 0 {
			t.Fatalf("node %d left stale check data", id)
		}
	}
}
