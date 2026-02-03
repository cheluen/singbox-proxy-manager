package api

import (
	"os"
	"strconv"
	"sync"
	"time"
)

type loginAttempt struct {
	first        time.Time
	last         time.Time
	count        int
	blockedUntil time.Time
}

type loginRateLimiter struct {
	mu          sync.Mutex
	attempts    map[string]*loginAttempt
	window      time.Duration
	maxAttempts int
	block       time.Duration
}

func newLoginRateLimiterFromEnv() *loginRateLimiter {
	// Defaults: 10 failures per 1 minute -> block 10 minutes.
	windowSeconds := readEnvInt("LOGIN_RATE_LIMIT_WINDOW_SECONDS", 60)
	maxAttempts := readEnvInt("LOGIN_RATE_LIMIT_MAX_ATTEMPTS", 10)
	blockSeconds := readEnvInt("LOGIN_RATE_LIMIT_BLOCK_SECONDS", 600)

	if windowSeconds <= 0 {
		windowSeconds = 60
	}
	if maxAttempts <= 0 {
		maxAttempts = 10
	}
	if blockSeconds <= 0 {
		blockSeconds = 600
	}

	return &loginRateLimiter{
		attempts:    make(map[string]*loginAttempt),
		window:      time.Duration(windowSeconds) * time.Second,
		maxAttempts: maxAttempts,
		block:       time.Duration(blockSeconds) * time.Second,
	}
}

func (l *loginRateLimiter) Allow(key string, now time.Time) (bool, time.Duration) {
	if l == nil || key == "" {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	l.cleanupLocked(now)

	a := l.attempts[key]
	if a == nil {
		return true, 0
	}
	if !a.blockedUntil.IsZero() && now.Before(a.blockedUntil) {
		return false, a.blockedUntil.Sub(now)
	}
	if now.Sub(a.first) > l.window {
		a.first = time.Time{}
		a.count = 0
		a.blockedUntil = time.Time{}
	}
	return true, 0
}

func (l *loginRateLimiter) OnFailure(key string, now time.Time) {
	if l == nil || key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	l.cleanupLocked(now)

	a := l.attempts[key]
	if a == nil {
		a = &loginAttempt{first: now}
		l.attempts[key] = a
	}
	if a.first.IsZero() || now.Sub(a.first) > l.window {
		a.first = now
		a.count = 0
		a.blockedUntil = time.Time{}
	}

	a.count++
	a.last = now

	if a.count >= l.maxAttempts {
		a.blockedUntil = now.Add(l.block)
		a.count = 0
		a.first = time.Time{}
	}
}

func (l *loginRateLimiter) OnSuccess(key string) {
	if l == nil || key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
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
		if last.IsZero() || now.Sub(last) > ttl {
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
