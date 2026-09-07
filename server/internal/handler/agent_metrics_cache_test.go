package handler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeAgentMetricsCacheStore struct {
	mu      sync.Mutex
	values  map[string]string
	getErr  error
	setErr  error
	lastTTL time.Duration
}

func newFakeAgentMetricsCacheStore() *fakeAgentMetricsCacheStore {
	return &fakeAgentMetricsCacheStore{values: make(map[string]string)}
}

func (s *fakeAgentMetricsCacheStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return "", s.getErr
	}
	value, ok := s.values[key]
	if !ok {
		return "", redis.Nil
	}
	return value, nil
}

func (s *fakeAgentMetricsCacheStore) Set(_ context.Context, key, value string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setErr != nil {
		return s.setErr
	}
	s.values[key] = value
	s.lastTTL = ttl
	return nil
}

type testAgentMetricRow struct {
	AgentID string `json:"agent_id"`
	Count   int32  `json:"count"`
}

func TestLoadAgentMetricsCacheHitSkipsLoader(t *testing.T) {
	store := newFakeAgentMetricsCacheStore()
	store.values["cached"] = `[{"agent_id":"agent-1","count":7}]`
	cache := newAgentMetricsCache(store, agentMetricsCacheTTL)

	rows, err := loadAgentMetrics(context.Background(), cache, "cached", func(context.Context) ([]testAgentMetricRow, error) {
		t.Fatal("loader must not run on a cache hit")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("load cached metrics: %v", err)
	}
	if len(rows) != 1 || rows[0].AgentID != "agent-1" || rows[0].Count != 7 {
		t.Fatalf("unexpected cached rows: %#v", rows)
	}
}

func TestLoadAgentMetricsCacheKeysIsolateWorkspaceAndUser(t *testing.T) {
	store := newFakeAgentMetricsCacheStore()
	cache := newAgentMetricsCache(store, agentMetricsCacheTTL)
	ctx := context.Background()

	keyA := agentMetricsCacheKey("run_counts", "workspace-1", "user-1")
	keyB := agentMetricsCacheKey("run_counts", "workspace-1", "user-2")
	keyC := agentMetricsCacheKey("run_counts", "workspace-2", "user-1")
	activityKey := agentMetricsCacheKey("activity_30d", "workspace-1", "user-1")
	if keyA == keyB || keyA == keyC || keyA == activityKey {
		t.Fatal("cache keys must differ by user, workspace, and metric")
	}
	for key, count := range map[string]int32{keyA: 3, keyB: 9, keyC: 12, activityKey: 15} {
		rows, err := loadAgentMetrics(ctx, cache, key, func(context.Context) ([]testAgentMetricRow, error) {
			return []testAgentMetricRow{{AgentID: "agent-1", Count: count}}, nil
		})
		if err != nil || len(rows) != 1 || rows[0].Count != count {
			t.Fatalf("load %s: rows=%#v err=%v", key, rows, err)
		}
	}

	rows, err := loadAgentMetrics(ctx, cache, keyA, func(context.Context) ([]testAgentMetricRow, error) {
		t.Fatal("workspace/user-specific entry should be cached")
		return nil, nil
	})
	if err != nil || len(rows) != 1 || rows[0].Count != 3 {
		t.Fatalf("reload key A: rows=%#v err=%v", rows, err)
	}
	if store.lastTTL != agentMetricsCacheTTL {
		t.Fatalf("cache TTL = %v, want %v", store.lastTTL, agentMetricsCacheTTL)
	}
}

func TestLoadAgentMetricsRedisErrorsFallBackToLoader(t *testing.T) {
	store := newFakeAgentMetricsCacheStore()
	store.getErr = errors.New("redis unavailable")
	store.setErr = errors.New("redis unavailable")
	cache := newAgentMetricsCache(store, agentMetricsCacheTTL)

	var loads atomic.Int32
	rows, err := loadAgentMetrics(context.Background(), cache, "fallback", func(context.Context) ([]testAgentMetricRow, error) {
		loads.Add(1)
		return []testAgentMetricRow{{AgentID: "agent-1", Count: 5}}, nil
	})
	if err != nil {
		t.Fatalf("cache failure must not fail request: %v", err)
	}
	if loads.Load() != 1 || len(rows) != 1 || rows[0].Count != 5 {
		t.Fatalf("unexpected fallback result: loads=%d rows=%#v", loads.Load(), rows)
	}
}

func TestLoadAgentMetricsCollapsesConcurrentMisses(t *testing.T) {
	store := newFakeAgentMetricsCacheStore()
	cache := newAgentMetricsCache(store, agentMetricsCacheTTL)
	const callers = 20

	start := make(chan struct{})
	release := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(callers)
	var loads atomic.Int32
	results := make(chan error, callers)

	for range callers {
		go func() {
			ready.Done()
			<-start
			rows, err := loadAgentMetrics(context.Background(), cache, "same-key", func(context.Context) ([]testAgentMetricRow, error) {
				loads.Add(1)
				<-release
				return []testAgentMetricRow{{AgentID: "agent-1", Count: 11}}, nil
			})
			if err == nil && (len(rows) != 1 || rows[0].Count != 11) {
				err = errors.New("unexpected rows")
			}
			results <- err
		}()
	}
	ready.Wait()
	close(start)

	deadline := time.Now().Add(time.Second)
	for loads.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	for range callers {
		if err := <-results; err != nil {
			t.Fatalf("concurrent load: %v", err)
		}
	}
	if loads.Load() != 1 {
		t.Fatalf("loader ran %d times, want 1", loads.Load())
	}
}

func TestLoadAgentMetricsRoundTripsDatabaseRows(t *testing.T) {
	store := newFakeAgentMetricsCacheStore()
	cache := newAgentMetricsCache(store, agentMetricsCacheTTL)
	key := agentMetricsCacheKey("activity_30d", "workspace-1", "user-1")
	want := []db.GetWorkspaceAgentActivity30dRow{{
		AgentID:     parseUUID("00000000-0000-0000-0000-000000000001"),
		Bucket:      pgtype.Timestamptz{Time: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), Valid: true},
		TaskCount:   4,
		FailedCount: 1,
	}}

	if _, err := loadAgentMetrics(context.Background(), cache, key, func(context.Context) ([]db.GetWorkspaceAgentActivity30dRow, error) {
		return want, nil
	}); err != nil {
		t.Fatalf("populate cache: %v", err)
	}
	got, err := loadAgentMetrics(context.Background(), cache, key, func(context.Context) ([]db.GetWorkspaceAgentActivity30dRow, error) {
		t.Fatal("loader must not run after database rows are cached")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	if len(got) != 1 || got[0].AgentID != want[0].AgentID || !got[0].Bucket.Time.Equal(want[0].Bucket.Time) || got[0].TaskCount != 4 || got[0].FailedCount != 1 {
		t.Fatalf("database row did not round-trip: %#v", got)
	}
}
