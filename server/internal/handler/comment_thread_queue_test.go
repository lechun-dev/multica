package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestCommentThreadsQueueSeparatelyAndExecuteSerially(t *testing.T) {
	ctx := context.Background()
	runtimeID := dbfx.Runtime(t, "thread queue runtime", testutil.Cols{"metadata": testutil.Raw(`'{"capabilities":["issue-thread-concurrency-v1"]}'::jsonb`)})
	agentID := dbfx.Agent(t, "thread queue agent", runtimeID, testutil.Cols{"max_concurrent_tasks": 2})
	issueID := dbfx.Issue(t, "thread queue issue", testutil.Cols{"assignee_type": "agent", "assignee_id": agentID})
	rootA := dbfx.Comment(t, issueID, "thread A")
	replyA := dbfx.Comment(t, issueID, "A supplement", testutil.Cols{"parent_id": rootA})
	nestedA := dbfx.Comment(t, issueID, "A nested supplement", testutil.Cols{"parent_id": replyA})
	t.Cleanup(func() { dbfx.Exec(t, "DELETE FROM agent_task_queue WHERE issue_id = $1", issueID) })
	issue, err := testHandler.Queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := testHandler.Queries.GetAgent(ctx, parseUUID(agentID))
	if err != nil {
		t.Fatal(err)
	}
	enqueue := func(commentID string, pending bool, want DispatchStatus) {
		t.Helper()
		result := testHandler.enqueueCommentAgentTriggers(ctx, issue, parseUUID(commentID),
			[]commentAgentTrigger{{Agent: agent, Source: commentTriggerSourceMentionAgent, AlreadyPending: pending}})
		if result[agentID].status != want {
			t.Fatalf("enqueue %s: got %+v, want %s", commentID, result[agentID], want)
		}
	}
	enqueue(rootA, false, DispatchQueued)
	enqueue(replyA, true, DispatchCoalesced)
	enqueue(nestedA, true, DispatchCoalesced)
	claim := db.ClaimAgentTaskParams{AgentID: parseUUID(agentID), RuntimeID: parseUUID(runtimeID), PrepareLeaseSecs: 60, RuntimeStaleSecs: 120}
	a, err := testHandler.Queries.ClaimAgentTask(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if uuidToString(a.CommentThreadID) != rootA || uuidToString(a.TriggerCommentID) != nestedA || len(a.CoalescedCommentIds) != 2 {
		t.Fatalf("wrong input batch for thread A: %+v", a)
	}
	dbfx.Exec(t, "UPDATE agent_task_queue SET status='running', started_at=now(), delivered_comment_ids=ARRAY[$2::uuid,$3::uuid,$4::uuid] WHERE id=$1", uuidToString(a.ID), rootA, replyA, nestedA)

	// Post a second top-level @mention while the first execution is running.
	var rootB CommentResponse
	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{
		"content": fmt.Sprintf("[@Agent](mention://agent/%s) thread B", agentID),
	}), "id", issueID)
	testutil.Call(t, testHandler.CreateComment, req).Want(http.StatusCreated).JSON(&rootB)
	if outcome := findCommentOutcome(t, rootB.TriggerOutcomes, agentID); outcome.Status != DispatchQueued {
		t.Fatalf("second mention should enqueue a separate run: %+v", outcome)
	}
	tasks, err := testHandler.Queries.ListTasksByIssue(ctx, parseUUID(issueID))
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d runs, want one per thread", len(tasks))
	}
	var b db.AgentTaskQueue
	for _, task := range tasks {
		if uuidToString(task.CommentThreadID) == rootB.ID {
			b = task
		}
	}
	if !b.ID.Valid || uuidToString(b.TriggerCommentID) != rootB.ID || len(b.CoalescedCommentIds) != 0 {
		t.Fatalf("wrong input batch for thread B: %+v", b)
	}
	followupA := dbfx.Comment(t, issueID, "A follow-up", testutil.Cols{"parent_id": rootA})
	enqueue(followupA, false, DispatchQueued)
	if _, err := testHandler.Queries.ClaimAgentTask(ctx, claim); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("both same-issue successors must wait for A: %v", err)
	}
	dbfx.Exec(t, "UPDATE agent_task_queue SET status='completed', completed_at=now() WHERE id=$1", uuidToString(a.ID))
	completed, err := testHandler.Queries.GetAgentTask(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	testHandler.reconcileCommentsOnCompletion(ctx, &completed)
	tasks, err = testHandler.Queries.ListTasksByIssue(ctx, parseUUID(issueID))
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("completion duplicated another thread: got %d tasks", len(tasks))
	}
	if other, err := testHandler.Queries.ClaimAgentTask(ctx, claim); err != nil || other.ID != b.ID {
		t.Fatalf("thread B should be next after A completes: %s / %v", uuidToString(other.ID), err)
	}
	// A row-specific rerun replaces only A's pending successor.
	rerun, err := testHandler.TaskService.RerunIssue(ctx, issue.ID, a.ID, pgtype.UUID{}, parseUUID(testUserID), nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := testHandler.Queries.GetAgentTask(ctx, b.ID)
	if err != nil || other.Status != "dispatched" {
		t.Fatalf("rerun disturbed thread B: %s / %v", other.Status, err)
	}
	if rerun.CommentThreadID != a.CommentThreadID {
		t.Fatal("rerun lost thread scope")
	}
	if _, err := testHandler.TaskService.CancelTask(ctx, rerun.ID); err != nil {
		t.Fatal(err)
	}
	other, err = testHandler.Queries.GetAgentTask(ctx, b.ID)
	if err != nil || other.Status != "dispatched" {
		t.Fatalf("stop disturbed thread B: %s / %v", other.Status, err)
	}
}

