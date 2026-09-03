package notify

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"
)

type OAuthUser struct {
	DingUserID        string
	UnionID           string
	OpenID            string
	Name              string
	Email             string
	AvatarURL         string
	Departments       []DingTalkDepartment
	DepartmentsSynced bool
}

// DingTalkDepartment is profile metadata from the enterprise directory. The
// stable ID stays server-side; profile API responses expose names only.
type DingTalkDepartment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// 2026-09-03 coder(lq): Keep the complete directory snapshot provider-neutral;
// the host resolves external identities to Multica users transactionally.
// DingTalkDirectorySnapshot is the complete enterprise directory used by
// workspace authorization synchronization. It intentionally contains only
// stable directory identifiers and display metadata; Multica user IDs are
// resolved by the host application.
type DingTalkDirectorySnapshot struct {
	Departments []DingTalkDirectoryDepartment
	Members     []DingTalkDirectoryMember
}

type DingTalkDirectoryDepartment struct {
	ID       string
	Name     string
	ParentID string
}

type DingTalkDirectoryMember struct {
	DingUserID    string
	UnionID       string
	Name          string
	Email         string
	DepartmentIDs []string
}

type OAuthProvider interface {
	AuthorizationURL(ctx context.Context, state, redirectURI string) (string, error)
	ExchangeCode(ctx context.Context, code, redirectURI string) (OAuthUser, error)
}

type BindingStore interface {
	Upsert(ctx context.Context, binding MemberBinding) error
	Get(ctx context.Context, workspaceID, memberID string) (MemberBinding, bool, error)
	Revoke(ctx context.Context, workspaceID, memberID string, at time.Time) error
}

type pendingOAuth struct {
	WorkspaceID string
	MemberID    string
	ExpiresAt   time.Time
}

type OAuthService struct {
	Provider OAuthProvider
	Bindings BindingStore
	Now      func() time.Time
	TTL      time.Duration
	mu       sync.Mutex
	pending  map[string]pendingOAuth
}

type Authorization struct {
	URL   string
	State string
}

// Begin creates a single-use state tied to the currently authenticated
// Multica member. The callback must never choose a member from query params.
func (s *OAuthService) Begin(ctx context.Context, workspaceID, memberID, redirectURI string) (Authorization, error) {
	if s.Provider == nil {
		return Authorization{}, errors.New("oauth provider is required")
	}
	if workspaceID == "" || memberID == "" {
		return Authorization{}, errors.New("workspace and member are required")
	}
	state, err := randomState()
	if err != nil {
		return Authorization{}, err
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	ttl := s.TTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	s.mu.Lock()
	if s.pending == nil {
		s.pending = make(map[string]pendingOAuth)
	}
	s.pending[state] = pendingOAuth{WorkspaceID: workspaceID, MemberID: memberID, ExpiresAt: now.Add(ttl)}
	s.mu.Unlock()
	url, err := s.Provider.AuthorizationURL(ctx, state, redirectURI)
	if err != nil {
		s.mu.Lock()
		delete(s.pending, state)
		s.mu.Unlock()
		return Authorization{}, err
	}
	return Authorization{URL: url, State: state}, nil
}

// Complete consumes state before exchanging the code, preventing replay even
// if a provider responds slowly or returns an error.
func (s *OAuthService) Complete(ctx context.Context, state, code, redirectURI string) (MemberBinding, error) {
	if s.Provider == nil || s.Bindings == nil {
		return MemberBinding{}, errors.New("oauth provider and binding store are required")
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	s.mu.Lock()
	pending, ok := s.pending[state]
	if ok {
		delete(s.pending, state)
	}
	s.mu.Unlock()
	if !ok || state == "" || pending.ExpiresAt.Before(now) {
		return MemberBinding{}, errors.New("oauth state is invalid or expired")
	}
	if code == "" {
		return MemberBinding{}, errors.New("oauth code is required")
	}
	user, err := s.Provider.ExchangeCode(ctx, code, redirectURI)
	if err != nil {
		return MemberBinding{}, err
	}
	if user.DingUserID == "" {
		return MemberBinding{}, errors.New("oauth provider returned no DingTalk user id")
	}
	binding := MemberBinding{WorkspaceID: pending.WorkspaceID, MemberID: pending.MemberID, DingUserID: user.DingUserID, Active: true}
	if err := s.Bindings.Upsert(ctx, binding); err != nil {
		return MemberBinding{}, fmt.Errorf("save DingTalk binding: %w", err)
	}
	return binding, nil
}

func randomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

type MemoryBindingStore struct {
	mu       sync.Mutex
	bindings map[string]MemberBinding
}

func NewMemoryBindingStore() *MemoryBindingStore {
	return &MemoryBindingStore{bindings: make(map[string]MemberBinding)}
}
func bindingKey(workspaceID, memberID string) string { return workspaceID + "\x00" + memberID }
func (s *MemoryBindingStore) Upsert(_ context.Context, binding MemberBinding) error {
	if binding.WorkspaceID == "" || binding.MemberID == "" {
		return errors.New("binding workspace and member are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings[bindingKey(binding.WorkspaceID, binding.MemberID)] = binding
	return nil
}
func (s *MemoryBindingStore) Get(_ context.Context, workspaceID, memberID string) (MemberBinding, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bindings[bindingKey(workspaceID, memberID)]
	return b, ok, nil
}
func (s *MemoryBindingStore) Revoke(_ context.Context, workspaceID, memberID string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bindings[bindingKey(workspaceID, memberID)]
	if !ok {
		return nil
	}
	b.Active = false
	s.bindings[bindingKey(workspaceID, memberID)] = b
	return nil
}
