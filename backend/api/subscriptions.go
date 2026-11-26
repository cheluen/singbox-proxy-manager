package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
)

type subscriptionResult struct {
	Added     int      `json:"added"`
	Updated   int      `json:"updated"`
	Removed   int      `json:"removed"`
	Unchanged int      `json:"unchanged"`
	Warnings  []string `json:"warnings"`
}

var (
	errSubscriptionChanged   = groupConflict{code: "subscription_group_changed"}
	errSubscriptionBusy      = groupConflict{code: "subscription_refresh_in_progress"}
	errSubscriptionLoad      = nodeRequestError{http.StatusBadGateway, errors.New("could not fetch or parse the subscription")}
	errSubscriptionPartial   = nodeRequestError{http.StatusUnprocessableEntity, errors.New("subscription contains invalid nodes")}
	errSubscriptionEmpty     = nodeRequestError{http.StatusUnprocessableEntity, errors.New("subscription contains no nodes")}
	errSubscriptionAmbiguous = nodeRequestError{http.StatusConflict, errors.New("subscription node identities are ambiguous")}
	errSubscriptionApply     = nodeRequestError{http.StatusInternalServerError, errors.New("could not apply subscription changes")}
	errSubscriptionStorage   = nodeRequestError{http.StatusInternalServerError, errors.New("could not read or save subscription status")}
)

type subscriptionLabel struct{ name, protocol, server string }

func subscriptionNodeLabel(node models.ProxyNode) subscriptionLabel {
	var endpoint struct {
		Server string `json:"server"`
		Peers  []struct {
			Server string `json:"server"`
		} `json:"peers"`
	}
	if json.Unmarshal([]byte(node.Config), &endpoint) != nil {
		return subscriptionLabel{}
	}
	server := strings.TrimSpace(endpoint.Server)
	if server == "" && node.Type == "wireguard" && len(endpoint.Peers) == 1 {
		server = strings.TrimSpace(endpoint.Peers[0].Server)
	}
	if server == "" || node.SubscriptionName == "" {
		return subscriptionLabel{}
	}
	if ip, err := netip.ParseAddr(strings.Trim(server, "[]")); err == nil {
		server = ip.String()
	} else {
		server = strings.ToLower(strings.TrimSuffix(server, "."))
	}
	return subscriptionLabel{node.SubscriptionName, node.Type, server}
}

func subscriptionNodes(items []services.ImportItem) ([]models.ProxyNode, []string, error) {
	nodes := make([]models.ProxyNode, 0, len(items))
	seen := make(map[[2]string]bool, len(items))
	for _, item := range items {
		data, err := json.Marshal(item.Config)
		if err != nil || item.Config == nil {
			return nil, nil, errSubscriptionPartial
		}
		node := models.ProxyNode{Name: strings.TrimSpace(item.Name), Type: strings.ToLower(strings.TrimSpace(item.Type)), Config: string(data)}
		if node.Name == "" || validateNodeStorageFields(node) != nil {
			return nil, nil, errSubscriptionPartial
		}
		config, err := node.ParseConfig()
		if err != nil {
			return nil, nil, errSubscriptionPartial
		}
		canonical, err := json.Marshal(config)
		if err != nil {
			return nil, nil, errSubscriptionPartial
		}
		node.Config = string(canonical)
		node.SubscriptionKey = fmt.Sprintf("%x", sha256.Sum256([]byte(node.Type+"\x00"+node.Config)))
		node.SubscriptionName = node.Name
		identity := [2]string{node.SubscriptionKey, node.SubscriptionName}
		if !seen[identity] {
			seen[identity] = true
			nodes = append(nodes, node)
		}
	}
	warnings := []string{}
	if duplicates := len(items) - len(nodes); duplicates != 0 {
		warnings = append(warnings, fmt.Sprintf("ignored %d identical duplicate subscription entries", duplicates))
	}
	return nodes, warnings, nil
}

type subscriptionIndex struct {
	namedKeys map[[2]string]int
	labels    map[subscriptionLabel]int
}

func indexSubscriptionKey[Key comparable](index map[Key]int, key Key, position int) {
	if _, ok := index[key]; ok {
		position = -1
	}
	index[key] = position
}

