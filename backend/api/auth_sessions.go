package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	adminSessionTTLHoursEnvKey     = "ADMIN_SESSION_TTL_HOURS"
	defaultAdminSessionTTLHours    = 168
	maxAdminSessionTTLHoursAllowed = 24 * 365
)

func adminSessionDuration() time.Duration {
	hours := readEnvInt(adminSessionTTLHoursEnvKey, defaultAdminSessionTTLHours)
	if hours <= 0 || hours > maxAdminSessionTTLHoursAllowed {
		hours = defaultAdminSessionTTLHours
	}
	return time.Duration(hours) * time.Hour
}

func normalizeAuthToken(headerValue string) string {
	token := strings.TrimSpace(headerValue)
	if token == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		return strings.TrimSpace(token[7:])
	}
	return token
}

func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (h *Handler) isValidAdminSession(token string) (bool, error) {
	return h.isValidAdminSessionContext(context.Background(), token)
}

func (h *Handler) isValidAdminSessionContext(ctx context.Context, token string) (bool, error) {
	if token == "" {
		return false, nil
	}

	tokenHash := hashSessionToken(token)
	var expiresAt int64
	err := h.db.QueryRowContext(ctx, `
		SELECT session.expires_at
		FROM admin_sessions AS session
		JOIN settings AS singleton
		  ON singleton.singleton_key = 1
		 AND singleton.auth_generation = session.auth_generation
		WHERE session.token_hash = ?
		LIMIT 1
	`, tokenHash).Scan(&expiresAt)
	switch err {
	case nil:
		if time.Now().Unix() >= expiresAt {
			_, _ = h.db.Exec("DELETE FROM admin_sessions WHERE token_hash = ?", tokenHash)
			return false, nil
		}
		return true, nil
	case sql.ErrNoRows:
		return false, nil
	default:
		return false, err
	}
}

func (h *Handler) createAdminSession(c *gin.Context) (string, time.Time, error) {
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	var generation int64
	if err := h.db.QueryRowContext(ctx, "SELECT auth_generation FROM settings WHERE singleton_key = 1").Scan(&generation); err != nil {
		return "", time.Time{}, err
	}
	return createAdminSessionWithGeneration(ctx, c, h.db, generation)
}

type adminSessionExecer interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

func createAdminSessionWithGeneration(
	ctx context.Context,
	c *gin.Context,
	execer adminSessionExecer,
	generation int64,
) (string, time.Time, error) {
	now := time.Now()
	if _, err := execer.ExecContext(
		ctx,
		"DELETE FROM admin_sessions WHERE expires_at <= ? OR auth_generation <> ?",
		now.Unix(),
		generation,
	); err != nil {
		return "", time.Time{}, err
	}

	expiry := now.Add(adminSessionDuration())
	userAgent := ""
	ip := ""
	if c != nil && c.Request != nil {
		userAgent = c.Request.UserAgent()
		ip = c.ClientIP()
	}

	for i := 0; i < 3; i++ {
		tokenBytes := make([]byte, 32)
		if _, err := rand.Read(tokenBytes); err != nil {
			return "", time.Time{}, err
		}
		token := base64.URLEncoding.EncodeToString(tokenBytes)
		tokenHash := hashSessionToken(token)

		if _, err := execer.ExecContext(
			ctx,
			"INSERT INTO admin_sessions (token_hash, auth_generation, expires_at, user_agent, ip) VALUES (?, ?, ?, ?, ?)",
			tokenHash,
			generation,
			expiry.Unix(),
			userAgent,
			ip,
		); err != nil {
			continue
		}
		return token, expiry, nil
	}

	return "", time.Time{}, errors.New("failed to create session token")
}
