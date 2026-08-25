package notify

import (
	"context"
	"testing"
	"time"
)

type oauthStub struct{}

func (oauthStub) AuthorizationURL(_ context.Context, state, redirect string) (string, error) {
	return redirect + "?state=" + state, nil
}
func (oauthStub) ExchangeCode(context.Context, string, string) (OAuthUser, error) {
	return OAuthUser{DingUserID: "ding-1"}, nil
}

func TestOAuthBindsCurrentMemberAndRejectsStateReplay(t *testing.T) {
	store := NewMemoryBindingStore()
	svc := &OAuthService{Provider: oauthStub{}, Bindings: store, Now: func() time.Time { return time.Unix(100, 0) }}
	auth, err := svc.Begin(context.Background(), "w1", "m1", "https://example.test/callback")
	if err != nil || auth.URL == "" || auth.State == "" {
		t.Fatalf("auth=%+v err=%v", auth, err)
	}
	binding, err := svc.Complete(context.Background(), auth.State, "code", "https://example.test/callback")
	if err != nil || binding.DingUserID != "ding-1" || binding.WorkspaceID != "w1" || binding.MemberID != "m1" {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
	if _, err := svc.Complete(context.Background(), auth.State, "code", "https://example.test/callback"); err == nil {
		t.Fatal("state replay should fail")
	}
	got, found, _ := store.Get(context.Background(), "w1", "m1")
	if !found || !got.Active {
		t.Fatalf("stored binding=%+v found=%v", got, found)
	}
}

func TestOAuthExpiresState(t *testing.T) {
	now := time.Unix(100, 0)
	svc := &OAuthService{Provider: oauthStub{}, Bindings: NewMemoryBindingStore(), Now: func() time.Time { return now }, TTL: time.Second}
	auth, _ := svc.Begin(context.Background(), "w1", "m1", "https://example.test/callback")
	svc.Now = func() time.Time { return now.Add(2 * time.Second) }
	if _, err := svc.Complete(context.Background(), auth.State, "code", "https://example.test/callback"); err == nil {
		t.Fatal("expired state should fail")
	}
}
