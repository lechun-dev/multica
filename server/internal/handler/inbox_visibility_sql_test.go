package handler

import (
	"github.com/jackc/pgx/v5/pgtype"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

// 2026-10-09 coder(lq): The narrowed summary must retain newest-notification
// semantics across workspaces and immediately reflect task-grant revocation.
func TestUnreadInboxScopedVisibilityKeepsLatestNotification(t *testing.T) {
	t.Setenv("PROJECT_OWNER_BYPASS_ENABLED", "false")
	reader := dbfx.User(t, "Scoped inbox reader", "scoped-inbox-reader@example.test")
	h := *testHandler
	h.ProjectAuth = projectauth.New(newProjectAuthRepository(testPool), true)
	for _, suffix := range []string{"a", "b"} {
		ws := dbfx.Workspace(t, "Scoped inbox "+suffix, "scoped-inbox-"+suffix)
		fx := testutil.New(testPool, ws, testUserID)
		fx.Member(t, ws, reader, "member")
		parent := fx.Issue(t, "Directly shared parent")
		child := fx.Issue(t, "Inherited child", testutil.Cols{"parent_issue_id": parent})
		hidden := fx.Issue(t, "Hidden task")
		archived := fx.Issue(t, "Archived task", testutil.Cols{"archived_at": testutil.Raw("now()")})
		read := fx.Issue(t, "Read task", testutil.Cols{"creator_id": reader})
		grant := fx.Insert(t, "projectauth_issue_access_grants", testutil.Cols{"workspace_id": ws, "issue_id": parent, "subject_type": "user", "subject_id": reader, "role_key": "viewer"})
		notice := func(issueID any, isRead bool, createdAt string) {
			fx.Insert(t, "inbox_item", testutil.Cols{"workspace_id": ws, "recipient_type": "member", "recipient_id": reader, "type": "status_changed", "severity": "info", "issue_id": issueID, "title": "Test notice", "read": isRead, "archived": false, "created_at": testutil.Raw(createdAt)})
		}
		notice(child, false, "now() - interval '2 minutes'")
		notice(child, false, "now() - interval '1 minute'")
		notice(hidden, false, "now()")
		notice(archived, false, "now()")
		notice(read, false, "now() - interval '1 minute'")
		notice(read, true, "now()")
		notice(nil, false, "now()")
		check := func(want int64) {
			t.Helper()
			counts, err := h.unreadInboxCountsWithinProjectPermissions(t.Context(), parseUUID(reader), true)
			if err != nil {
				t.Fatal(err)
			}
			if got := counts[parseUUID(ws)]; got != want {
				t.Fatalf("workspace %s count=%d want=%d", suffix, got, want)
			}
			// 2026-10-10 coder(lq): Raw badges count both child notices and the
			// older unread notice; summaries retain only each task's newest row.
			wantRaw := int64(2)
			if want == 2 {
				wantRaw = 4
			}
			raw, err := h.countUnreadInboxWithinProjectPermissions(t.Context(), parseUUID(ws), parseUUID(reader), true)
			if err != nil || raw != wantRaw {
				t.Fatalf("raw count=%d want=%d err=%v", raw, wantRaw, err)
			}
			windowRaw, err := h.countUnreadInboxWithinWindow(t.Context(), parseUUID(ws), parseUUID(reader), issueWindowPolicy{limit: 100}, true)
			if err != nil || windowRaw != wantRaw {
				t.Fatalf("window raw count=%d want=%d err=%v", windowRaw, wantRaw, err)
			}
			windowSummary, err := h.unreadInboxCountsWithinWindows(t.Context(), parseUUID(reader), []pgtype.UUID{parseUUID(ws)}, []int64{100}, true)
			if err != nil || windowSummary[parseUUID(ws)] != want {
				t.Fatalf("window summary=%v want=%d err=%v", windowSummary, want, err)
			}
			ids := []string{child, child, hidden}
			requested := []pgtype.UUID{}
			for _, id := range ids {
				requested = append(requested, parseUUID(id))
			}
			visible, err := h.visibleIssueIDsByProjectPermission(t.Context(), parseUUID(ws), parseUUID(reader), requested)
			if err != nil {
				t.Fatal(err)
			}
			wantVisible := 0
			if want == 2 {
				wantVisible = 1
			}
			if len(visible) != wantVisible {
				t.Fatalf("batch visible=%d want=%d", len(visible), wantVisible)
			}
		}
		check(2)
		fx.InsertNoID(t, "projectauth_grant_constraints", testutil.Cols{"workspace_id": ws, "grant_id": grant, "expires_at": testutil.Raw("now() - interval '1 minute'")}, "workspace_id=$1 AND grant_id=$2", ws, grant)
		check(1)
		fx.Exec(t, "DELETE FROM projectauth_grant_constraints WHERE grant_id=$1", grant)
		check(2)
		fx.Exec(t, "DELETE FROM projectauth_issue_access_grants WHERE id=$1", grant)
		check(1)
	}
}
