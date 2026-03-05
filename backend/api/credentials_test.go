package api

import "testing"

func TestGenerateRandomUsernameExcludesPlus(t *testing.T) {
	for i := 0; i < 256; i++ {
		username, err := generateRandomUsername(12)
		if err != nil {
			t.Fatalf("generateRandomUsername failed: %v", err)
		}
		if len(username) == 0 {
			t.Fatalf("generated username should not be empty")
		}
		for _, ch := range username {
			if ch == '+' {
				t.Fatalf("generated username must not contain '+': %q", username)
			}
		}
	}
}
