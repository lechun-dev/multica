package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

const frequentReadTTL = 15 * time.Second
const frequentReadMaxPayload = 512 * 1024
const frequentReadStoreTimeout = 250 * time.Millisecond

// 2026-10-09 coder(lq): Cache only human list/statistic responses after router
// authentication. Shared generations discard pre-mutation fills on every API
// node; live claim/write authorization never consumes these responses.
type FrequentReadCache struct {
	store        agentMetricsCacheStore
	prefix       string
	loads        singleflight.Group
	blockedUntil atomic.Int64
}

func NewFrequentReadCache(rdb redis.UniversalClient, namespace string) *FrequentReadCache {
	var store agentMetricsCacheStore = &memoryFrequentReadStore{values: make(map[string]memoryFrequentReadEntry)}
	if rdb != nil {
		store = redisAgentMetricsCacheStore{rdb: rdb}
	}
	cache := &FrequentReadCache{store: store, prefix: fmt.Sprintf("mul:frequent_reads:v1:%x:", sha256.Sum256([]byte(namespace)))}
	cache.Invalidate()
	return cache
}

// 2026-10-09 coder(lq): Cache/expiry metadata outages must not consume the
// list query's HTTP timeout; a short timeout falls back to the live response.
func (c *FrequentReadCache) read(ctx context.Context, key string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, frequentReadStoreTimeout)
	defer cancel()
	return c.store.Get(ctx, key)
}
func (c *FrequentReadCache) save(ctx context.Context, key, value string, ttl time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, frequentReadStoreTimeout)
	defer cancel()
	return c.store.Set(ctx, key, value, ttl)
}

func (c *FrequentReadCache) generation(ctx context.Context) (string, error) {
	if time.Now().UnixNano() < c.blockedUntil.Load() {
		return "", errors.New("read cache invalidation unavailable")
	}
	value, err := c.read(ctx, c.prefix+"generation")
	if errors.Is(err, redis.Nil) {
		return "", errors.New("read cache generation unavailable")
	}
	return value, err
}

func (c *FrequentReadCache) Invalidate() {
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), frequentReadStoreTimeout)
	defer cancel()
	// A random generation avoids ABA after a Redis restart or counter eviction.
	if err := c.store.Set(ctx, c.prefix+"generation", uuid.NewString(), 0); err != nil {
		c.blockedUntil.Store(time.Now().Add(frequentReadTTL).UnixNano())
		slog.Warn("frequent read cache invalidation failed; bypassing local cache", "error", err)
	}
}

func (c *FrequentReadCache) Observe(e events.Event) {
	for _, prefix := range []string{"issue:", "project:", "member:", "workspace:", "inbox:", "organization:", "projectauth:", "permission:"} {
		if strings.HasPrefix(e.Type, prefix) {
			c.Invalidate()
			return
		}
	}
}

// 2026-10-09 coder(lq): Invalidate both sides of API mutations, including role
// and organization administration that may not publish domain events. The
// POST list twins are reads and must not evict their own entries.
func (c *FrequentReadCache) MutationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		relevant := false
		for _, prefix := range []string{"/api/issues", "/api/inbox", "/api/projects", "/api/workspaces", "/api/project-permission-roles", "/api/task-permission-roles"} {
			relevant = relevant || path == prefix || strings.HasPrefix(path, prefix+"/")
		}
		readPost := r.Method == http.MethodPost && (strings.HasPrefix(path, "/api/issues/table/") || path == "/api/issues/query")
		if relevant && !readPost && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			c.Invalidate()
			defer c.Invalidate()
		}
		next.ServeHTTP(w, r)
	})
}

type frequentReadResponse struct {
	Status  int         `json:"status"`
	Body    []byte      `json:"body"`
	Headers http.Header `json:"headers"`
}

type frequentReadWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *frequentReadWriter) Header() http.Header { return w.header }
func (w *frequentReadWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *frequentReadWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.body.Write(p)
}

