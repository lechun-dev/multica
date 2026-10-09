package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/events"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/redis/go-redis/v9"
)

type organizationReadScopeKey struct{}
type organizationReadScope struct {
	cache     *OrganizationReadCache
	userID    string
	loader    func(context.Context, string, string) ([]string, error)
	endpoint  string
	expiresAt atomic.Int64
}

func (s *organizationReadScope) boundExpiry(expiry time.Time) {
	for {
		old := s.expiresAt.Load()
		if old != 0 && old <= expiry.UnixNano() {
			return
		}
		if s.expiresAt.CompareAndSwap(old, expiry.UnixNano()) {
			return
		}
	}
}

type organizationReadEntry struct {
	IDs       []string  `json:"ids"`
	ExpiresAt time.Time `json:"expires_at"`
}

// 2026-10-09 coder(lq): Organization ancestry is shared only by explicitly
// opted-in human reads. Workspace generations survive unrelated issue events.
type OrganizationReadCache struct {
	cache   *FrequentReadCache
	Metrics *obsmetrics.ReadCacheMetrics
}

func NewOrganizationReadCache(rdb redis.UniversalClient, namespace string) *OrganizationReadCache {
	return &OrganizationReadCache{cache: NewFrequentReadCache(rdb, namespace+":organization-reads")}
}

// 2026-10-09 coder(lq): UUID casing in request headers must not create a cache
// partition that ignores canonical workspace IDs from mutation events.
func organizationCacheID(value string) string {
	if id, err := uuid.Parse(value); err == nil {
		return id.String()
	}
	return value
}

func (c *OrganizationReadCache) generation(ctx context.Context, workspace string) (string, error) {
	workspace = organizationCacheID(workspace)
	if time.Now().UnixNano() < c.cache.blockedUntil.Load() {
		return "", errors.New("organization invalidation unavailable")
	}
	key := c.cache.prefix + "workspace:" + workspace
	version, err := c.cache.read(ctx, key)
	if errors.Is(err, redis.Nil) {
		// 2026-10-09 coder(lq): Eviction never revives a reusable initial key.
		// Concurrent initialization can discard a fill, but cannot revive stale IDs.
		result := c.cache.loads.DoChan(key+":initialize", func() (any, error) {
			if version, err := c.cache.read(ctx, key); !errors.Is(err, redis.Nil) {
				return version, err
			}
			if err := c.cache.save(ctx, key, uuid.NewString(), time.Minute); err != nil {
				return "", err
			}
			return c.cache.read(ctx, key)
		})
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case loaded := <-result:
			if loaded.Err != nil {
				return "", loaded.Err
			}
			return loaded.Val.(string), nil
		}
	}
	return version, err
}

func (c *OrganizationReadCache) Invalidate(workspace string) {
	c.invalidate(workspace, "manual")
}

func (c *OrganizationReadCache) invalidate(workspace, reason string) {
	if c == nil || workspace == "" {
		return
	}
	workspace = organizationCacheID(workspace)
	result := "success"
	if err := c.cache.save(context.Background(), c.cache.prefix+"workspace:"+workspace, uuid.NewString(), time.Minute); err != nil {
		result = "error"
		c.cache.blockedUntil.Store(time.Now().Add(frequentReadTTL).UnixNano())
		slog.Warn("organization read cache invalidation failed; bypassing cache", "error", err)
	}
	c.Metrics.RecordInvalidation("organization", "workspace", reason, result)
}

func (c *OrganizationReadCache) Observe(e events.Event) {
	if strings.HasPrefix(e.Type, "organization:") || strings.HasPrefix(e.Type, "projectauth:") || strings.HasPrefix(e.Type, "member:") || e.Type == "workspace:deleted" {
		c.invalidate(e.WorkspaceID, strings.SplitN(e.Type, ":", 2)[0])
	}
}

