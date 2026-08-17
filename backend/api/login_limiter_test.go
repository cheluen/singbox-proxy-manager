package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

func newTestLoginLimiter(maxAttempts, maxConcurrent int) *loginRateLimiter {
	return &loginRateLimiter{
		attempts:    make(map[string]*loginAttempt),
		window:      time.Minute,
		maxAttempts: maxAttempts,
		block:       10 * time.Minute,
		checkSlots:  make(chan struct{}, maxConcurrent),
	}
}

func TestLoginRateLimiterReservesConcurrentAttemptsBeforePasswordCheck(t *testing.T) {
	limiter := newTestLoginLimiter(2, 4)
	now := time.Now()

	first, _, err := limiter.Begin(context.Background(), "192.0.2.10", now)
	if err != nil {
		t.Fatalf("reserve first attempt: %v", err)
	}
	second, _, err := limiter.Begin(context.Background(), "192.0.2.10", now)
	if err != nil {
		t.Fatalf("reserve second attempt: %v", err)
	}
	if _, _, err := limiter.Begin(context.Background(), "192.0.2.10", now); !errors.Is(err, ErrLoginRateLimited) {
		t.Fatalf("third concurrent attempt bypassed the per-client limit: %v", err)
	}

	first.Failure(now)
	second.Failure(now)
	if _, retryAfter, err := limiter.Begin(context.Background(), "192.0.2.10", now); !errors.Is(err, ErrLoginRateLimited) || retryAfter <= 0 {
		t.Fatalf("failed attempts did not activate the block: retry=%v err=%v", retryAfter, err)
	}
}

func TestLoginRateLimiterBoundsPasswordChecksAcrossClientAddresses(t *testing.T) {
	limiter := newTestLoginLimiter(10, 2)
	now := time.Now()

	first, _, err := limiter.Begin(context.Background(), "192.0.2.1", now)
	if err != nil {
		t.Fatalf("reserve first global slot: %v", err)
	}
	second, _, err := limiter.Begin(context.Background(), "192.0.2.2", now)
	if err != nil {
		t.Fatalf("reserve second global slot: %v", err)
	}
	if _, retryAfter, err := limiter.Begin(context.Background(), "192.0.2.3", now); !errors.Is(err, ErrLoginCheckCapacity) || retryAfter <= 0 {
		t.Fatalf("global password-check capacity was not enforced: retry=%v err=%v", retryAfter, err)
	}

	first.Cancel()
	third, _, err := limiter.Begin(context.Background(), "192.0.2.3", now)
	if err != nil {
		t.Fatalf("released global slot was not reusable: %v", err)
	}
	second.Cancel()
	third.Success()
}

func TestLoginRateLimiterDoesNotBypassEmptyClientKey(t *testing.T) {
	limiter := newTestLoginLimiter(1, 2)
	now := time.Now()

	permit, _, err := limiter.Begin(context.Background(), "", now)
	if err != nil {
		t.Fatalf("reserve unknown client attempt: %v", err)
	}
	if _, _, err := limiter.Begin(context.Background(), "  ", now); !errors.Is(err, ErrLoginRateLimited) {
		t.Fatalf("empty client key bypassed the per-client limit: %v", err)
	}
	permit.Cancel()
}

func TestLoginRateLimiterRejectsCancelledContextWithoutReservation(t *testing.T) {
	limiter := newTestLoginLimiter(2, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, err := limiter.Begin(ctx, "192.0.2.1", time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context was not rejected: %v", err)
	}
	if len(limiter.checkSlots) != 0 || len(limiter.attempts) != 0 {
		t.Fatalf("cancelled attempt leaked limiter state: slots=%d attempts=%d", len(limiter.checkSlots), len(limiter.attempts))
	}
}

func TestLoginHandlerSkipsCancelledRequestBeforePasswordComparison(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, nil)
	if _, err := handler.db.Exec(`
		UPDATE settings
		SET admin_password = 'test-hash', admin_password_set = 1
		WHERE singleton_key = 1
	`); err != nil {
		t.Fatalf("seed login password: %v", err)
	}
	var comparisons atomic.Int32
	handler.comparePassword = func([]byte, []byte) error {
		comparisons.Add(1)
		return nil
	}

	requestContext, cancel := context.WithCancel(context.Background())
	cancel()
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewBufferString(`{"password":"irrelevant"}`))
	request = request.WithContext(requestContext)
	request.RemoteAddr = "192.0.2.45:12345"
	request.Header.Set("Content-Type", "application/json")
	ginContext.Request = request

	handler.Login(ginContext)
	if comparisons.Load() != 0 {
		t.Fatalf("cancelled request reached password comparison")
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("cancelled request produced a response body: %s", recorder.Body.String())
	}
}

func TestLoginHandlerReservesAttemptsBeforePasswordComparison(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, nil)
	handler.db.SetMaxOpenConns(1)
	if _, err := handler.db.Exec(`
		UPDATE settings
		SET admin_password = 'test-hash', admin_password_set = 1
		WHERE singleton_key = 1
	`); err != nil {
		t.Fatalf("seed login password: %v", err)
	}
	handler.loginLimiter = newTestLoginLimiter(2, 4)

	const requestCount = 12
	started := make(chan struct{}, requestCount)
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var comparisons atomic.Int32
	handler.comparePassword = func([]byte, []byte) error {
		comparisons.Add(1)
		started <- struct{}{}
		<-release
		return bcrypt.ErrMismatchedHashAndPassword
	}

	type loginResult struct {
		status     int
		retryAfter string
	}
	results := make(chan loginResult, requestCount)
	start := make(chan struct{})
	for index := 0; index < requestCount; index++ {
		go func() {
			<-start
			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewBufferString(`{"password":"wrong"}`))
			request.RemoteAddr = "192.0.2.44:12345"
			request.Header.Set("Content-Type", "application/json")
			ginContext.Request = request
			handler.Login(ginContext)
			results <- loginResult{
				status:     recorder.Code,
				retryAfter: recorder.Header().Get("Retry-After"),
			}
		}()
	}
	close(start)

	for count := 0; count < 2; count++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("reserved login attempts did not reach password comparison")
		}
	}
	select {
	case <-started:
		t.Fatal("more password comparisons than the per-client limit ran concurrently")
	case <-time.After(100 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })

	unauthorized := 0
	rateLimited := 0
	for count := 0; count < requestCount; count++ {
		select {
		case result := <-results:
			switch result.status {
			case http.StatusUnauthorized:
				unauthorized++
			case http.StatusTooManyRequests:
				rateLimited++
				if result.retryAfter == "" {
					t.Fatal("rate-limited login response omitted Retry-After")
				}
			default:
				t.Fatalf("unexpected concurrent login status: %d", result.status)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent login request did not finish")
		}
	}
	if comparisons.Load() != 2 || unauthorized != 2 || rateLimited != requestCount-2 {
		t.Fatalf("login work was not bounded: comparisons=%d unauthorized=%d rate_limited=%d", comparisons.Load(), unauthorized, rateLimited)
	}
}
