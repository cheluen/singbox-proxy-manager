package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"

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
