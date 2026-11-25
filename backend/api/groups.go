package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	appdb "sb-proxy/backend/database"
	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
)

type groupConflict struct {
	code    string
	details gin.H
}

func (conflict groupConflict) Error() string { return conflict.code }

func groupError(c *gin.Context, err error) {
	var conflict groupConflict
	if errors.As(err, &conflict) {
		body := conflict.details
		if body == nil {
			body = gin.H{}
		}
		body["code"], body["error"] = conflict.code, conflict.code
		c.JSON(409, body)
		return
	}
	writeNodeMutationError(c, err, true)
}

func groupSaveError(c *gin.Context, err error, validatingUpstream bool) {
	if validatingUpstream && errors.Is(err, services.ErrConfigRejected) {
		err = invalidNodeRequest(err)
	}
	groupError(c, err)
}

func requestGroup(ctx context.Context, db sqlQueryRower, id *int) (models.NodeGroup, error) {
	groupID := 0
	if id != nil {
		if *id <= 0 {
			return models.NodeGroup{}, invalidNodeRequest(errors.New("invalid group_id"))
		}
		groupID = *id
	}
	group, err := models.LoadGroup(ctx, db, groupID)
	if errors.Is(err, sql.ErrNoRows) {
		return group, nodeRequestError{404, errors.New("group not found")}
	}
	return group, err
}

func (h *Handler) respondNode(c *gin.Context, id, status int) {
	node, err := models.LoadProxyNode(c.Request.Context(), h.db, id)
	if err != nil {
		writeNodeReadError(c, err)
		return
	}
	c.JSON(status, h.singBoxService.WithPolicyStatus(node))
}

func (h *Handler) respondGroup(c *gin.Context, id, status int) {
	group, err := requestGroup(c.Request.Context(), h.db, &id)
	if err != nil {
		groupError(c, err)
		return
	}
	c.JSON(status, group)
}

func (h *Handler) GetGroups(c *gin.Context) {
	groups, err := models.LoadGroups(c.Request.Context(), h.db)
	if err != nil {
		c.JSON(500, gin.H{"error": "database error"})
		return
	}
	c.JSON(200, groups)
}
func (h *Handler) GetGroup(c *gin.Context) {
	if id, ok := parseNodeID(c, true); ok {
		h.respondGroup(c, id, 200)
	}
}

type groupInput struct {
	Name                     *string `json:"name"`
	Allocate                 *bool   `json:"allocate_port"`
	Start                    *int    `json:"start_port"`
	URL                      *string `json:"subscription_url"`
	Auto                     *bool   `json:"subscription_auto_update"`
	Minutes                  *int    `json:"subscription_interval_minutes"`
	ReleasePins              bool    `json:"release_pins"`
	UpstreamEnabled          *bool   `json:"upstream_enabled"`
	UpstreamType             *string `json:"upstream_type"`
	UpstreamConfig           *string `json:"upstream_config"`
	ExpectedUpstreamRevision *int64  `json:"expected_upstream_revision"`
	ExpectedUpstreamEnabled  *bool   `json:"expected_upstream_enabled"`
}

func (input groupInput) apply(group *models.NodeGroup, service *services.SingBoxService) error {
	if input.Name != nil {
		group.Name = strings.TrimSpace(*input.Name)
	}
	if input.Start != nil {
		group.StartPort = *input.Start
	}
	if input.Allocate != nil {
		group.AllocatePort = *input.Allocate
	}
	if input.URL != nil {
		group.SubscriptionURL = strings.TrimSpace(*input.URL)
	}
	if input.Auto != nil {
		group.SubscriptionAutoUpdate = *input.Auto
	}
	if input.Minutes != nil {
		group.SubscriptionIntervalMinutes = *input.Minutes
	}
	if group.Name == "" || validateCharacterLimit("name", group.Name, 255) != nil {
		return invalidNodeRequest(errors.New("invalid group name"))
	}
	if err := validateStartPort(group.StartPort); err != nil {
		return invalidNodeRequest(err)
	}
	if group.SubscriptionIntervalMinutes < 1 || group.SubscriptionIntervalMinutes > 525600 {
		return invalidNodeRequest(errors.New("subscription interval must be between 1 and 525600 minutes"))
	}
	if group.SubscriptionURL != "" {
		target, err := url.Parse(group.SubscriptionURL)
		if err != nil || len(group.SubscriptionURL) > 4096 || target.Hostname() == "" || (target.Scheme != "http" && target.Scheme != "https") {
			return invalidNodeRequest(errors.New("invalid subscription URL"))
		}
	}
	if group.SubscriptionAutoUpdate && group.SubscriptionURL == "" {
		return invalidNodeRequest(errors.New("subscription URL required for automatic updates"))
	}
	previousType, previousConfig := group.UpstreamType, group.UpstreamConfig
	if input.UpstreamEnabled != nil {
		group.UpstreamEnabled = *input.UpstreamEnabled
	}
	if input.UpstreamType != nil {
		group.UpstreamType = strings.ToLower(strings.TrimSpace(*input.UpstreamType))
	}
	if input.UpstreamConfig != nil {
		group.UpstreamConfig = strings.TrimSpace(*input.UpstreamConfig)
	}
	definitionChanged := previousType != group.UpstreamType || previousConfig != group.UpstreamConfig
	if group.UpstreamEnabled || definitionChanged && (group.UpstreamType != "" || group.UpstreamConfig != "") {
		if err := validateCharacterLimit("upstream_type", group.UpstreamType, maxUpstreamTypeCharacters); err != nil {
			return invalidNodeRequest(err)
		}
		if err := service.ValidateUpstreamDefinition(models.ProxyDefinition{Type: group.UpstreamType, Config: group.UpstreamConfig}); err != nil {
			return invalidNodeRequest(err)
		}
	}
	return nil
}

