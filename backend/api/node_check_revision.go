package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"

	"sb-proxy/backend/models"
)

type checkOutbound struct {
	protocol       string
	config         [sha256.Size]byte
	upstreamType   string
	upstreamConfig [sha256.Size]byte
	detourID       int
	enabled        bool
}

type checkPath struct {
	outbound           checkOutbound
	port               int
	username, password string
}

func checkConfig(raw string) ([sha256.Size]byte, string) {
	var fields map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&fields) != nil || fields == nil {
		return sha256.Sum256([]byte(raw)), ""
	}
	detour, _ := fields["detour"].(string)
	delete(fields, "detour")
	canonical, _ := json.Marshal(fields)
	return sha256.Sum256(canonical), strings.TrimSpace(detour)
}

func nodeCheckPaths(nodes []models.ProxyNode, settings models.Settings) map[int]checkPath {
	names := make(map[string]int, len(nodes))
	tags := make(map[string]int, len(nodes))
	for _, node := range nodes {
		if node.Enabled {
			names[strings.TrimSpace(node.Name)] = node.ID
			tags["node-"+strconv.Itoa(node.ID)+"-out"] = node.ID
		}
	}
	globalConfig, _ := checkConfig(settings.GlobalUpstreamConfig)
	paths := make(map[int]checkPath, len(nodes))
	for _, node := range nodes {
		config, detour := checkConfig(node.Config)
		outbound := checkOutbound{protocol: node.Type, config: config, enabled: node.Enabled}
		mode, _ := models.NormalizeUpstreamMode(node.UpstreamMode)
		switch mode {
		case models.UpstreamModeLegacy:
			if detour != "" && detour != "direct" {
				outbound.detourID = tags[detour]
				if outbound.detourID == 0 {
					outbound.detourID = names[detour]
				}
			}
		case models.UpstreamModeGlobal:
			if settings.GlobalUpstreamEnabled {
				outbound.upstreamType, outbound.upstreamConfig = settings.GlobalUpstreamType, globalConfig
			}
		case models.UpstreamModeCustom:
			outbound.upstreamType = node.UpstreamType
			outbound.upstreamConfig, _ = checkConfig(node.UpstreamConfig)
		}
		path := checkPath{outbound: outbound, port: node.InboundPort}
		if node.InboundPort > 0 {
			path.username, path.password = node.Username, node.Password
		}
		paths[node.ID] = path
	}
	return paths
}

func resetChangedChecks(ctx context.Context, tx *sql.Tx, before map[int]checkPath, nodes []models.ProxyNode, settings models.Settings, replacedID int) error {
	after := nodeCheckPaths(nodes, settings)
	dependents := map[int][]int{}
	for _, node := range nodes {
		if models.IsPolicy(node.Type) {
			parsed, err := node.ParseConfig()
			if err != nil {
				return err
			}
			for _, member := range parsed.(*models.PolicyConfig).MemberIDs {
				dependents[member] = append(dependents[member], node.ID)
			}
		}
	}
	changed := map[int]bool{}
	visited := map[int]bool{}
	var outboundChanges []int
	for id, path := range after {
		if path.outbound.detourID != 0 {
			dependents[path.outbound.detourID] = append(dependents[path.outbound.detourID], id)
		}
		old, exists := before[id]
		if !exists {
			continue
		}
		if old != path || id == replacedID {
			changed[id] = true
		}
		if old.outbound != path.outbound || id == replacedID {
			visited[id] = true
			outboundChanges = append(outboundChanges, id)
		}
	}
	for index := 0; index < len(outboundChanges); index++ {
		for _, id := range dependents[outboundChanges[index]] {
			changed[id] = true
			if !visited[id] {
				visited[id] = true
				outboundChanges = append(outboundChanges, id)
			}
		}
	}
	for index := range nodes {
		node := &nodes[index]
		if !changed[node.ID] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE proxy_nodes SET check_revision=check_revision+1,
			check_status='untested',check_error='',checked_at=NULL,node_ip='',location='',country_code='',latency=0 WHERE id=?`, node.ID); err != nil {
			return err
		}
		node.CheckRevision++
		node.CheckStatus, node.CheckError, node.CheckedAt = "untested", "", nil
		node.NodeIP, node.Location, node.CountryCode, node.Latency = "", "", "", 0
	}
	return nil
}