func indexSubscriptionNodes(nodes []models.ProxyNode) (subscriptionIndex, []subscriptionLabel) {
	index := subscriptionIndex{
		namedKeys: make(map[[2]string]int, len(nodes)),
		labels:    make(map[subscriptionLabel]int, len(nodes)),
	}
	labels := make([]subscriptionLabel, len(nodes))
	for position, node := range nodes {
		indexSubscriptionKey(index.namedKeys, [2]string{node.SubscriptionKey, node.SubscriptionName}, position)
		labels[position] = subscriptionNodeLabel(node)
		if labels[position] != (subscriptionLabel{}) {
			indexSubscriptionKey(index.labels, labels[position], position)
		}
	}
	return index, labels
}

type subscriptionPlan struct {
	add, update []models.ProxyNode
	remove      []int
	unchanged   int
}

func planSubscription(current, incoming []models.ProxyNode, groupID int) (subscriptionPlan, error) {
	var managed []models.ProxyNode
	for _, node := range current {
		if node.GroupID == groupID && node.SubscriptionKey != "" {
			if node.Type == "wireguard" && node.Config != "" && fmt.Sprintf("%x", sha256.Sum256([]byte(node.Type+"\x00"+node.Config))) == node.SubscriptionKey {
				if config, err := node.ParseConfig(); err == nil {
					if canonical, err := json.Marshal(config); err == nil {
						if rekey := fmt.Sprintf("%x", sha256.Sum256([]byte(node.Type+"\x00"+string(canonical)))); rekey != node.SubscriptionKey {
							node.SubscriptionKey = rekey
						}
					}
				}
			}
			managed = append(managed, node)
		}
	}
	previous, previousLabels := indexSubscriptionNodes(managed)
	next, nextLabels := indexSubscriptionNodes(incoming)
	matched := make([]int, len(incoming))
	used := make([]bool, len(managed))
	remainingNew := make(map[string]int)
	for position, node := range incoming {
		namedKey := [2]string{node.SubscriptionKey, node.SubscriptionName}
		if old, ok := previous.namedKeys[namedKey]; ok && old >= 0 && next.namedKeys[namedKey] >= 0 {
			matched[position], used[old] = old, true
		} else {
			matched[position] = -1
			indexSubscriptionKey(remainingNew, node.SubscriptionKey, position)
		}
	}
	remainingOld := make(map[string]int)
	for position, node := range managed {
		if !used[position] {
			indexSubscriptionKey(remainingOld, node.SubscriptionKey, position)
		}
	}
	for key, position := range remainingNew {
		old, shared := remainingOld[key]
		if !shared {
			continue
		}
		if old < 0 || position < 0 {
			return subscriptionPlan{}, errSubscriptionAmbiguous
		}
		matched[position], used[old] = old, true
	}
	for position, label := range nextLabels {
		if old, ok := previous.labels[label]; matched[position] < 0 && ok && old >= 0 && next.labels[label] >= 0 && !used[old] {
			matched[position], used[old] = old, true
		}
	}
	oldNames, newNames := map[[2]string]int{}, map[[2]string]int{}
	unmatchedLabels := make(map[subscriptionLabel]bool)
	for pos, node := range managed {
		if used[pos] {
			continue
		}
		if label := previousLabels[pos]; label != (subscriptionLabel{}) {
			unmatchedLabels[label] = true
		}
		if node.SubscriptionName != "" {
			indexSubscriptionKey(oldNames, [2]string{node.SubscriptionName, node.Type}, pos)
		}
	}
	for pos, node := range incoming {
		if matched[pos] >= 0 {
			continue
		}
		if unmatchedLabels[nextLabels[pos]] {
			return subscriptionPlan{}, errSubscriptionAmbiguous
		}
		if node.SubscriptionName != "" {
			indexSubscriptionKey(newNames, [2]string{node.SubscriptionName, node.Type}, pos)
		}
	}
	for name, pos := range newNames {
		if old, ok := oldNames[name]; ok {
			if pos < 0 || old < 0 {
				return subscriptionPlan{}, errSubscriptionAmbiguous
			}
			matched[pos], used[old] = old, true
		}
	}
	var plan subscriptionPlan
	for position, node := range managed {
		if !used[position] {
			plan.remove = append(plan.remove, node.ID)
		}
	}
	for position, node := range incoming {
		if matched[position] < 0 {
			node.GroupID, node.Enabled, node.TCPReuseEnabled = groupID, true, true
			node.UpstreamMode = importedUpstreamMode([]byte(node.Config))
			plan.add = append(plan.add, node)
			continue
		}
		old := managed[matched[position]]
		node.ID, node.UpstreamMode = old.ID, old.UpstreamMode
		if old.Name != old.SubscriptionName {
			node.Name = old.Name
		}
		if old.UpstreamMode == importedUpstreamMode([]byte(old.Config)) {
			node.UpstreamMode = importedUpstreamMode([]byte(node.Config))
		}
		if old.Type == node.Type && old.Config == node.Config && old.Name == node.Name && old.SubscriptionName == node.SubscriptionName && old.SubscriptionKey == node.SubscriptionKey && old.UpstreamMode == node.UpstreamMode {
			plan.unchanged++
		} else {
			plan.update = append(plan.update, node)
		}
	}
	return plan, nil
}

