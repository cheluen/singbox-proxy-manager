package api

import "testing"

func TestGenerateRandomUsernameExcludesPlus(t *testing.T) {
	for i := 0; i < 256; i++ {
		username, err := generateRandomString(12)
		if err != nil {
			t.Fatalf("generateRandomString failed: %v", err)
		}
		if len(username) != 12 {
			t.Fatalf("unexpected generated username length: %d", len(username))
		}
		for _, ch := range username {
			if ch == '+' {
				t.Fatalf("generated username must not contain '+': %q", username)
			}
		}
	}
}
