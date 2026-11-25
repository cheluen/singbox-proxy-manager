package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
)

type nodeRequestError struct {
	status int
	cause  error
}

func (err nodeRequestError) Error() string {
	return err.cause.Error()
}

func (err nodeRequestError) Unwrap() error {
	return err.cause
}

var errNodeNotFound = nodeRequestError{status: http.StatusNotFound, cause: errors.New("node not found")}

func invalidNodeRequest(err error) nodeRequestError {
	return nodeRequestError{status: http.StatusBadRequest, cause: err}
}

func writeNodeMutationError(c *gin.Context, err error, validateUpstream bool) {
	var requestErr nodeRequestError
	switch {
	case errors.As(err, &requestErr):
		c.JSON(requestErr.status, gin.H{"error": services.SafeDiagnostic(requestErr.Error())})
	case validateUpstream && services.IsUpstreamValidationError(err):
		c.JSON(http.StatusBadRequest, gin.H{"error": services.SafeDiagnostic(err.Error())})
	default:
		c.JSON(http.StatusInternalServerError, singboxUpdateError(err))
	}
}

func writeNodeReadError(c *gin.Context, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(errNodeNotFound.status, gin.H{"error": errNodeNotFound.Error()})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
}

func parseNodeID(c *gin.Context, positiveOnly bool) (int, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || (positiveOnly && id <= 0) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return 0, false
	}
	return id, true
}

func bindRequestJSON(c *gin.Context, payload any) bool {
	if err := c.ShouldBindJSON(payload); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return false
	}
	return true
}

func parseNodeUpsert(c *gin.Context) (models.ProxyNode, nodeUpsertRequest, bool) {
	var payload nodeUpsertRequest
	if !bindRequestJSON(c, &payload) {
		return models.ProxyNode{}, payload, false
	}
	node := nodeFromUpsertRequest(payload)
	mode, err := models.NormalizeInboundMode(node.InboundMode)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return models.ProxyNode{}, payload, false
	}
	node.InboundMode = mode
	if err := validateNodeStorageFields(node); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return models.ProxyNode{}, payload, false
	}
	if _, err := node.ParseConfig(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid config format"})
		return models.ProxyNode{}, payload, false
	}
	if err := validateInboundCredentials(node.Username, node.Password, payload.AuthEnabled); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return models.ProxyNode{}, payload, false
	}
	return node, payload, true
}

func validateNodeInboundTLS(ctx context.Context, tx *sql.Tx, node models.ProxyNode, previousMode string) error {
	if node.InboundMode != models.InboundModeHTTPS || node.InboundPort <= 0 || (!node.Enabled && previousMode == models.InboundModeHTTPS) {
		return nil
	}
	return ensureInboundTLSReadyAndPersist(ctx, tx)
}

func ensureInboundTLSReadyAndPersist(ctx context.Context, tx *sql.Tx) error {
	var certPath, keyPath string
	if err := tx.QueryRowContext(ctx, "SELECT inbound_tls_cert_path,inbound_tls_key_path FROM settings WHERE singleton_key=1").Scan(&certPath, &keyPath); err != nil {
		return err
	}
	status := services.ResolveInboundTLS(certPath, keyPath)
	certificate, err := status.RequireCertificate()
	if err != nil {
		return invalidNodeRequest(err)
	}
	if status.SelectionMode == "auto" && certPath == "" && keyPath == "" {
		_, err := tx.ExecContext(ctx, "UPDATE settings SET inbound_tls_cert_path=?,inbound_tls_key_path=?,updated_at=CURRENT_TIMESTAMP WHERE singleton_key=1", certificate.CertPath, certificate.KeyPath)
		return err
	}
	return nil
}