func writeFrequentRead(w http.ResponseWriter, response frequentReadResponse) {
	for name, values := range response.Headers {
		w.Header()[name] = append([]string(nil), values...)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(response.Status)
	_, _ = w.Write(response.Body)
}

func (h *Handler) CacheFrequentRead(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cache := h.FrequentReadCache
		user := requestUserID(r)
		if user == "" || isMachineCredentialActor(r) || r.Header.Get("X-Agent-ID") != "" {
			next(w, r)
			return
		}
		if h.OrganizationReadCache != nil {
			// 2026-10-09 coder(lq): Preload through the original DB pool. A
			// timeout inside a table snapshot transaction would abort that transaction.
			repo := &projectAuthRepository{db: h.DB}
			scope := &organizationReadScope{cache: h.OrganizationReadCache, userID: user, loader: repo.listUserOrganizations}
			r = r.WithContext(context.WithValue(r.Context(), organizationReadScopeKey{}, scope))
		}
		if cache == nil {
			next(w, r)
			return
		}
		generation, err := cache.generation(r.Context())
		if err != nil {
			next(w, r)
			return
		}
		var body []byte
		if r.Body != nil {
			body, err = io.ReadAll(io.LimitReader(r.Body, 2*1024*1024+1))
			r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
			if err != nil || len(body) > 2*1024*1024 {
				next(w, r)
				return
			}
		}
		workspace := h.resolveWorkspaceID(r)
		phase := projectauth.RolloutOff
		if h.ProjectAuth != nil {
			phase = h.ProjectAuth.RolloutPhase()
		}
		identity := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%t|%s", generation, user, workspace, r.Method, r.URL.Path, r.URL.Query().Encode(), phase, projectauth.WorkspaceOwnerBypassEnabledFromEnvironment(), body)
		key := fmt.Sprintf("%s%x", cache.prefix, sha256.Sum256([]byte(identity)))
		serveHit := func() bool {
			payload, err := cache.read(r.Context(), key)
			if err != nil {
				return false
			}
			var response frequentReadResponse
			if json.Unmarshal([]byte(payload), &response) != nil || response.Status != http.StatusOK || !json.Valid(response.Body) {
				return false
			}
			current, err := cache.generation(r.Context())
			if err != nil || current != generation {
				return false
			}
			writeFrequentRead(w, response)
			return true
		}
		if serveHit() {
			return
		}
		result := cache.loads.DoChan(key, func() (any, error) {
			// 2026-10-09 coder(lq): Another fill can finish between the outer miss
			// and singleflight entry; recheck before starting another database read.
			if payload, err := cache.read(r.Context(), key); err == nil {
				var response frequentReadResponse
				if json.Unmarshal([]byte(payload), &response) == nil && response.Status == http.StatusOK && json.Valid(response.Body) {
					return response, nil
				}
			}
			started := time.Now()
			capture := &frequentReadWriter{header: make(http.Header)}
			loadRequest := r.Clone(r.Context())
			loadRequest.Body = io.NopCloser(bytes.NewReader(body))
			next(capture, loadRequest)
			if err := r.Context().Err(); err != nil {
				return nil, err
			}
			response := frequentReadResponse{Status: capture.status, Body: capture.body.Bytes(), Headers: capture.header.Clone()}
			if response.Status == 0 {
				response.Status = http.StatusOK
			}
			if response.Status != http.StatusOK || capture.header.Get("Set-Cookie") != "" {
				return response, nil
			}
			expiryWorkspace := workspace
			if r.URL.Path == "/api/inbox/unread-summary" {
				expiryWorkspace = ""
			}
			ttl := h.frequentReadExpiry(r.Context(), expiryWorkspace, user, started)
			current, err := cache.generation(r.Context())
			if err == nil && current == generation && ttl > 0 && len(response.Body) <= frequentReadMaxPayload && r.Context().Err() == nil {
				if encoded, err := json.Marshal(response); err == nil {
					_ = cache.save(r.Context(), key, string(encoded), ttl)
				}
			}
			return response, nil
		})
		select {
		case <-r.Context().Done():
			return
		case loaded := <-result:
			current, err := cache.generation(r.Context())
			response, ok := loaded.Val.(frequentReadResponse)
			if err != nil || current != generation || loaded.Err != nil || !ok {
				next(w, r)
				return
			}
			writeFrequentRead(w, response)
		}
	}
}

// 2026-10-09 coder(lq): A timed grant can expire without any mutation/event.
// Never store a response past the earliest pending expiry, including grants
// that expired while the list was executing. Failed expiry reads disable caching.
func (h *Handler) frequentReadExpiry(ctx context.Context, workspace, user string, started time.Time) time.Duration {
	ttl := time.Until(started.Add(frequentReadTTL))
	// 2026-10-09 coder(lq): Layered caches must share the oldest expiry;
	// otherwise a 14-second-old organization set could live another 15 seconds.
	if scope, ok := ctx.Value(organizationReadScopeKey{}).(*organizationReadScope); ok {
		if expiry := scope.expiresAt.Load(); expiry > 0 {
			ttl = min(ttl, time.Until(time.Unix(0, expiry)))
		}
	}
	if h.ProjectAuth == nil || !h.ProjectAuth.Enabled() {
		return ttl
	}
	ctx, cancel := context.WithTimeout(ctx, frequentReadStoreTimeout)
	defer cancel()
	var expiry pgtype.Timestamptz
	var err error
	if workspace != "" {
		err = h.DB.QueryRow(ctx, "SELECT min(expires_at) FROM projectauth_grant_constraints WHERE workspace_id=$1::uuid AND expires_at>$2", workspace, started).Scan(&expiry)
	} else {
		err = h.DB.QueryRow(ctx, `SELECT min(g.expires_at) FROM projectauth_grant_constraints g
   JOIN member m ON m.workspace_id=g.workspace_id WHERE m.user_id=$1::uuid AND g.expires_at>$2`, user, started).Scan(&expiry)
	}
	if err != nil {
		return 0
	}
	if expiry.Valid {
		ttl = min(ttl, time.Until(expiry.Time))
	}
	return ttl
}

type memoryFrequentReadEntry struct {
	value   string
	expires time.Time
}
type memoryFrequentReadStore struct {
	mu     sync.Mutex
	values map[string]memoryFrequentReadEntry
}

func (s *memoryFrequentReadStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.values[key]
	if !ok || (!entry.expires.IsZero() && !time.Now().Before(entry.expires)) {
		delete(s.values, key)
		return "", redis.Nil
	}
	return entry.value, nil
}
func (s *memoryFrequentReadStore) Set(_ context.Context, key, value string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, entry := range s.values {
		if !entry.expires.IsZero() && !time.Now().Before(entry.expires) {
			delete(s.values, key)
		}
	}
	if len(s.values) >= 128 {
		for key, entry := range s.values {
			if !entry.expires.IsZero() {
				delete(s.values, key)
				break
			}
		}
	}
	entry := memoryFrequentReadEntry{value: value}
	if ttl > 0 {
		entry.expires = time.Now().Add(ttl)
	}
	s.values[key] = entry
	return nil
}
