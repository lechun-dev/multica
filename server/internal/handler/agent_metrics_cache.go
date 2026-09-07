package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	agentMetricsCacheTTL    = 30 * time.Second
	agentMetricsCachePrefix = "mul:agent_metrics:v1:"
)

type agentMetricsCacheStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
}

type redisAgentMetricsCacheStore struct {
	rdb *redis.Client
}

func (s redisAgentMetricsCacheStore) Get(ctx context.Context, key string) (string, error) {
	return s.rdb.Get(ctx, key).Result()
}

func (s redisAgentMetricsCacheStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return s.rdb.Set(ctx, key, value, ttl).Err()
}

// 2026-09-07 coder(lq): AgentMetricsCache keeps the two expensive
// permission-aware agent aggregates shared across API instances. Redis errors
// are deliberately treated as cache misses so metrics remain available during
// a cache outage.
type AgentMetricsCache struct {
	store agentMetricsCacheStore
	ttl   time.Duration
	loads singleflight.Group
}

func NewAgentMetricsCache(rdb *redis.Client) *AgentMetricsCache {
	if rdb == nil {
		return nil
	}
	return newAgentMetricsCache(redisAgentMetricsCacheStore{rdb: rdb}, agentMetricsCacheTTL)
}

func newAgentMetricsCache(store agentMetricsCacheStore, ttl time.Duration) *AgentMetricsCache {
	if store == nil || ttl <= 0 {
		return nil
	}
	return &AgentMetricsCache{store: store, ttl: ttl}
}

func agentMetricsCacheKey(metric, workspaceID, userID string) string {
	return fmt.Sprintf("%s%s:%s:%s", agentMetricsCachePrefix, metric, workspaceID, userID)
}

func readAgentMetricsCache[T any](ctx context.Context, cache *AgentMetricsCache, key string) ([]T, bool) {
	payload, err := cache.store.Get(ctx, key)
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			slog.WarnContext(ctx, "agent metrics cache read failed; falling back to database", "key", key, "error", err)
		}
		return nil, false
	}

	var rows []T
	if err := json.Unmarshal([]byte(payload), &rows); err != nil {
		slog.WarnContext(ctx, "agent metrics cache value is invalid; falling back to database", "key", key, "error", err)
		return nil, false
	}
	return rows, true
}

// 2026-09-07 coder(lq): loadAgentMetrics first checks Redis, then collapses
// concurrent misses in this process. The shared database load outlives an
// individual disconnected HTTP request so another waiter does not immediately
// start the same expensive SQL.
func loadAgentMetrics[T any](ctx context.Context, cache *AgentMetricsCache, key string, loader func(context.Context) ([]T, error)) ([]T, error) {
	if cache == nil {
		return loader(ctx)
	}
	if rows, ok := readAgentMetricsCache[T](ctx, cache, key); ok {
		return rows, nil
	}

	result := cache.loads.DoChan(key, func() (any, error) {
		sharedCtx := context.WithoutCancel(ctx)
		// 2026-09-07 coder(lq): Recheck Redis after entering singleflight because
		// another instance may have filled the shared cache during this miss.
		if rows, ok := readAgentMetricsCache[T](sharedCtx, cache, key); ok {
			return rows, nil
		}

		rows, err := loader(sharedCtx)
		if err != nil {
			return nil, err
		}
		payload, err := json.Marshal(rows)
		if err != nil {
			slog.WarnContext(sharedCtx, "agent metrics cache encode failed", "key", key, "error", err)
			return rows, nil
		}
		if err := cache.store.Set(sharedCtx, key, string(payload), cache.ttl); err != nil {
			slog.WarnContext(sharedCtx, "agent metrics cache write failed", "key", key, "error", err)
		}
		return rows, nil
	})

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case loaded := <-result:
		if loaded.Err != nil {
			return nil, loaded.Err
		}
		return loaded.Val.([]T), nil
	}
}

func (h *Handler) getCachedWorkspaceAgentRunCountsWithProjectPermission(ctx context.Context, workspaceID, userID pgtype.UUID) ([]db.GetWorkspaceAgentRunCountsRow, error) {
	key := agentMetricsCacheKey("run_counts", uuidToString(workspaceID), uuidToString(userID))
	return loadAgentMetrics(ctx, h.AgentMetricsCache, key, func(loadCtx context.Context) ([]db.GetWorkspaceAgentRunCountsRow, error) {
		return h.getWorkspaceAgentRunCountsWithProjectPermission(loadCtx, workspaceID, userID)
	})
}

func (h *Handler) getCachedWorkspaceAgentActivityWithProjectPermission(ctx context.Context, workspaceID, userID pgtype.UUID) ([]db.GetWorkspaceAgentActivity30dRow, error) {
	key := agentMetricsCacheKey("activity_30d", uuidToString(workspaceID), uuidToString(userID))
	return loadAgentMetrics(ctx, h.AgentMetricsCache, key, func(loadCtx context.Context) ([]db.GetWorkspaceAgentActivity30dRow, error) {
		return h.getWorkspaceAgentActivityWithProjectPermission(loadCtx, workspaceID, userID)
	})
}