func (h *Handler) applySubscription(ctx context.Context, tx *sql.Tx, group models.NodeGroup, plan subscriptionPlan) error {
	for chunk := range slices.Chunk(plan.remove, nodeIDsPerQuery) {
		condition, args := nodeIDCondition(chunk)
		if _, err := tx.ExecContext(ctx, "DELETE FROM proxy_nodes"+condition+" AND group_id=? AND subscription_key<>''", append(args, group.ID)...); err != nil {
			return err
		}
	}
	for _, node := range plan.update {
		if _, err := tx.ExecContext(ctx, `UPDATE proxy_nodes SET name=?,type=?,config=?,upstream_mode=?,subscription_key=?,subscription_name=?,updated_at=CURRENT_TIMESTAMP
			WHERE id=? AND group_id=? AND subscription_key<>''`, node.Name, node.Type, node.Config, node.UpstreamMode, node.SubscriptionKey, node.SubscriptionName, node.ID, group.ID); err != nil {
			return err
		}
	}
	if len(plan.add) == 0 {
		return nil
	}
	var nextOrder int
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sort_order),-1)+1 FROM proxy_nodes WHERE group_id=?", group.ID).Scan(&nextOrder); err != nil {
		return err
	}
	var used map[int]struct{}
	var err error
	if group.AllocatePort {
		used, err = collectUsedInboundPortsTx(ctx, tx, 0)
		if err != nil {
			return err
		}
	}
	stmt, err := tx.PrepareContext(ctx, insertNodeSQL)
	if err != nil {
		return err
	}
	defer stmt.Close()
	nextPort := group.StartPort
	for offset, node := range plan.add {
		if group.AllocatePort {
			node.InboundPort, err = h.nextBindablePort(ctx, nextPort, used)
			if err != nil {
				return err
			}
			used[node.InboundPort] = struct{}{}
			nextPort = node.InboundPort + 1
		}
		node.Username, err = generateRandomString(12)
		if err != nil {
			return err
		}
		node.Password, err = generateRandomString(24)
		if err != nil {
			return err
		}
		node.SortOrder = nextOrder + offset
		if _, err := stmt.ExecContext(ctx, nodeInsertArgs(node)...); err != nil {
			return err
		}
	}
	return nil
}

func subscriptionStatusCAS(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, group models.NodeGroup, message string) error {
	assignments := "subscription_last_error=?,updated_at=CURRENT_TIMESTAMP,revision=revision+1"
	if message == "" {
		assignments += ",subscription_last_updated_at=CURRENT_TIMESTAMP"
	}
	result, err := db.ExecContext(ctx, "UPDATE node_groups SET "+assignments+" WHERE id=? AND revision=?", message, group.ID, group.Revision)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return errSubscriptionChanged
	}
	return err
}

func (h *Handler) fetchSubscriptionNodes(ctx context.Context, url string) ([]models.ProxyNode, []string, error) {
	items, failures, err := h.fetchSubscription(ctx, []string{url}, services.BatchImportSourceSubscription)
	switch {
	case ctx.Err() != nil:
		return nil, nil, ctx.Err()
	case err != nil:
		return nil, nil, errSubscriptionLoad
	case len(failures) > 0 && len(items) == 0:
		if diagnostic := failures[0].SubscriptionFetchDiagnostic(); diagnostic != "" {
			return nil, nil, nodeRequestError{http.StatusBadGateway, errors.New(diagnostic)}
		}
		return nil, nil, errSubscriptionLoad
	case len(failures) > 0:
		return nil, nil, errSubscriptionPartial
	case len(items) == 0:
		return nil, nil, errSubscriptionEmpty
	}
	return subscriptionNodes(items)
}

