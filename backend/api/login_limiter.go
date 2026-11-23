package api

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	errLoginRateLimited   = errors.New("login rate limit exceeded")
	errLoginCheckCapacity = errors.New("login password-check capacity exhausted")
)

type loginAttempt struct {
	first        time.Time
	last         time.Time
	failures     int
	inFlight     int
	blockedUntil time.Time
}

type loginRateLimiter struct {
	mu          sync.Mutex
	attempts    map[string]*loginAttempt
	window      time.Duration
	maxAttempts int
	block       time.Duration
	checkSlots  chan struct{}
}

type loginAttemptPermit struct {
	limiter *loginRateLimiter
	key     string
	attempt *loginAttempt
	once    sync.Once
}

func newLoginRateLimiterFromEnv() *loginRateLimiter {
	// Defaults: 10 failures per 1 minute -> block 10 minutes.
	windowSeconds := readEnvInt("LOGIN_RATE_LIMIT_WINDOW_SECONDS", 60)
	maxAttempts := readEnvInt("LOGIN_RATE_LIMIT_MAX_ATTEMPTS", 10)
	blockSeconds := readEnvInt("LOGIN_RATE_LIMIT_BLOCK_SECONDS", 600)
	maxConcurrentChecks := readEnvInt("LOGIN_MAX_CONCURRENT_CHECKS", 2)

	if windowSeconds <= 0 {
		windowSeconds = 60
	}
	if maxAttempts <= 0 {
		maxAttempts = 10
	}
	if blockSeconds <= 0 {
		blockSeconds = 600
	}
	if maxConcurrentChecks <= 0 {
		maxConcurrentChecks = 2
	}

	return newLoginRateLimiter(time.Duration(windowSeconds)*time.Second, maxAttempts, time.Duration(blockSeconds)*time.Second, maxConcurrentChecks)
}

func newLoginRateLimiter(window time.Duration, maxAttempts int, block time.Duration, maxConcurrentChecks int) *loginRateLimiter {
	return &loginRateLimiter{
		attempts:    make(map[string]*loginAttempt),
		window:      window,
		maxAttempts: maxAttempts,
		block:       block,
		checkSlots:  make(chan struct{}, maxConcurrentChecks),
	}
}

func (l *loginRateLimiter) Begin(ctx context.Context, key string, now time.Time) (*loginAttemptPermit, time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		key = "unknown"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupLocked(now)

	a := l.attempts[key]
	if a == nil {
		a = &loginAttempt{}
	}
	if !a.blockedUntil.IsZero() && now.Before(a.blockedUntil) {
		retryAfter := a.blockedUntil.Sub(now)
		return nil, retryAfter, errLoginRateLimited
	}
	if !a.first.IsZero() && now.Sub(a.first) > l.window {
		a.first = time.Time{}
		a.failures = 0
		a.blockedUntil = time.Time{}
	}
	if a.failures+a.inFlight >= l.maxAttempts {
		return nil, time.Second, errLoginRateLimited
	}

	select {
	case l.checkSlots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-l.checkSlots
			return nil, 0, err
		}
		a.inFlight++
		a.last = now
		l.attempts[key] = a
		return &loginAttemptPermit{limiter: l, key: key, attempt: a}, 0, nil
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	default:
		return nil, time.Second, errLoginCheckCapacity
	}
}

func (p *loginAttemptPermit) Failure(now time.Time) {
	p.finish(now, false, true)
}

func (p *loginAttemptPermit) Success() {
	p.finish(time.Time{}, true, false)
}

func (p *loginAttemptPermit) Cancel() {
	p.finish(time.Time{}, false, false)
}

func (p *loginAttemptPermit) finish(now time.Time, success, failure bool) {
	p.once.Do(func() {
		l := p.limiter
		l.mu.Lock()
		defer l.mu.Unlock()
		<-l.checkSlots
		a, exists := l.attempts[p.key]
		if !exists || a != p.attempt {
			return
		}
		a.inFlight--
		if success {
			delete(l.attempts, p.key)
			return
		}
		if failure {
			if a.first.IsZero() || now.Sub(a.first) > l.window {
				a.first = now
				a.failures = 0
				a.blockedUntil = time.Time{}
			}
			a.failures++
			a.last = now
			if a.failures >= l.maxAttempts {
				a.blockedUntil = now.Add(l.block)
				a.failures = 0
				a.first = time.Time{}
			}
			return
		}
		if a.inFlight == 0 && a.failures == 0 && a.blockedUntil.IsZero() {
			delete(l.attempts, p.key)
		}
	})
}

func (l *loginRateLimiter) cleanupLocked(now time.Time) {
	ttl := l.window + l.block + (30 * time.Second)
	for k, a := range l.attempts {
		if a.inFlight == 0 && now.Sub(a.last) > ttl {
			delete(l.attempts, k)
		}
	}
}

func readEnvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