func TestIssueClaimsShareConfiguredAgentCapacity(t *testing.T) {
	for _, capacity := range []int{1, 2, 6} {
		t.Run(fmt.Sprintf("capacity_%d", capacity), func(t *testing.T) {
			ctx := context.Background()
			rt := dbfx.Runtime(t, "parallel runtime", testutil.Cols{"metadata": testutil.Raw(`'{"capabilities":["issue-thread-concurrency-v1"]}'::jsonb`)})
			agent := dbfx.Agent(t, "parallel agent", rt, testutil.Cols{"max_concurrent_tasks": capacity})
			for n := range capacity + 1 {
				issue := dbfx.Issue(t, fmt.Sprintf("parallel issue %d", n))
				root := dbfx.Comment(t, issue, fmt.Sprintf("independent instruction %d", n))
				dbfx.Task(t, agent, testutil.Cols{"runtime_id": rt, "issue_id": issue, "trigger_comment_id": root})
				if n == 0 {
					second := dbfx.Comment(t, issue, "second thread in first issue")
					dbfx.Task(t, agent, testutil.Cols{"runtime_id": rt, "issue_id": issue, "trigger_comment_id": second})
				}
			}
			var wg sync.WaitGroup
			start := make(chan struct{})
			errs := make(chan error, capacity+1)
			for range capacity + 1 {
				wg.Go(func() {
					<-start
					_, err := testHandler.TaskService.ClaimTask(ctx, parseUUID(agent))
					errs <- err
				})
			}
			close(start)
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			if got := dbfx.Count(t, "SELECT count(*) FROM agent_task_queue WHERE agent_id=$1 AND status='dispatched'", agent); got != capacity {
				t.Fatalf("active runs = %d, configured capacity = %d", got, capacity)
			}
			if got := dbfx.Count(t, "SELECT count(DISTINCT issue_id) FROM agent_task_queue WHERE agent_id=$1 AND status='dispatched'", agent); got != capacity {
				t.Fatalf("active runs should span %d distinct issues, got %d", capacity, got)
			}
			// Free one slot; exactly one queued issue may refill it.
			dbfx.Exec(t, "UPDATE agent_task_queue SET status='completed', completed_at=now() WHERE id=(SELECT id FROM agent_task_queue WHERE agent_id=$1 AND status='dispatched' LIMIT 1)", agent)
			if task, err := testHandler.TaskService.ClaimTask(ctx, parseUUID(agent)); err != nil || task == nil {
				t.Fatalf("released capacity did not refill: %v", err)
			}
			if task, err := testHandler.TaskService.ClaimTask(ctx, parseUUID(agent)); err != nil || task != nil {
				t.Fatalf("claim exceeded configured capacity: task=%v err=%v", task, err)
			}
		})
	}
}

