package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	appdb "sb-proxy/backend/database"
)

const (
	adminSessionTTLHoursEnvKey     = "ADMIN_SESSION_TTL_HOURS"
	defaultAdminSessionTTLHours    = 168
	maxAdminSessionTTLHoursAllowed = 24 * 365
	adminSessionHashKey            = "adminSessionHash"
)

var errAuthGenerationChanged = errors.New("authentication generation changed during login")

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

func (h *Handler) verifiedAdminSessionHash(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", nil
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
		if now := time.Now().Unix(); now >= expiresAt {
			_, _ = h.db.ExecContext(ctx, "DELETE FROM admin_sessions WHERE token_hash = ? AND expires_at <= ?", tokenHash, now)
			return "", nil
		}
		return tokenHash, nil
	case sql.ErrNoRows:
		return "", nil
	default:
		return "", err
	}
}

func createAdminSessionWithGeneration(
	ctx context.Context,
	db *sql.DB,
	generation int64,
	userAgent, ip string,
) (string, time.Time, error) {
	dialect := appdb.DialectFor(db)
	query := `
		INSERT INTO admin_sessions (token_hash, auth_generation, expires_at, user_agent, ip)
		SELECT ?, auth_generation, ?, ?, ?
		FROM settings WHERE singleton_key = 1 AND auth_generation = ?`
	if dialect == appdb.DialectPostgres || dialect == appdb.DialectMySQL {
		query += " FOR UPDATE"
	}

	now := time.Now()
	expiry := now.Add(adminSessionDuration())
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	defer tx.Rollback()

	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return "", time.Time{}, err
	}
	token := base64.URLEncoding.EncodeToString(tokenBytes[:])
	result, err := tx.ExecContext(ctx, query, hashSessionToken(token), expiry.Unix(), userAgent, ip, generation)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("insert admin session: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return "", time.Time{}, err
	}
	if inserted == 0 {
		return "", time.Time{}, errAuthGenerationChanged
	}

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM admin_sessions
		WHERE expires_at <= ?
		   OR auth_generation <> (SELECT auth_generation FROM settings WHERE singleton_key = 1)
	`, now.Unix()); err != nil {
		return "", time.Time{}, fmt.Errorf("clean admin sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", time.Time{}, err
	}
	return token, expiry, nil
}
