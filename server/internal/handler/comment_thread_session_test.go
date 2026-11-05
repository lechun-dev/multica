package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestClaimTaskIssueSessionsStayWithinCommentThread(t *testing.T) {
	ctx := context.Background()
	agent, rt, daemon := createRuntimeGuardAgent(t, ctx)
	issue := dbfx.Issue(t, "thread session isolation")
	a, b := dbfx.Comment(t, issue, "A"), dbfx.Comment(t, issue, "B")
	prior := func(root, session, workdir string) {
		t.Helper()
		dbfx.Task(t, agent, testutil.Cols{
			"runtime_id": rt, "issue_id": issue, "trigger_comment_id": root,
			"status": "completed", "started_at": testutil.Raw("now()-interval '2 minutes'"),
			"completed_at": testutil.Raw("now()-interval '1 minute'"), "session_id": session, "work_dir": workdir,
		})
	}
	prior(a, "session-a", "/tmp/workdir-a")
	prior(b, "session-b", "/tmp/workdir-b")
	// An overflow/missing rollout in B cannot retire A's healthy context.
	dbfx.Task(t, agent, testutil.Cols{
		"runtime_id": rt, "issue_id": issue, "trigger_comment_id": b,
		"status": "failed", "started_at": testutil.Raw("now()"), "completed_at": testutil.Raw("now()"),
		"failure_reason": "codex_resume_oversized", "session_rollout_missing": true,
	})
	for _, tc := range []struct {
		name, root, session, workdir string
		missing                      bool
	}{
		{name: "reply_A", root: a, session: "session-a", workdir: "/tmp/workdir-a"},
		{name: "reply_B_overflow", root: b, missing: true},
		{name: "new_root", root: dbfx.Comment(t, issue, "C")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := dbfx.Comment(t, issue, "follow-up", testutil.Cols{"parent_id": tc.root})
			id := dbfx.Task(t, agent, testutil.Cols{"runtime_id": rt, "issue_id": issue, "trigger_comment_id": reply,
				"originator_user_id": testUserID, "accountable_user_id": testUserID})
			response := claimTaskForRuntimeGuard(t, rt, daemon)
			if response.TriggerThreadID != tc.root || response.PriorSessionID != tc.session || response.PriorWorkDir != tc.workdir || response.PriorSessionResumeUnavailable != tc.missing {
				t.Fatalf("wrong conversation context: %+v", response)
			}
			dbfx.Exec(t, "UPDATE agent_task_queue SET status='completed', completed_at=now() WHERE id=$1", id)
		})
	}
}

func TestIssueThreadSessionPoisonAndWakeupScope(t *testing.T) {
	ctx := context.Background()
	rt := dbfx.Runtime(t, "session runtime")
	agent := dbfx.Agent(t, "session agent", rt)
	issue := dbfx.Issue(t, "session safety")
	a, b := dbfx.Comment(t, issue, "A"), dbfx.Comment(t, issue, "B")
	row := func(root, session string, overrides testutil.Cols) string {
		t.Helper()
		return dbfx.Task(t, agent, testutil.Cols{"runtime_id": rt, "issue_id": issue, "trigger_comment_id": root,
			"session_id": session, "status": "completed", "completed_at": testutil.Raw("now()-interval '1 minute'")}, overrides)
	}
	row(a, "poison-a", nil)
	row(a, "poison-a", testutil.Cols{"status": "failed", "failure_reason": "api_invalid_request", "completed_at": testutil.Raw("now()")})
	row(b, "safe-b", nil)
	lookup := func(root string) (db.GetLastTaskSessionRow, error) {
		return testHandler.Queries.GetLastTaskSession(ctx, db.GetLastTaskSessionParams{AgentID: parseUUID(agent), IssueID: parseUUID(issue), CommentThreadID: parseUUID(root)})
	}
	if _, err := lookup(a); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("poisoned thread inherited another session: %v", err)
	}
	if got, err := lookup(b); err != nil || got.SessionID.String != "safe-b" {
		t.Fatalf("A poisoned B: %+v / %v", got, err)
	}
	// A wakeup's scheduling UUID is never a transcript key. Its real delivery
	// thread remains eligible for ordinary follow-ups on that thread.
	wakeup := row(a, "wakeup-a", testutil.Cols{"context": testutil.Raw(`'{"wakeup_id":"11111111-1111-4111-8111-111111111111"}'::jsonb`), "completed_at": testutil.Raw("now()")})
	if got, err := lookup(a); err != nil || got.SessionID.String != "wakeup-a" {
		t.Fatalf("wakeup lost its delivery thread: %+v / %v", got, err)
	}
	// Explicit session retirement still wins over any older healthy row.
	dbfx.Exec(t, "UPDATE agent_task_queue SET retired_session_id='wakeup-a', session_id=NULL WHERE id=$1", wakeup)
	if _, err := lookup(a); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("retired session resurrected: %v", err)
	}
}

func TestClaimSessionThreadIDRetainsDeletedTriggerScope(t *testing.T) {
	root := parseUUID("11111111-1111-4111-8111-111111111111")
	task := db.AgentTaskQueue{CommentThreadID: root}
	if got := claimSessionThreadID(task, ""); got != root {
		t.Fatal("missing trigger body erased the persisted conversation scope")
	}
	task.Context = []byte(`{"wakeup_id":"22222222-2222-4222-8222-222222222222"}`)
	if got := claimSessionThreadID(task, ""); got.Valid {
		t.Fatal("wakeup scheduling ID leaked into the transcript scope")
	}
}
