package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const adminSessionDuration = 24 * time.Hour

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

func constantTimeEqual(expected string, actual string) bool {
	if len(expected) != len(actual) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (h *Handler) isValidAdminSession(token string) (bool, error) {
	if token == "" {
		return false, nil
	}

	tokenHash := hashSessionToken(token)
	var expiresAt int64
	err := h.db.QueryRow("SELECT expires_at FROM admin_sessions WHERE token_hash = ? LIMIT 1", tokenHash).Scan(&expiresAt)
	switch err {
	case nil:
		if time.Now().Unix() > expiresAt {
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
	expiry := time.Now().Add(adminSessionDuration)
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

		if _, err := h.db.Exec(
			"INSERT INTO admin_sessions (token_hash, expires_at, user_agent, ip) VALUES (?, ?, ?, ?)",
			tokenHash,
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
