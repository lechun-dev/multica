package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	dbfx "github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// batchClaimFixture provisions two runtimes on one machine, each with its own
// agent (max_concurrent_tasks=5): agent1 (on rt1) has two queued tasks on two
// different issues; agent2 (on rt2) has one queued task. Returns the two
// runtime IDs.
func batchClaimFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (rt1, rt2 string) {
	t.Helper()
	suffix := time.Now().UnixNano()

	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ($1,$2) RETURNING id`,
		"Batch Claim Test", fmt.Sprintf("batch-claim-%d@multica.ai", suffix)).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	var workspaceID string
	if err := pool.QueryRow(ctx, `INSERT INTO workspace (name, slug, description, issue_prefix) VALUES ($1,$2,$3,$4) RETURNING id`,
		"Batch Claim Test", fmt.Sprintf("batch-claim-%d", suffix), "temp batch claim test workspace", "BCR").Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1,$2,'owner')`, workspaceID, userID); err != nil {
		t.Fatalf("create member: %v", err)
	}

	mkRuntime := func(name, provider string) string {
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, visibility, owner_id)
			VALUES ($1, 'daemon-batch', $2, 'cloud', $3, 'online', 'test runtime', '{}'::jsonb, now(), 'private', $4)
			RETURNING id`, workspaceID, name, provider, userID).Scan(&id); err != nil {
			t.Fatalf("create runtime %s: %v", name, err)
		}
		return id
	}
	mkAgent := func(name, runtimeID string) string {
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_config, runtime_id, visibility, max_concurrent_tasks, owner_id)
			VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 5, $4)
			RETURNING id`, workspaceID, name, runtimeID, userID).Scan(&id); err != nil {
			t.Fatalf("create agent %s: %v", name, err)
		}
		return id
	}

	rt1 = mkRuntime("Batch RT1", "batch_provider_1")
	rt2 = mkRuntime("Batch RT2", "batch_provider_2")
	agent1 := mkAgent("Batch Agent1", rt1)
	agent2 := mkAgent("Batch Agent2", rt2)

	mkQueuedTask := func(agentID, runtimeID string, n int) {
		var issueID string
		if err := pool.QueryRow(ctx, `
			INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, position)
			VALUES ($1, $2, 'in_progress', 'none', $3, 'member', $4, $5)
			RETURNING id`, workspaceID, fmt.Sprintf("batch issue %d", n), userID, 800000+n, n).Scan(&issueID); err != nil {
			t.Fatalf("create issue %d: %v", n, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO agent_task_queue (agent_id, issue_id, status, priority, context, runtime_id)
			VALUES ($1, $2, 'queued', 0, '{}'::jsonb, $3)`, agentID, issueID, runtimeID); err != nil {
			t.Fatalf("create task %d: %v", n, err)
		}
		// Space out created_at so ordering is deterministic.
		time.Sleep(2 * time.Millisecond)
	}
	mkQueuedTask(agent1, rt1, 1)
	mkQueuedTask(agent1, rt1, 2)
	mkQueuedTask(agent2, rt2, 3)

	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM agent_task_queue WHERE agent_id IN ($1,$2)`, agent1, agent2)
		pool.Exec(c, `DELETE FROM issue WHERE workspace_id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM agent WHERE id IN ($1,$2)`, agent1, agent2)
		pool.Exec(c, `DELETE FROM agent_runtime WHERE id IN ($1,$2)`, rt1, rt2)
		pool.Exec(c, `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, workspaceID, userID)
		pool.Exec(c, `DELETE FROM workspace WHERE id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM "user" WHERE id = $1`, userID)
	})
	return rt1, rt2
}

// MultiRuntimeDrain fills capacity in rounds rather than stopping after one
// claim per agent. Candidate order still gives each runtime a turn first.
func TestClaimTasksForRuntimes_MultiRuntimeDrain(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	svc := NewTaskService(db.New(pool), pool, nil, events.New())
	rt1, rt2 := batchClaimFixture(t, ctx, pool)
	ids := []pgtype.UUID{util.MustParseUUID(rt1), util.MustParseUUID(rt2)}
	got, err := svc.ClaimTasksForRuntimes(ctx, ids, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("claimed %d tasks, want 3", len(got))
	}
	for i, want := range []string{rt1, rt2, rt1} {
		if actual := util.UUIDToString(got[i].RuntimeID); actual != want {
			t.Fatalf("task %d runtime = %s, want %s", i, actual, want)
		}
	}
	got, err = svc.ClaimTasksForRuntimes(ctx, ids, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("drained batch returned %d tasks", len(got))
	}
}

func TestClaimTasksForRuntimes_AgentCapacityAndRefill(t *testing.T) {
	for _, capacity := range []int{1, 10} {
		t.Run(fmt.Sprint(capacity), func(t *testing.T) {
			ctx := context.Background()
			pool := newTaskClaimRacePool(t)
			rt1, _ := batchClaimFixture(t, ctx, pool)
			var agentID, workspaceID, ownerID string
			if err := pool.QueryRow(ctx, `UPDATE agent SET max_concurrent_tasks=$2 WHERE runtime_id=$1 RETURNING id, workspace_id, owner_id`, rt1, capacity).Scan(&agentID, &workspaceID, &ownerID); err != nil {
				t.Fatal(err)
			}
			fx := dbfx.New(pool, workspaceID, ownerID)
			// Fixture already has two tasks; add ten independent issues.
			for i := range 10 {
				issueID := fx.Issue(t, fmt.Sprintf("capacity issue %d", i))
				fx.Task(t, agentID, dbfx.Cols{"issue_id": issueID, "runtime_id": rt1})
			}
			svc := NewTaskService(db.New(pool), pool, nil, events.New())
			ids := []pgtype.UUID{util.MustParseUUID(rt1)}
			got, err := svc.ClaimTasksForRuntimes(ctx, ids, 20)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != capacity {
				t.Fatalf("claimed %d, want capacity %d", len(got), capacity)
			}
			blocked, err := svc.ClaimTasksForRuntimes(ctx, ids, 20)
			if err != nil {
				t.Fatal(err)
			}
			if len(blocked) != 0 {
				t.Fatalf("full agent claimed %d more tasks", len(blocked))
			}
			fx.Exec(t, `UPDATE agent_task_queue SET status='completed' WHERE id=$1`, util.UUIDToString(got[0].ID))
			refilled, err := svc.ClaimTasksForRuntimes(ctx, ids, 20)
			if err != nil {
				t.Fatal(err)
			}
			if len(refilled) != 1 {
				t.Fatalf("released slot claimed %d tasks, want 1", len(refilled))
			}
		})
	}
}

// TestClaimTasksForRuntimes_MaxTasksCap verifies max_tasks bounds the number of
// tasks returned in a single batch call.
func TestClaimTasksForRuntimes_MaxTasksCap(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	svc := NewTaskService(db.New(pool), pool, nil, events.New())

	rt1, rt2 := batchClaimFixture(t, ctx, pool)
	ids := []pgtype.UUID{util.MustParseUUID(rt1), util.MustParseUUID(rt2)}

	got, err := svc.ClaimTasksForRuntimes(ctx, ids, 1)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("claimed %d tasks with max_tasks=1, want 1", len(got))
	}
}

// TestClaimTasksForRuntimes_AnyDueRuntimeChecksTheCompleteSet pins MUL-6486's
// collection-level reclaim gate. Per-runtime backstops can begin staggered, but
// one due member must run the single UPDATE over every runtime on that daemon;
// filtering the SQL array to only the due member both misses recoverable work
// and lets fixed query overhead drift back toward one call per poll.
func TestClaimTasksForRuntimes_AnyDueRuntimeChecksTheCompleteSet(t *testing.T) {
	ctx := context.Background()
	rdb := newRedisTestClient(t)
	pool := newTaskClaimRacePool(t)
	svc := NewTaskService(db.New(pool), pool, nil, events.New())
	svc.ReclaimCheck = NewReclaimCheckCache(rdb)

	rt1, rt2 := batchClaimFixture(t, ctx, pool)
	ids := []pgtype.UUID{util.MustParseUUID(rt1), util.MustParseUUID(rt2)}

	var staleTaskID string
	if err := pool.QueryRow(ctx, `
		SELECT id FROM agent_task_queue
		WHERE runtime_id = $1
		ORDER BY created_at
		LIMIT 1
	`, rt2).Scan(&staleTaskID); err != nil {
		t.Fatalf("load rt2 task: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'dispatched', dispatched_at = now() - interval '10 minutes',
		    started_at = NULL, prepare_lease_expires_at = NULL
		WHERE id = $1
	`, staleTaskID); err != nil {
		t.Fatalf("make rt2 task stale: %v", err)
	}

	now := time.Now()
	svc.ReclaimCheck.MarkChecked(
		ctx,
		[]string{rt1},
		now.Add(-ReclaimCheckBackstopInterval-time.Second),
		now.Add(ReclaimCheckRetryInterval),
	)
	svc.ReclaimCheck.MarkChecked(ctx, []string{rt2}, now, now.Add(ReclaimCheckRetryInterval))
	due := svc.ReclaimCheck.DueRuntimeIDs(ctx, []string{rt1, rt2}, now)
	if len(due) != 1 || due[0] != rt1 {
		t.Fatalf("precondition due runtimes = %v, want [%s]", due, rt1)
	}

	claimed, err := svc.ClaimTasksForRuntimes(ctx, ids, 5)
	if err != nil {
		t.Fatalf("batch claim: %v", err)
	}
	for _, task := range claimed {
		if util.UUIDToString(task.ID) == staleTaskID {
			return
		}
	}
	t.Fatalf("batch claim omitted stale task %s on non-due runtime %s: got %v", staleTaskID, rt2, claimed)
}

// TestClaimTasksForRuntimes_EmptyReclaimDefersHintForRetry covers the recovery
// hole where the task hint was previously made inert after one empty UPDATE.
// A temporarily stale runtime heartbeat is intentionally correlated with a
// lost claim response: the task remains dispatched, and its hint must become
// due again after the short retry rather than waiting for the 90-second
// backstop.
func TestClaimTasksForRuntimes_EmptyReclaimDefersHintForRetry(t *testing.T) {
	ctx := context.Background()
	rdb := newRedisTestClient(t)
	pool := newTaskClaimRacePool(t)
	svc := NewTaskService(db.New(pool), pool, nil, events.New())
	svc.ReclaimCheck = NewReclaimCheckCache(rdb)

	rt1, _ := batchClaimFixture(t, ctx, pool)
	runtimeID := util.MustParseUUID(rt1)
	var taskID string
	if err := pool.QueryRow(ctx, `
		SELECT id FROM agent_task_queue
		WHERE runtime_id = $1
		ORDER BY created_at
		LIMIT 1
	`, rt1).Scan(&taskID); err != nil {
		t.Fatalf("load task: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'dispatched', dispatched_at = now() - interval '10 minutes',
		    started_at = NULL, prepare_lease_expires_at = NULL
		WHERE id = $1
	`, taskID); err != nil {
		t.Fatalf("make task stale: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE agent_runtime
		SET status = 'online', last_seen_at = now() - interval '10 minutes'
		WHERE id = $1
	`, rt1); err != nil {
		t.Fatalf("make runtime temporarily ineligible: %v", err)
	}

	now := time.Now()
	svc.ReclaimCheck.MarkChecked(ctx, []string{rt1}, now, now.Add(ReclaimCheckRetryInterval))
	svc.ReclaimCheck.Track(ctx, rt1, taskID, now.Add(-time.Second))
	if due := svc.ReclaimCheck.DueRuntimeIDs(ctx, []string{rt1}, now); len(due) != 1 {
		t.Fatalf("precondition due runtimes = %v, want [%s]", due, rt1)
	}

	if claimed, err := svc.ClaimTasksForRuntimes(ctx, []pgtype.UUID{runtimeID}, 5); err != nil {
		t.Fatalf("batch claim: %v", err)
	} else if len(claimed) != 0 {
		t.Fatalf("claimed tasks with stale runtime heartbeat: %v", claimed)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status); err != nil {
		t.Fatalf("read task after empty reclaim: %v", err)
	}
	if status != "dispatched" {
		t.Fatalf("task status after empty reclaim = %q, want dispatched", status)
	}

	score, err := rdb.ZScore(ctx, reclaimCheckScheduleKey(rt1), taskID).Result()
	if err != nil {
		t.Fatalf("read deferred hint: %v", err)
	}
	retryAt := time.UnixMilli(int64(score))
	if retryAt.Before(now.Add(ReclaimCheckRetryInterval)) || retryAt.After(time.Now().Add(ReclaimCheckRetryInterval+time.Second)) {
		t.Fatalf("retry hint = %v, want approximately one retry interval after the check", retryAt)
	}
	if due := svc.ReclaimCheck.DueRuntimeIDs(ctx, []string{rt1}, retryAt.Add(-time.Millisecond)); len(due) != 0 {
		t.Fatalf("hint retriggered before retry deadline: %v", due)
	}
	if due := svc.ReclaimCheck.DueRuntimeIDs(ctx, []string{rt1}, retryAt.Add(time.Millisecond)); len(due) != 1 {
		t.Fatalf("hint did not retrigger after empty reclaim: %v", due)
	}
}

// TestClaimTasksForRuntimes_EmptyInputs guards the trivial short-circuits.
func TestClaimTasksForRuntimes_EmptyInputs(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	svc := NewTaskService(db.New(pool), pool, nil, events.New())

	if got, err := svc.ClaimTasksForRuntimes(ctx, nil, 5); err != nil || got != nil {
		t.Fatalf("nil runtimes: got=%v err=%v, want nil,nil", got, err)
	}
	rt1, _ := batchClaimFixture(t, ctx, pool)
	if got, err := svc.ClaimTasksForRuntimes(ctx, []pgtype.UUID{util.MustParseUUID(rt1)}, 0); err != nil || got != nil {
		t.Fatalf("maxTasks=0: got=%v err=%v, want nil,nil", got, err)
	}
}
