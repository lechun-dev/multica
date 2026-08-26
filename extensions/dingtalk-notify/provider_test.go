package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDingTalkProviderRefreshesTokenAfterUnauthorized(t *testing.T) {
	var tokenCalls, sendCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			atomic.AddInt32(&tokenCalls, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "token", "expireIn": 7200})
		case "/v1.0/robot/oToMessages/batchSend":
			if atomic.AddInt32(&sendCalls, 1) == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":"InvalidAuthentication"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := &DingTalkProvider{BaseURL: srv.URL, ClientID: "app", ClientSecret: "secret", RobotCode: "robot"}
	err := p.Send(context.Background(), Message{EventID: "e1", WorkspaceID: "w1", ChannelType: "p2p", DingUserID: "user", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if tokenCalls != 2 || sendCalls != 2 {
		t.Fatalf("token calls=%d send calls=%d", tokenCalls, sendCalls)
	}
}

func TestDingTalkProviderClassifiesRateLimitAsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "accessToken") {
			_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "token", "expireIn": 7200})
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := &DingTalkProvider{BaseURL: srv.URL, ClientID: "app", ClientSecret: "secret", RobotCode: "robot"}
	err := p.Send(context.Background(), Message{ChannelType: "group", ChannelID: "group", Text: "hello"})
	var retry RetryableError
	if !errors.As(err, &retry) {
		t.Fatalf("expected retryable error, got %v", err)
	}
}

func TestDingTalkOAuthProviderExchangesCodeAndLoadsIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]string{"accessToken": "oauth-token"})
		case "/user":
			_ = json.NewEncoder(w).Encode(map[string]string{"userid": "u1", "unionId": "union-1", "openId": "open-1", "nick": "Alice"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := DingTalkOAuthProvider{AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token", UserURL: srv.URL + "/user", ClientID: "id", ClientSecret: "secret"}
	url, err := p.AuthorizationURL(context.Background(), "state", "https://app/callback")
	if err != nil || !strings.Contains(url, "state=state") {
		t.Fatalf("url=%q err=%v", url, err)
	}
	user, err := p.ExchangeCode(context.Background(), "code", "https://app/callback")
	if err != nil || user.DingUserID != "u1" || user.UnionID != "union-1" {
		t.Fatalf("user=%+v err=%v", user, err)
	}
}
