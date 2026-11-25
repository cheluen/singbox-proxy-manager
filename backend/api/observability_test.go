package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"sb-proxy/backend/services"
)

func TestObservabilityAuthenticatedSnapshotAndStream(t *testing.T) {
	handler := newTestHandler(t, nil)
	observer := services.NewObservability(handler.db, handler.singBoxService)
	t.Cleanup(func() {
		if err := observer.Close(); err != nil {
			t.Error(err)
		}
	})
	router := gin.New()
	protected := router.Group("/protected")
	protected.Use(handler.AuthMiddleware())
	handler.RegisterObservabilityRoutes(protected, observer)
	protected.POST("/logout", handler.Logout)
	server := httptest.NewServer(router)
	defer server.Close()
	for _, resource := range []string{"connections", "connections/stream", "logs", "logs/settings", "logs/traffic"} {
		res, err := server.Client().Get(server.URL + "/protected/" + resource)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s: %d", resource, res.StatusCode)
		}
	}
	send := func(ctx context.Context, method, path, token string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, server.URL+"/protected/"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	token, _, err := createTestAdminSession(t, handler)
	if err != nil {
		t.Fatal(err)
	}
	res := send(t.Context(), "GET", "connections", token)
	var snap services.ConnectionSnapshot
	err = json.NewDecoder(res.Body).Decode(&snap)
	res.Body.Close()
	if err != nil || snap.Routes == nil || snap.Up != nil {
		t.Fatalf("empty snapshot %+v %v", snap, err)
	}
	for _, action := range []string{"logout", "expire", "generation"} {
		t.Run(action, func(t *testing.T) {
			token, _, err := createTestAdminSession(t, handler)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			stream := send(ctx, "GET", "connections/stream", token)
			defer stream.Body.Close()
			reader := bufio.NewReader(stream.Body)
			event, err := reader.ReadString('\n')
			if err != nil || event != "event: error\n" {
				t.Fatalf("SSE event %q %v", event, err)
			}
			data, err := reader.ReadString('\n')
			var failure struct{ Error string }
			if err != nil || json.Unmarshal([]byte(strings.TrimPrefix(data, "data: ")), &failure) != nil || failure.Error == "" {
				t.Fatalf("SSE error frame: %q, %v", data, err)
			}
			if _, err = reader.ReadString('\n'); err != nil {
				t.Fatal(err)
			}
			switch action {
			case "logout":
				loggedOut := send(ctx, "POST", "logout", token)
				loggedOut.Body.Close()
				if loggedOut.StatusCode != http.StatusOK {
					t.Fatal(loggedOut.Status)
				}
			case "expire":
				_, err = handler.db.Exec("UPDATE admin_sessions SET expires_at=0 WHERE token_hash=?", hashSessionToken(token))
			case "generation":
				_, err = handler.db.Exec("UPDATE settings SET auth_generation=auth_generation+1 WHERE singleton_key=1")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reader.ReadByte(); err != io.EOF {
				t.Fatalf("revoked stream still readable: %v", err)
			}
		})
	}
}
