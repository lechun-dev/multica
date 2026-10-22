package realtime

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/multica-ai/multica/server/internal/auth"
)

func TestMissionReadTokenCannotAuthenticateWebSocket(t *testing.T) {
	claims := auth.MissionReadClaims(testUserID, "", "7db83b86-61ad-4e24-82bd-c9db7ea5bfaa", time.Now())
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(auth.JWTSecret())
	if err != nil {
		t.Fatalf("sign read token: %v", err)
	}
	uid, errMsg := authenticateToken(token, nil, context.Background())
	if uid != "" || errMsg == "" {
		t.Fatalf("restricted read token accepted by WebSocket: user=%q error=%q", uid, errMsg)
	}
}
