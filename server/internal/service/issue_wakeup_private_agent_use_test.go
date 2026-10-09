package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 2026-10-10 coder(lq): Timer dispatch must not bypass the private enqueue
// authorizer, even though the final daemon claim checks task permission again.
func TestWakeupDispatchReusesPrivateAgentUseAuthorizer(t *testing.T) {
	f, s, issue, agent := wakeFixture(t)
	w := wakeCreate(t, f, s, issue, WakeupInput{AgentID: agent, Kind: "every", IntervalSeconds: 3600, Instruction: "Check"})
	denied := errors.New("task AgentUse revoked")
	calls := 0
	s.Tasks.IssueAgentUseAuthorizer = func(_ context.Context, target db.Issue, principal pgtype.UUID, phase string) error {
		calls++
		if target.ID != issue || util.UUIDToString(principal) != f.UserID || phase != "enqueue" {
			t.Fatalf("wrong enqueue authority issue=%s principal=%s phase=%s", util.UUIDToString(target.ID), util.UUIDToString(principal), phase)
		}
		return denied
	}
	err := s.Trigger(context.Background(), issue, w.ID, parseTestUUID(t, f.UserID))
	// The trigger/event is accepted durably; immediate dispatch is best effort.
	// Permission rejection must preserve that input without producing a run.
	if err != nil || calls != 1 {
		t.Fatalf("dispatch authorizer calls=%d error=%v", calls, err)
	}
	if n := wakeRuns(t, f, w.ID); n != 0 {
		t.Fatalf("denied enqueue created %d runs", n)
	}
}

// 2026-10-10 coder(lq): Child-done is a system event but its run still uses
// the parent's documented human attribution through the central task gate.
func TestChildDoneDispatchReusesPrivateAgentUseAuthorizer(t *testing.T) {
	f, s, issue, agent := conditionFixture(t)
	ctx := context.Background()
	f.Exec(t, "UPDATE issue SET status='in_progress',assignee_type='agent',assignee_id=$2 WHERE id=$1", issue, agent)
	child := f.Issue(t, "Child for denied wakeup", testutil.Cols{"parent_issue_id": issue, "status": "in_progress"})
	f.Cleanup(t, "DELETE FROM issue_child_event WHERE parent_id=$1", issue)
	if err := s.ProcessChildEvents(ctx, issue); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("task AgentUse revoked")
	calls := 0
	s.Tasks.IssueAgentUseAuthorizer = func(_ context.Context, target db.Issue, principal pgtype.UUID, phase string) error {
		calls++
		if target.ID != issue || util.UUIDToString(principal) != f.UserID || phase != "enqueue" {
			t.Fatalf("wrong system authority issue=%s principal=%s phase=%s", util.UUIDToString(target.ID), util.UUIDToString(principal), phase)
		}
		return denied
	}
	f.Exec(t, "UPDATE issue SET status='done' WHERE id=$1", child)
	err := s.ProcessChildEvents(ctx, issue)
	// The trigger/event is accepted durably; immediate dispatch is best effort.
	// Permission rejection must preserve that input without producing a run.
	if err != nil || calls != 1 {
		t.Fatalf("system authorizer calls=%d error=%v", calls, err)
	}
	if n := f.Count(t, "SELECT count(*) FROM agent_task_queue WHERE issue_id=$1", issue); n != 0 {
		t.Fatalf("denied system enqueue created %d runs", n)
	}
}