// 2026-10-09 coder(lq): Import/sync can change membership, hierarchy and active
// states without events. Invalidate both sides, using the URL workspace rather
// than the currently selected workspace header. Preview is a read.
func (c *OrganizationReadCache) MutationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if r.Method == http.MethodPost && len(parts) == 6 && parts[0] == "api" && parts[1] == "workspaces" && parts[3] == "projectauth" && parts[4] == "organizations" && (parts[5] == "import" || parts[5] == "sync") {
			if workspace, err := uuid.Parse(parts[2]); err == nil {
				c.invalidate(workspace.String(), "mutation")
				defer c.invalidate(workspace.String(), "mutation")
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (c *OrganizationReadCache) load(ctx context.Context, workspace, user string, loader func(context.Context, string, string) ([]string, error)) (organizationReadEntry, error) {
	endpoint, lookupResult := "other", "bypass"
	if scope, ok := ctx.Value(organizationReadScopeKey{}).(*organizationReadScope); ok {
		endpoint = scope.endpoint
	}
	defer func() { c.Metrics.RecordLookup("organization", endpoint, lookupResult) }()
	workspace, user = organizationCacheID(workspace), organizationCacheID(user)
	version, err := c.generation(ctx, workspace)
	if err != nil {
		c.Metrics.RecordFallback("organization", endpoint, "generation_unavailable")
		return organizationReadEntry{}, err
	}
	key := c.cache.prefix + workspace + ":" + version + ":" + user
	read := func() (organizationReadEntry, bool) {
		payload, err := c.cache.read(ctx, key)
		if err != nil && !errors.Is(err, redis.Nil) {
			c.Metrics.RecordFallback("organization", endpoint, "store_error")
		}
		var entry organizationReadEntry
		ok := err == nil && json.Unmarshal([]byte(payload), &entry) == nil && time.Now().Before(entry.ExpiresAt)
		return entry, ok
	}
	entry, hit := read()
	lookupResult = "hit"
	if !hit {
		lookupResult = "miss"
		result := c.cache.loads.DoChan(key, func() (any, error) {
			if entry, hit := read(); hit {
				return entry, nil
			}
			started := time.Now()
			loadResult := "error"
			defer func() { c.Metrics.ObserveLoad("organization", endpoint, loadResult, time.Since(started)) }()
			// 2026-10-09 coder(lq): A slow preload falls back to the original statement-local recursion.
			loadCtx, cancel := context.WithTimeout(ctx, frequentReadStoreTimeout)
			defer cancel()
			ids, err := loader(loadCtx, workspace, user)
			if err != nil {
				reason := "load_error"
				if errors.Is(err, context.DeadlineExceeded) || errors.Is(loadCtx.Err(), context.DeadlineExceeded) {
					loadResult, reason = "timeout", "load_timeout"
				} else if errors.Is(err, context.Canceled) || errors.Is(loadCtx.Err(), context.Canceled) {
					loadResult, reason = "canceled", "canceled"
				}
				c.Metrics.RecordFallback("organization", endpoint, reason)
				return nil, err
			}
			loadResult = "success"
			entry := organizationReadEntry{IDs: ids, ExpiresAt: started.Add(frequentReadTTL)}
			current, err := c.generation(ctx, workspace)
			if err != nil || current != version {
				c.Metrics.RecordFallback("organization", endpoint, "generation_changed")
				return nil, errors.New("organization set changed during load")
			}
			if payload, err := json.Marshal(entry); err == nil && len(payload) <= frequentReadMaxPayload && time.Now().Before(entry.ExpiresAt) {
				if err := c.cache.save(ctx, key, string(payload), time.Until(entry.ExpiresAt)); err != nil {
					c.Metrics.RecordFallback("organization", endpoint, "store_error")
				}
			}
			return entry, nil
		})
		select {
		case <-ctx.Done():
			return organizationReadEntry{}, ctx.Err()
		case result := <-result:
			if result.Err != nil {
				return organizationReadEntry{}, result.Err
			}
			entry = result.Val.(organizationReadEntry)
		}
	}
	current, err := c.generation(ctx, workspace)
	if err != nil || current != version || !time.Now().Before(entry.ExpiresAt) {
		c.Metrics.RecordFallback("organization", endpoint, "generation_changed")
		return organizationReadEntry{}, errors.New("organization cache changed or expired")
	}
	return entry, nil
}

func (h *Handler) organizationReadSQL(r *http.Request, workspaceRef, userRef string, addArg func(any) string) string {
	scope, ok := r.Context().Value(organizationReadScopeKey{}).(*organizationReadScope)
	if ok && scope.loader != nil {
		workspace := h.resolveWorkspaceID(r)
		entry, err := scope.cache.load(r.Context(), workspace, scope.userID, scope.loader)
		if err == nil {
			scope.boundExpiry(entry.ExpiresAt)
			return fmt.Sprintf("SELECT unnest(%s::text[])", addArg(entry.IDs))
		}
	}
	return userOrganizationIDsSQL(workspaceRef, userRef)
}
