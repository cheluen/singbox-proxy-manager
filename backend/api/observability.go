package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"sb-proxy/backend/services"
)

func (h *Handler) RegisterObservabilityRoutes(routes *gin.RouterGroup, observer *services.Observability) {
	routes.GET("/connections", func(c *gin.Context) {
		snapshot, err := observer.Connections.Capture(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, snapshot)
	})
	routes.GET("/connections/stream", func(c *gin.Context) {
		ctx := c.Request.Context()
		hash := c.GetString(adminSessionHashKey)
		if hash == "" {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		valid := func() bool {
			checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			verified, err := h.checkAdminSessionHash(checkCtx, hash)
			return err == nil && verified != ""
		}
		updates, release := observer.Connections.Subscribe()
		defer release()
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-store")
		c.Header("X-Accel-Buffering", "no")
		control := http.NewResponseController(c.Writer)
		if err := control.Flush(); err != nil {
			return
		}
		recheck := time.NewTimer(1200 * time.Millisecond)
		defer recheck.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-recheck.C:
				if !valid() {
					return
				}
			case snapshot := <-updates:
				if !valid() {
					return
				}
				var frame bytes.Buffer
				var payload any = snapshot
				if snapshot.StreamError != "" {
					frame.WriteString("event: error\ndata: ")
					payload = gin.H{"error": snapshot.StreamError}
				} else {
					frame.WriteString("event: snapshot\ndata: ")
				}
				if err := json.NewEncoder(&frame).Encode(payload); err != nil {
					return
				}
				frame.WriteByte('\n')
				_ = control.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if _, err := c.Writer.Write(frame.Bytes()); err != nil {
					return
				}
				if err := control.Flush(); err != nil {
					return
				}
				_ = control.SetWriteDeadline(time.Time{})
			}
			recheck.Reset(1200 * time.Millisecond)
		}
	})
	routes.GET("/logs/settings", func(c *gin.Context) { c.JSON(http.StatusOK, observer.Settings()) })
	routes.PUT("/logs/settings", func(c *gin.Context) {
		var input services.LogSettingsInput
		if !bindRequestJSON(c, &input) {
			return
		}
		if err := observer.Save(c.Request.Context(), input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, observer.Settings())
	})
	for _, path := range []string{"/logs", "/logs/traffic"} {
		routes.GET(path, func(c *gin.Context) {
			filter, err := logFilter(c)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			var data any
			if path == "/logs" {
				data, err = observer.Logs(c.Request.Context(), filter)
			} else {
				data, err = observer.Traffic(c.Request.Context(), filter)
			}
			if err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, data)
		})
	}
}

func logFilter(c *gin.Context) (services.LogFilter, error) {
	f := services.LogFilter{Source: c.Query("source"), Level: strings.ToUpper(c.Query("level")), IP: c.Query("client_ip"), Query: c.Query("q")}
	for name, ptr := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if value := c.Query(name); value != "" {
			stamp, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return f, fmt.Errorf("%s requires an RFC3339 timestamp", name)
			}
			*ptr = stamp
		}
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.From.After(f.To) {
		return f, errors.New("invalid time range")
	}
	for name, ptr := range map[string]*int{"inbound_port": &f.Port, "outbound_node_id": &f.Outbound, "limit": &f.Limit} {
		if value := c.Query(name); value != "" {
			number, err := strconv.Atoi(value)
			if err != nil || number <= 0 {
				return f, fmt.Errorf("invalid %s", name)
			}
			*ptr = number
		}
	}
	if raw := c.Query("cursor"); raw != "" {
		cursor, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || cursor < 0 {
			return f, errors.New("invalid log cursor")
		}
		f.Cursor = cursor
	}
	return f, nil
}
