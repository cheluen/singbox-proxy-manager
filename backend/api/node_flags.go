package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"sb-proxy/backend/services"
)

func (h *Handler) UpdateNodeFlags(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var payload struct {
		Enabled         *bool `json:"enabled"`
		TCPReuseEnabled *bool `json:"tcp_reuse_enabled"`
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil ||
		(payload.Enabled == nil && payload.TCPReuseEnabled == nil) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "only enabled and tcp_reuse_enabled boolean fields are accepted"})
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()
	nodes, err := h.nodeMutations.Execute(c.Request.Context(), NodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			node, err := loadNodeByIDFrom(ctx, tx, id)
			if err != nil {
				return err
			}
			if payload.Enabled != nil {
				node.Enabled = *payload.Enabled
			}
			if payload.TCPReuseEnabled != nil {
				node.TCPReuseEnabled = *payload.TCPReuseEnabled
			}
			_, err = tx.ExecContext(ctx, `
				UPDATE proxy_nodes
				SET enabled = ?, tcp_reuse_enabled = ?, updated_at = CURRENT_TIMESTAMP
				WHERE id = ?
			`, node.Enabled, node.TCPReuseEnabled, id)
			return err
		},
	})
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		case services.IsUpstreamValidationError(err):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, singboxUpdateError(err))
		}
		return
	}
	for _, node := range nodes {
		if node.ID == id {
			c.JSON(http.StatusOK, node)
			return
		}
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "updated node missing from runtime snapshot"})
}