func (h *Handler) CreateGroup(c *gin.Context) {
	var input groupInput
	if !bindRequestJSON(c, &input) {
		return
	}
	group := models.NodeGroup{StartPort: 30001, AllocatePort: true, SubscriptionIntervalMinutes: 60}
	if err := input.apply(&group, h.singBoxService); err != nil {
		groupError(c, err)
		return
	}
	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()
	_, err := h.nodeMutations.execute(c.Request.Context(), nodeMutationOperation{ApplyRuntime: group.UpstreamEnabled, ChangedGroupID: &group.ID, Mutate: func(ctx context.Context, tx *sql.Tx) error {
		var nextOrder int
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sort_order),-1)+1 FROM node_groups").Scan(&nextOrder); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO node_groups(name,sort_order,allocate_port,start_port,subscription_url,subscription_auto_update,subscription_interval_minutes,upstream_enabled,upstream_type,upstream_config) VALUES (?,?,?,?,?,?,?,?,?,?)`, group.Name, nextOrder, group.AllocatePort, group.StartPort, group.SubscriptionURL, group.SubscriptionAutoUpdate, group.SubscriptionIntervalMinutes, group.UpstreamEnabled, group.UpstreamType, group.UpstreamConfig)
		if err != nil {
			return err
		}
		id, err := appdb.LastInsertID(ctx, tx, result, appdb.DialectFor(h.db))
		group.ID = int(id)
		return err
	}})
	if err != nil {
		groupSaveError(c, err, group.UpstreamEnabled)
		return
	}
	h.respondGroup(c, group.ID, 201)
}

