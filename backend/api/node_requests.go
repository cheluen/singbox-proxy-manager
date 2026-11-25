package api

import (
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
