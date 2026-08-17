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
	ErrLoginRateLimited   = errors.New("login rate limit exceeded")
	ErrLoginCheckCapacity = errors.New("login password-check capacity exhausted")
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
	hasSlot bool
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

	return &loginRateLimiter{
		attempts:    make(map[string]*loginAttempt),
		window:      time.Duration(windowSeconds) * time.Second,
		maxAttempts: maxAttempts,
		block:       time.Duration(blockSeconds) * time.Second,
		checkSlots:  make(chan struct{}, maxConcurrentChecks),
	}
}

func (l *loginRateLimiter) Begin(ctx context.Context, key string, now time.Time) (*loginAttemptPermit, time.Duration, error) {
	if l == nil {
		return &loginAttemptPermit{}, 0, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		key = "unknown"
	}
	l.mu.Lock()
	l.cleanupLocked(now)

	a := l.attempts[key]
	if a == nil {
		a = &loginAttempt{}
		l.attempts[key] = a
	}
	if !a.blockedUntil.IsZero() && now.Before(a.blockedUntil) {
		retryAfter := a.blockedUntil.Sub(now)
		l.mu.Unlock()
		return nil, retryAfter, ErrLoginRateLimited
	}
	if !a.first.IsZero() && now.Sub(a.first) > l.window {
		a.first = time.Time{}
		a.failures = 0
		a.blockedUntil = time.Time{}
	}
	if a.failures+a.inFlight >= l.maxAttempts {
		l.mu.Unlock()
		return nil, time.Second, ErrLoginRateLimited
	}
	a.inFlight++
	a.last = now
	l.mu.Unlock()

	permit := &loginAttemptPermit{limiter: l, key: key, attempt: a}
	if l.checkSlots == nil {
		return permit, 0, nil
	}
	select {
	case l.checkSlots <- struct{}{}:
		permit.hasSlot = true
		if err := ctx.Err(); err != nil {
			permit.Cancel()
			return nil, 0, err
		}
		return permit, 0, nil
	case <-ctx.Done():
		permit.Cancel()
		return nil, 0, ctx.Err()
	default:
		permit.Cancel()
		return nil, time.Second, ErrLoginCheckCapacity
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
	if p == nil {
		return
	}
	p.once.Do(func() {
		l := p.limiter
		if l == nil {
			return
		}
		if p.hasSlot && l.checkSlots != nil {
			<-l.checkSlots
		}

		l.mu.Lock()
		defer l.mu.Unlock()
		a, exists := l.attempts[p.key]
		if !exists || a != p.attempt {
			return
		}
		if a.inFlight > 0 {
			a.inFlight--
		}
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
		if a == nil {
			delete(l.attempts, k)
			continue
		}
		last := a.last
		if last.IsZero() {
			last = a.first
		}
		if a.inFlight == 0 && (last.IsZero() || now.Sub(last) > ttl) {
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
