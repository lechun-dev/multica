package auth

import (
	"errors"
	"testing"
	"time"
)

func TestMissionReadTokenCannotBecomeSession(t *testing.T) {
	claims := MissionReadClaims("user-1", "user-1@multica.ai", "7db83b86-61ad-4e24-82bd-c9db7ea5bfaa", time.Now())
	token := signSession(t, claims)
	if parsed, err := ParseSessionToken(token); !errors.Is(err, ErrNotSessionToken) || parsed != nil {
		t.Fatalf("restricted read token accepted as session: claims=%v err=%v", parsed, err)
	}
}

func TestMissionReadClaimsCannotRenewSession(t *testing.T) {
	claims := MissionReadClaims("user-1", "user-1@multica.ai", "7db83b86-61ad-4e24-82bd-c9db7ea5bfaa", time.Now())
	token, expiry, err := RenewSessionToken(claims)
	if !errors.Is(err, ErrNotSessionToken) || token != "" || !expiry.IsZero() {
		t.Fatal("restricted read claims must not mint an unrestricted session")
	}
}
