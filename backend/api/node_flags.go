package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"

	"github.com/gin-gonic/gin"

	"sb-proxy/backend/models"
)

func (h *Handler) UpdateNodeFlags(c *gin.Context) {
	id, ok := parseNodeID(c, true)
	if !ok {
		return
	}
	var payload struct {
		Enabled         *bool   `json:"enabled"`
		TCPReuseEnabled *bool   `json:"tcp_reuse_enabled"`
		InboundMode     *string `json:"inbound_mode"`
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil ||
		(payload.Enabled == nil && payload.TCPReuseEnabled == nil && payload.InboundMode == nil) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "only enabled, tcp_reuse_enabled and inbound_mode fields are accepted"})
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()
	nodes, err := h.nodeMutations.execute(c.Request.Context(), nodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			node, err := loadNodeForMutation(ctx, tx, id)
			if err != nil {
				return err
			}
			previousMode := node.InboundMode
			if payload.Enabled != nil {
				node.Enabled = *payload.Enabled
			}
			if payload.TCPReuseEnabled != nil {
				node.TCPReuseEnabled = *payload.TCPReuseEnabled
			}
			if payload.InboundMode != nil {
				mode, err := models.NormalizeInboundMode(*payload.InboundMode)
				if err != nil {
					return invalidNodeRequest(err)
				}
				node.InboundMode = mode
			}
			if err := validateNodeInboundTLS(ctx, tx, node, previousMode); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `
				UPDATE proxy_nodes
				SET enabled = ?, tcp_reuse_enabled = ?, inbound_mode = ?, updated_at = CURRENT_TIMESTAMP
				WHERE id = ?
			`, node.Enabled, node.TCPReuseEnabled, node.InboundMode, id)
			return err
		},
	})
	if err != nil {
		writeNodeMutationError(c, err, true)
		return
	}
	for _, node := range nodes {
		if node.ID == id {
			c.JSON(http.StatusOK, h.singBoxService.WithPolicyStatus(node))
			return
		}
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "updated node missing from runtime snapshot"})
}

func (h *Handler) BatchSetNodesEnabled(c *gin.Context) {
	var payload struct {
		IDs     []int `json:"ids"`
		Enabled *bool `json:"enabled"`
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if payload.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "enabled is required"})
		return
	}
	if len(payload.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no nodes selected"})
		return
	}
	if err := validateNodeIDs(payload.IDs); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()
	enabled := *payload.Enabled
	changedIDs, err := loadChangedNodeEnabledIDs(c.Request.Context(), h.db, payload.IDs, enabled)
	if err != nil {
		writeNodeMutationError(c, err, true)
		return
	}
	updatedCount := 0
	if len(changedIDs) > 0 {
		_, err = h.nodeMutations.execute(c.Request.Context(), nodeMutationOperation{
			ApplyRuntime: true,
			Mutate: func(ctx context.Context, tx *sql.Tx) error {
				changedIDs, err := loadChangedNodeEnabledIDs(ctx, tx, payload.IDs, enabled)
				if err != nil {
					return err
				}
				updatedCount = len(changedIDs)
				for chunk := range slices.Chunk(changedIDs, nodeIDsPerQuery) {
					condition, args := nodeIDCondition(chunk)
					if _, err := tx.ExecContext(ctx,
						"UPDATE proxy_nodes SET enabled = ?, updated_at = CURRENT_TIMESTAMP"+condition,
						append([]any{enabled}, args...)...); err != nil {
						return err
					}
				}
				return nil
			},
		})
		if err != nil {
			writeNodeMutationError(c, err, true)
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"message":         "nodes updated",
		"enabled":         enabled,
		"updated_count":   updatedCount,
		"unchanged_count": len(payload.IDs) - updatedCount,
	})
}

func loadChangedNodeEnabledIDs(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, ids []int, enabled bool) ([]int, error) {
	changedIDs := make([]int, 0, len(ids))
	for chunk := range slices.Chunk(ids, nodeIDsPerQuery) {
		condition, args := nodeIDCondition(chunk)
		rows, err := db.QueryContext(ctx, "SELECT id, enabled FROM proxy_nodes"+condition, args...)
		if err != nil {
			return nil, err
		}
		states := make(map[int]bool, len(chunk))
		for rows.Next() {
			var id int
			var currentEnabled bool
			if err := rows.Scan(&id, &currentEnabled); err != nil {
				rows.Close()
				return nil, err
			}
			states[id] = currentEnabled
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		for _, id := range chunk {
			currentEnabled, exists := states[id]
			if !exists {
				return nil, nodeRequestError{status: http.StatusNotFound, cause: fmt.Errorf("node %d not found", id)}
			}
			if currentEnabled != enabled {
				changedIDs = append(changedIDs, id)
			}
		}
	}
	return changedIDs, nil
}