func (h *Handler) UpdateGroup(c *gin.Context) {
	id, ok := parseNodeID(c, true)
	if !ok {
		return
	}
	var input groupInput
	if !bindRequestJSON(c, &input) {
		return
	}
	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()
	ctx := c.Request.Context()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		groupError(c, err)
		return
	}
	defer tx.Rollback()
	previous, err := requestGroup(ctx, tx, &id)
	if err != nil {
		groupError(c, err)
		return
	}
	if (input.ExpectedUpstreamRevision != nil && *input.ExpectedUpstreamRevision != previous.UpstreamRevision) ||
		(input.ExpectedUpstreamEnabled != nil && *input.ExpectedUpstreamEnabled != previous.UpstreamEnabled) {
		groupError(c, groupConflict{"upstream_conflict", gin.H{"upstream_revision": previous.UpstreamRevision, "upstream_enabled": previous.UpstreamEnabled}})
		return
	}
	group := previous
	if err = input.apply(&group, h.singBoxService); err != nil {
		groupError(c, err)
		return
	}
	allocateChanged := group.AllocatePort != previous.AllocatePort
	reassign := allocateChanged && group.AllocatePort
	if group.AllocatePort && !allocateChanged && group.StartPort != previous.StartPort {
		preserve, err := getPreservePorts(ctx, tx)
		if err != nil {
			groupError(c, err)
			return
		}
		reassign = !preserve
	}
	definitionChanged := group.UpstreamType != previous.UpstreamType || group.UpstreamConfig != previous.UpstreamConfig
	upstreamChanged := group.UpstreamEnabled != previous.UpstreamEnabled || (group.UpstreamEnabled && definitionChanged)
	applyRuntime := upstreamChanged || (reassign || allocateChanged) && group.NodeCount > 0
	_, err = h.nodeMutations.executeTx(ctx, tx, nodeMutationOperation{ApplyRuntime: applyRuntime, ChangedGroupID: &id, Mutate: func(ctx context.Context, tx *sql.Tx) error {
		if previous.AllocatePort && !group.AllocatePort {
			pinned, err := readIDs(ctx, tx, "SELECT id FROM proxy_nodes WHERE group_id=? AND inbound_port_pinned ORDER BY id", id)
			if err != nil {
				return err
			}
			if len(pinned) > 0 && !input.ReleasePins {
				return groupConflict{"pinned_ports_exist", gin.H{"pinned_node_ids": pinned}}
			}
			if _, err = tx.ExecContext(ctx, "UPDATE proxy_nodes SET inbound_port=NULL,inbound_port_pinned=FALSE WHERE group_id=?", id); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE node_groups SET name=?,allocate_port=?,start_port=?,subscription_url=?,subscription_auto_update=?,subscription_interval_minutes=?,upstream_enabled=?,upstream_type=?,upstream_config=?,revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE id=?`, group.Name, group.AllocatePort, group.StartPort, group.SubscriptionURL, group.SubscriptionAutoUpdate, group.SubscriptionIntervalMinutes, group.UpstreamEnabled, group.UpstreamType, group.UpstreamConfig, id)
		if err != nil {
			return err
		}
		if group.SubscriptionURL != previous.SubscriptionURL {
			if _, err = tx.ExecContext(ctx, "UPDATE node_groups SET subscription_last_updated_at=NULL,subscription_last_error='' WHERE id=?", id); err != nil {
				return err
			}
		}
		if definitionChanged {
			if _, err = tx.ExecContext(ctx, "UPDATE node_groups SET upstream_revision=upstream_revision+1,upstream_ip='',upstream_location='',upstream_country_code='',upstream_latency=0,upstream_error='' WHERE id=?", id); err != nil {
				return err
			}
		}
		if reassign {
			return h.reassignAutomaticPortsTx(ctx, tx, group)
		}
		return nil
	}})
	if err != nil {
		groupSaveError(c, err, group.UpstreamEnabled && upstreamChanged)
		return
	}
	h.respondGroup(c, id, 200)
}

func readIDs(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]int, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

type ownedPortKey struct{}

func (h *Handler) checkPort(ctx context.Context, port int) error {
	manager, _ := strconv.Atoi(os.Getenv("PORT"))
	if manager == 0 {
		manager = 30000
	}
	if port == manager {
		return errors.New("management port is reserved")
	}
	if h.skipPortAvailabilityCheck {
		return nil
	}
	address := net.JoinHostPort("", strconv.Itoa(port))
	tcp, err := net.Listen("tcp", address)
	if err == nil {
		_ = tcp.Close()
	} else if owned, ok := ctx.Value(ownedPortKey{}).(func() map[int]bool); !ok || !owned()[port] {
		return fmt.Errorf("inbound port not available: %w", err)
	}
	udp, err := net.ListenPacket("udp", address)
	if err != nil {
		if owned, ok := ctx.Value(ownedPortKey{}).(func() map[int]bool); ok && owned()[port] {
			return nil
		}
		return fmt.Errorf("inbound port not available: %w", err)
	}
	return udp.Close()
}

func (h *Handler) normalizeGroups(ctx context.Context, tx *sql.Tx, affected map[int]bool) error {
	preserve, err := getPreservePorts(ctx, tx)
	if err != nil {
		return err
	}
	groups, err := models.LoadGroups(ctx, tx)
	if err != nil {
		return err
	}
	for _, group := range groups {
		if affected != nil && !affected[group.ID] {
			continue
		}
		if err = normalizeNodeSortOrderTx(ctx, tx, group.ID); err != nil {
			return err
		}
		if !preserve {
			if err = h.reassignAutomaticPortsTx(ctx, tx, group); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *Handler) moveNodes(ctx context.Context, tx *sql.Tx, ids []int, target int) error {
	group, err := requestGroup(ctx, tx, &target)
	if err != nil {
		return err
	}
	if err = requireNodeIDs(ctx, tx, ids, true); err != nil {
		return err
	}
	used, err := collectUsedInboundPortsTx(ctx, tx, 0)
	if err != nil {
		return err
	}
	next := group.StartPort
	var last int
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sort_order),-1) FROM proxy_nodes WHERE group_id=?", target).Scan(&last); err != nil {
		return err
	}
	affected := map[int]bool{}
	for _, id := range ids {
		node, err := loadNodeForMutation(ctx, tx, id)
		if err != nil {
			return err
		}
		if node.GroupID == target {
			continue
		}
		affected[node.GroupID] = true
		affected[target] = true
		last++
		port, pin := node.InboundPort, node.InboundPortPinned
		if !group.AllocatePort {
			port, pin = 0, false
		} else if port == 0 {
			port, err = h.nextBindablePort(ctx, next, used)
			if err != nil {
				return err
			}
			used[port] = struct{}{}
			next = port + 1
		}
		if _, err = tx.ExecContext(ctx, `UPDATE proxy_nodes SET group_id=?,sort_order=?,inbound_port=?,inbound_port_pinned=?,subscription_key='',subscription_name='',updated_at=CURRENT_TIMESTAMP WHERE id=?`, target, last, models.NullablePort(port), pin, id); err != nil {
			return err
		}
	}
	return h.normalizeGroups(ctx, tx, affected)
}

func (h *Handler) BatchMoveNodes(c *gin.Context) {
	var req struct {
		IDs     []int `json:"ids"`
		GroupID int   `json:"group_id"`
	}
	if !bindRequestJSON(c, &req) {
		return
	}
	if len(req.IDs) == 0 || validateNodeIDs(req.IDs) != nil {
		c.JSON(400, gin.H{"error": "invalid node ids"})
		return
	}
	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()
	_, err := h.nodeMutations.execute(c.Request.Context(), nodeMutationOperation{ApplyRuntime: true, Mutate: func(ctx context.Context, tx *sql.Tx) error { return h.moveNodes(ctx, tx, req.IDs, req.GroupID) }})
	if err != nil {
		groupError(c, err)
		return
	}
	c.JSON(200, gin.H{"group_id": req.GroupID, "ids": req.IDs})
}

func (h *Handler) DeleteGroup(c *gin.Context) {
	id, ok := parseNodeID(c, true)
	if !ok {
		return
	}
	mode := c.Query("mode")
	target, _ := strconv.Atoi(c.Query("target_group_id"))
	if mode != "" && mode != "move" && mode != "delete_nodes" || mode == "move" && (target <= 0 || target == id) {
		c.JSON(400, gin.H{"error": "invalid group deletion request"})
		return
	}
	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()
	group, err := requestGroup(c.Request.Context(), h.db, &id)
	if err != nil {
		groupError(c, err)
		return
	}
	_, err = h.nodeMutations.execute(c.Request.Context(), nodeMutationOperation{ApplyRuntime: group.NodeCount > 0, ChangedGroupID: &id, Mutate: func(ctx context.Context, tx *sql.Tx) error {
		var total int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM node_groups").Scan(&total); err != nil {
			return err
		}
		if total <= 1 {
			return groupConflict{"last_group", nil}
		}
		if group.NodeCount > 0 && mode == "" {
			return groupConflict{"group_not_empty", gin.H{"node_count": group.NodeCount}}
		}
		if mode == "move" {
			ids, err := readIDs(ctx, tx, "SELECT id FROM proxy_nodes WHERE group_id=? ORDER BY sort_order,id", id)
			if err != nil {
				return err
			}
			if err = h.moveNodes(ctx, tx, ids, target); err != nil {
				return err
			}
		}
		if mode == "delete_nodes" {
			if _, err = tx.ExecContext(ctx, "DELETE FROM proxy_nodes WHERE group_id=?", id); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM node_groups WHERE id=?", id)
		return err
	}})
	if err != nil {
		groupError(c, err)
		return
	}
	c.JSON(200, gin.H{"deleted": id})
}

func (h *Handler) ReorderGroups(c *gin.Context) {
	var req struct {
		IDs []int `json:"ids"`
	}
	if !bindRequestJSON(c, &req) {
		return
	}
	if err := validateNodeIDs(req.IDs); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()
	_, err := h.nodeMutations.execute(c.Request.Context(), nodeMutationOperation{Mutate: func(ctx context.Context, tx *sql.Tx) error {
		groups, err := models.LoadGroups(ctx, tx)
		if err != nil {
			return err
		}
		if len(groups) != len(req.IDs) {
			return invalidNodeRequest(errors.New("all group ids are required"))
		}
		for order, id := range req.IDs {
			result, err := tx.ExecContext(ctx, "UPDATE node_groups SET sort_order=?,revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE id=?", order, id)
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if count != 1 {
				return invalidNodeRequest(fmt.Errorf("unknown group %d", id))
			}
		}
		return nil
	}})
	if err != nil {
		groupError(c, err)
		return
	}
	c.JSON(200, gin.H{"ids": req.IDs})
}
