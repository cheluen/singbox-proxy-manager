package api

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
)

func generateRandomString(length int) (string, error) {
	if length <= 0 {
		return "", nil
	}

	// RawURLEncoding expands by 4/3, round up enough bytes.
	byteLen := (length*3)/4 + 2
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := base64.RawURLEncoding.EncodeToString(b)
	if len(s) < length {
		// Extremely unlikely, but keep correctness.
		return s, nil
	}
	return s[:length], nil
}

func generateRandomUsername(length int) (string, error) {
	username, err := generateRandomString(length)
	if err != nil {
		return "", err
	}
	if !strings.Contains(username, "+") {
		return username, nil
	}
	// Defensive fallback to guarantee '+' is never used as part of generated usernames.
	return strings.ReplaceAll(username, "+", "_"), nil
}
