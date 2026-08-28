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
	return OAuthUser{DingUserID: "ding-1", Email: "alice@example.com"}, nil
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

func TestLoginOAuthServiceConsumesStateAndReturnsIdentity(t *testing.T) {
	now := time.Unix(100, 0)
	svc := &LoginOAuthService{Provider: oauthStub{}, Now: func() time.Time { return now }}
	auth, err := svc.Begin(context.Background(), "https://example.test/auth/dingtalk/callback")
	if err != nil || auth.State == "" || auth.URL == "" {
		t.Fatalf("auth=%+v err=%v", auth, err)
	}
	user, err := svc.Complete(context.Background(), auth.State, "code", "https://example.test/auth/dingtalk/callback")
	if err != nil || user.DingUserID != "ding-1" || user.Email != "alice@example.com" {
		t.Fatalf("user=%+v err=%v", user, err)
	}
	if _, err := svc.Complete(context.Background(), auth.State, "code", "https://example.test/auth/dingtalk/callback"); err == nil {
		t.Fatal("login state replay should fail")
	}
}

func TestLoginOAuthServiceCarriesDesktopClientInVerifiedState(t *testing.T) {
	now := time.Unix(100, 0)
	svc := &LoginOAuthService{Provider: oauthStub{}, Now: func() time.Time { return now }}
	auth, err := svc.BeginForClient(context.Background(), "https://example.test/auth/dingtalk/callback", LoginClientDesktop)
	if err != nil {
		t.Fatal(err)
	}
	if got := LoginClientFromState(auth.State); got != LoginClientDesktop {
		t.Fatalf("client=%q, want %q", got, LoginClientDesktop)
	}
	if _, err := svc.Complete(context.Background(), auth.State, "code", "https://example.test/auth/dingtalk/callback"); err != nil {
		t.Fatal(err)
	}
}

func TestLoginOAuthServiceCarriesPostLoginPathInState(t *testing.T) {
	svc := &LoginOAuthService{Provider: oauthStub{}, Now: func() time.Time { return time.Unix(100, 0) }}
	next := "/acme/issues/MUL-67#comment-comment-1"
	auth, err := svc.BeginForClientWithNext(context.Background(), "https://example.test/auth/dingtalk/callback", LoginClientWeb, next)
	if err != nil {
		t.Fatal(err)
	}
	if got := LoginNextFromState(auth.State); got != next {
		t.Fatalf("next=%q, want %q", got, next)
	}
	if got := LoginClientFromState(auth.State); got != LoginClientWeb {
		t.Fatalf("client=%q, want %q", got, LoginClientWeb)
	}
}

func TestLoginOAuthServiceCarriesDevelopmentDesktopClientInVerifiedState(t *testing.T) {
	now := time.Unix(100, 0)
	svc := &LoginOAuthService{Provider: oauthStub{}, Now: func() time.Time { return now }}
	auth, err := svc.BeginForClient(context.Background(), "https://example.test/auth/dingtalk/callback", LoginClientDesktopDev)
	if err != nil {
		t.Fatal(err)
	}
	if got := LoginClientFromState(auth.State); got != LoginClientDesktopDev {
		t.Fatalf("client=%q, want %q", got, LoginClientDesktopDev)
	}
}

func TestLoginOAuthServiceCarriesLechunDesktopClientInVerifiedState(t *testing.T) {
	now := time.Unix(100, 0)
	svc := &LoginOAuthService{Provider: oauthStub{}, Now: func() time.Time { return now }}
	auth, err := svc.BeginForClient(context.Background(), "https://example.test/auth/dingtalk/callback", LoginClientDesktopLechun)
	if err != nil {
		t.Fatal(err)
	}
	if got := LoginClientFromState(auth.State); got != LoginClientDesktopLechun {
		t.Fatalf("client=%q, want %q", got, LoginClientDesktopLechun)
	}
}

func TestLoginOAuthServiceRejectsUnsupportedClient(t *testing.T) {
	svc := &LoginOAuthService{Provider: oauthStub{}}
	if _, err := svc.BeginForClient(context.Background(), "https://example.test/auth/dingtalk/callback", LoginClient("mobile")); err == nil {
		t.Fatal("unsupported OAuth client should fail")
	}
}

func TestLoginOAuthServiceExpiresState(t *testing.T) {
	now := time.Unix(100, 0)
	svc := &LoginOAuthService{Provider: oauthStub{}, Now: func() time.Time { return now }, TTL: time.Second}
	auth, err := svc.Begin(context.Background(), "https://example.test/auth/dingtalk/callback")
	if err != nil {
		t.Fatal(err)
	}
	svc.Now = func() time.Time { return now.Add(2 * time.Second) }
	if _, err := svc.Complete(context.Background(), auth.State, "code", "https://example.test/auth/dingtalk/callback"); err == nil {
		t.Fatal("expired login state should fail")
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