func TestIssueClaimsSkipBlockedQueueHead(t *testing.T) {
	for _, status := range []string{"dispatched", "running", "waiting_local_directory"} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			rt := dbfx.Runtime(t, "fenced runtime", testutil.Cols{"metadata": testutil.Raw(`'{"capabilities":["issue-thread-concurrency-v1"]}'::jsonb`)})
			agent := dbfx.Agent(t, "fenced agent", rt, testutil.Cols{"max_concurrent_tasks": 6})
			issue := dbfx.Issue(t, "fenced issue")
			a, b := dbfx.Comment(t, issue, "A"), dbfx.Comment(t, issue, "B")
			dbfx.Task(t, agent, testutil.Cols{"runtime_id": rt, "issue_id": issue, "status": status, "trigger_comment_id": a})
			blocked := dbfx.Task(t, agent, testutil.Cols{"runtime_id": rt, "issue_id": issue, "trigger_comment_id": b})
			otherIssue := dbfx.Issue(t, "unrelated issue")
			other := dbfx.Task(t, agent, testutil.Cols{"runtime_id": rt, "issue_id": otherIssue})
			if task, err := testHandler.TaskService.ClaimTask(ctx, parseUUID(agent)); err != nil || task == nil || uuidToString(task.ID) != other {
				t.Fatalf("expected other issue %s to bypass blocked run %s: task=%v err=%v", other, blocked, task, err)
			}
			if task, err := testHandler.TaskService.ClaimTask(ctx, parseUUID(agent)); err != nil || task != nil {
				t.Fatalf("issue fence bypassed: task=%v err=%v", task, err)
			}
		})
	}
}

func TestDeferredCommentThreadsPromoteIndependently(t *testing.T) {
	ctx := context.Background()
	runtimeID := dbfx.Runtime(t, "thread promotion runtime")
	agentID := dbfx.Agent(t, "thread promotion agent", runtimeID)
	issueID := dbfx.Issue(t, "thread promotion issue")
	rootA := dbfx.Comment(t, issueID, "A")
	rootB := dbfx.Comment(t, issueID, "B")
	create := func(root string) string {
		return dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "issue_id": issueID, "trigger_comment_id": root, "status": "deferred", "fire_at": testutil.Raw("now()-interval '1 minute'")})
	}
	a, b := create(rootA), create(rootB)
	anotherA := create(rootA)
	promoted, err := testHandler.Queries.PromoteDueDeferredTasksForRuntime(ctx, db.PromoteDueDeferredTasksForRuntimeParams{RuntimeID: parseUUID(runtimeID), RuntimeStaleSecs: 120})
	if err != nil {
		t.Fatal(err)
	}
	if len(promoted) != 2 {
		t.Fatalf("promoted %d runs, want one per thread", len(promoted))
	}
	for _, id := range []string{a, b} {
		row, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(id))
		if err != nil || row.Status != "queued" {
			t.Fatalf("thread did not promote: %s / %v", row.Status, err)
		}
	}
	row, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(anotherA))
	if err != nil || row.Status != "deferred" {
		t.Fatalf("second run in A must stay deferred: %s / %v", row.Status, err)
	}
}
