package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

// 2026-10-10 coder(lq): Independently deny each task permission so tests cannot
// pass only because a Viewer happens to lack both Comment and AgentUse.
type upstreamSupplementPermissionResolver struct {
	projectauth.EffectiveAccessResolver
	denied projectauth.Permission
}

func (r upstreamSupplementPermissionResolver) CanIssue(_ context.Context, _ projectauth.Subject, _ string, permission projectauth.Permission) error {
	if permission == r.denied {
		return projectauth.ErrForbidden
	}
	return nil
}

func (r upstreamSupplementPermissionResolver) ExplainIssue(_ context.Context, _ projectauth.Subject, _ string, permission projectauth.Permission) (projectauth.PermissionExplanation, error) {
	return projectauth.PermissionExplanation{Allowed: permission != r.denied, Permission: permission}, nil
}

func TestUpstreamSupplementRequiresCommentAndAgentUse(t *testing.T) {
	f := newSupplementFixture(t, "codex", "running", true)
	enableProjectAuthForTest(t)
	for _, permission := range []projectauth.Permission{projectauth.IssueComment, projectauth.AgentUse} {
		t.Run(string(permission), func(t *testing.T) {
			h := *testHandler
			h.EffectiveIssueAccess = upstreamSupplementPermissionResolver{denied: permission}
			req := withURLParams(newRequest(http.MethodPost, "/supplements", map[string]any{
				"client_request_id": "0199a4e8-22ce-7b01-bba5-ffffffffffff", "content": "unauthorized additional input",
			}), "id", f.issueID, "taskId", f.taskID)
			testutil.Call(t, h.CreateTaskSupplement, req).Want(http.StatusForbidden)
			if n := dbfx.Count(t, `SELECT count(*) FROM task_supplement WHERE task_id=$1`, f.taskID); n != 0 {
				t.Fatalf("denied additional input created %d receipts", n)
			}
		})
	}
}

func TestUpstreamSteeringCannotBypassAgentUse(t *testing.T) {
	f := newSupplementFixture(t, "codex", "running", true)
	enableProjectAuthForTest(t)
	h := *testHandler
	h.EffectiveIssueAccess = upstreamSupplementPermissionResolver{denied: projectauth.AgentUse}
	issue, err := h.Queries.GetIssue(t.Context(), parseUUID(f.issueID))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := h.Queries.GetAgent(t.Context(), parseUUID(f.agentID))
	if err != nil {
		t.Fatal(err)
	}
	commentID := dbfx.Comment(t, f.issueID, "must not steer without AgentUse")
	comment, err := h.Queries.GetComment(t.Context(), parseUUID(commentID))
	if err != nil {
		t.Fatal(err)
	}
	triggers := []commentAgentTrigger{{Agent: agent}}
	kept, steered := h.steerCommentAgentTriggers(t.Context(), issue, comment, "member", triggers, []pgtype.UUID{parseUUID(f.taskID)})
	if len(kept) != 1 || len(steered) != 0 {
		t.Fatalf("unauthorized steering bypassed enqueue authorization: kept=%d steered=%d", len(kept), len(steered))
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM task_supplement WHERE task_id=$1`, f.taskID); n != 0 {
		t.Fatalf("unauthorized steering created %d receipts", n)
	}
}

func TestUpstreamArchivedIssueRejectsSupplement(t *testing.T) {
	f := newSupplementFixture(t, "codex", "running", true)
	dbfx.Exec(t, `UPDATE issue SET archived_at=now() WHERE id=$1`, f.issueID)
	supplementRequest(t, f, "0199a4e8-22ce-7b01-bba5-ffffffffffff", "do not execute archived work").Want(http.StatusConflict)
	if n := dbfx.Count(t, `SELECT count(*) FROM task_supplement WHERE task_id=$1`, f.taskID); n != 0 {
		t.Fatalf("archived issue accepted %d receipts", n)
	}
}

func TestUpstreamPRAutoCompletePreservesArchivedIssue(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	secret := "upstream-archive-pr-contract"
	t.Setenv("GITHUB_WEBHOOK_SECRET", secret)
	const inst int64 = 8758091
	issue := prAutoCompleteTestIssue(t, "archived PR target", inst)
	firePRWebhook(t, secret, inst, 1, "Closes "+issue.Identifier, "", "fix/archive", "opened")
	dbfx.Exec(t, `UPDATE issue SET archived_at=now() WHERE id=$1`, issue.ID)
	firePRWebhook(t, secret, inst, 1, "Closes "+issue.Identifier, "", "fix/archive", "merged")
	if got := issueStatusForTest(t, issue.ID); got != "in_progress" {
		t.Fatalf("merged PR changed archived issue to %s", got)
	}
	stored, err := testHandler.Queries.GetIssue(t.Context(), parseUUID(issue.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !stored.ArchivedAt.Valid {
		t.Fatal("PR automation restored archived issue")
	}
}

// 2026-10-10 coder(lq): The SQL boundary must also protect the decision/write
// window; a handler-only ArchivedAt check cannot guard concurrent archival.
func TestUpstreamPRAutoCompleteArchiveDuringDecision(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	secret := "upstream-archive-pr-race-contract"
	t.Setenv("GITHUB_WEBHOOK_SECRET", secret)
	const inst int64 = 8758092
	issue := prAutoCompleteTestIssue(t, "PR archive race", inst)
	firePRWebhook(t, secret, inst, 1, "Closes "+issue.Identifier, "", "fix/archive-race", "opened")
	original := testHandler.TxStarter
	t.Cleanup(func() { testHandler.TxStarter = original })
	testHandler.TxStarter = hookedTxStarter{base: original, hook: func() error {
		testHandler.TxStarter = original
		_, err := testPool.Exec(t.Context(), `UPDATE issue SET archived_at=now() WHERE id=$1`, issue.ID)
		return err
	}}
	firePRWebhook(t, secret, inst, 1, "Closes "+issue.Identifier, "", "fix/archive-race", "merged")
	if got := issueStatusForTest(t, issue.ID); got != "in_progress" {
		t.Fatalf("PR decision overwrote concurrent archive: status=%s", got)
	}
}
