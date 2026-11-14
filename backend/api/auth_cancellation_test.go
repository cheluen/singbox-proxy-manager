package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestAuthMiddlewareCanceledRequestDoesNotReportDatabaseFailure(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "canceled"
		if expired {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			handler := newTestHandler(t, nil)
			requestContext, cancel := context.WithCancel(context.Background())
			if expired {
				cancel()
				requestContext, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			} else {
				cancel()
			}
			defer cancel()
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/runtime/status", nil).WithContext(requestContext)
			ctx.Request.Header.Set("Authorization", "canceled-request-token")
			handler.AuthMiddleware()(ctx)
			if !ctx.IsAborted() {
				t.Fatal("已取消的鉴权请求仍继续执行受保护操作")
			}
			if recorder.Body.Len() != 0 || recorder.Code >= http.StatusInternalServerError {
				t.Fatalf("请求取消被误报为数据库错误：status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestAuthMiddlewareStillReportsRealDatabaseFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, nil)
	if err := handler.db.Close(); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/runtime/status", nil)
	ctx.Request.Header.Set("Authorization", "database-failure-token")
	handler.AuthMiddleware()(ctx)
	if !ctx.IsAborted() || recorder.Code != http.StatusInternalServerError {
		t.Fatalf("真实数据库故障被吞掉：status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAuthMiddlewareCancellationWhileWaitingForConnection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, nil)
	handler.db.SetMaxOpenConns(1)
	connection, err := handler.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/runtime/status", nil).WithContext(requestContext)
	ctx.Request.Header.Set("Authorization", "queued-request-token")
	done := make(chan struct{})
	go func() {
		handler.AuthMiddleware()(ctx)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for handler.db.Stats().WaitCount == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if handler.db.Stats().WaitCount == 0 {
		t.Fatal("鉴权查询没有进入连接池等待")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消请求没有释放排队鉴权查询")
	}
	if !ctx.IsAborted() || recorder.Body.Len() != 0 || recorder.Code >= http.StatusInternalServerError {
		t.Fatalf("排队请求取消后仍误报数据库故障：status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