func (h *Handler) commitSubscription(ctx context.Context, group models.NodeGroup, incoming []models.ProxyNode, warnings []string) (subscriptionResult, error) {
	ctx, cancel := context.WithTimeout(ctx, batchImportValidationTimeout())
	defer cancel()
	current, err := models.LoadGroupNodes(ctx, h.db, group.ID)
	if err != nil {
		return subscriptionResult{}, errSubscriptionStorage
	}
	plan, err := planSubscription(current, incoming, group.ID)
	if err != nil {
		return subscriptionResult{}, err
	}
	_, err = h.nodeMutations.execute(ctx, nodeMutationOperation{
		ApplyRuntime: len(plan.add)+len(plan.update)+len(plan.remove) > 0,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			if err := subscriptionStatusCAS(ctx, tx, group, ""); err != nil {
				return err
			}
			return h.applySubscription(ctx, tx, group, plan)
		},
	})
	if err != nil {
		return subscriptionResult{}, err
	}
	return subscriptionResult{Added: len(plan.add), Updated: len(plan.update), Removed: len(plan.remove), Unchanged: plan.unchanged, Warnings: warnings}, nil
}

func (h *Handler) refreshGroupSubscription(ctx context.Context, id int, automatic bool) (subscriptionResult, error) {
	result := subscriptionResult{Warnings: []string{}}
	h.refreshMu.Lock()
	if h.refreshing[id] {
		h.refreshMu.Unlock()
		return result, errSubscriptionBusy
	}
	h.refreshing[id] = true
	h.refreshMu.Unlock()
	defer func() {
		h.refreshMu.Lock()
		delete(h.refreshing, id)
		h.refreshMu.Unlock()
	}()
	group, err := requestGroup(ctx, h.db, &id)
	if err != nil {
		var requestErr nodeRequestError
		if errors.As(err, &requestErr) {
			return result, requestErr
		}
		return result, errSubscriptionStorage
	}
	if automatic && (!subscriptionDue(group, time.Now()) || group.SubscriptionURL == "") {
		return result, nil
	}
	if group.SubscriptionURL == "" {
		return result, invalidNodeRequest(errors.New("group has no subscription URL"))
	}
	incoming, warnings, err := h.fetchSubscriptionNodes(ctx, group.SubscriptionURL)
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err == nil {
		result, err = h.commitSubscription(ctx, group, incoming, warnings)
		if err == nil {
			return result, nil
		}
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	var conflict groupConflict
	if errors.As(err, &conflict) {
		return result, conflict
	}
	var safe nodeRequestError
	if !errors.As(err, &safe) {
		safe = errSubscriptionApply
	}
	if statusErr := subscriptionStatusCAS(ctx, h.db, group, safe.Error()); statusErr != nil {
		if errors.As(statusErr, &conflict) {
			return result, conflict
		}
		return result, errSubscriptionStorage
	}
	return result, safe
}

func (h *Handler) RefreshSubscription(c *gin.Context) {
	id, ok := parseNodeID(c, true)
	if !ok {
		return
	}
	result, err := h.refreshGroupSubscription(c.Request.Context(), id, false)
	if err != nil {
		groupError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func subscriptionDue(group models.NodeGroup, now time.Time) bool {
	if !group.SubscriptionAutoUpdate || group.SubscriptionIntervalMinutes < 1 {
		return false
	}
	last := group.SubscriptionLastUpdatedAt
	if group.SubscriptionLastError != "" {
		last = &group.UpdatedAt
	}
	return last == nil || !now.Before(last.Add(time.Duration(group.SubscriptionIntervalMinutes)*time.Minute))
}

func (h *Handler) dueSubscriptionIDs(ctx context.Context, now time.Time) ([]int, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT id,subscription_interval_minutes,subscription_last_updated_at,subscription_last_error,updated_at
		FROM node_groups WHERE subscription_auto_update AND subscription_url<>'' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		group := models.NodeGroup{SubscriptionAutoUpdate: true}
		if err := rows.Scan(&group.ID, &group.SubscriptionIntervalMinutes, &group.SubscriptionLastUpdatedAt, &group.SubscriptionLastError, &group.UpdatedAt); err != nil {
			return nil, err
		}
		if subscriptionDue(group, now) {
			ids = append(ids, group.ID)
		}
	}
	return ids, rows.Err()
}

func (h *Handler) StartSubscriptionScheduler(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for ctx.Err() == nil {
			ids, err := h.dueSubscriptionIDs(ctx, time.Now())
			if err != nil && ctx.Err() == nil {
				log.Print("Could not read the subscription schedule")
			}
			for _, id := range ids {
				if ctx.Err() != nil {
					return
				}
				if _, err := h.refreshGroupSubscription(ctx, id, true); err != nil && ctx.Err() == nil {
					log.Printf("Subscription refresh for group %d: %v", id, err)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return done
}
