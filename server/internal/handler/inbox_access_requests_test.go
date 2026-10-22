package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestInboxAccessRequestsAreIndependentActions(t *testing.T) {
	enableProjectAuthorizationForTest(t)
	ctx := context.Background()
	ws := dbfx.Workspace(t, "Independent requests", "independent-requests-"+uuid.NewString())
	fx := testutil.New(testPool, ws, testUserID)
	fx.Member(t, ws, testUserID, "owner")
	issue := fx.Issue(t, "Shared task")
	created := time.Now().UTC().Add(-time.Hour)
	insert := func(kind string, offset time.Duration) string {
		return fx.Insert(t, "inbox_item", testutil.Cols{
			"workspace_id": ws, "recipient_type": "member", "recipient_id": testUserID, "issue_id": issue,
			"type": kind, "severity": "action_required", "title": kind, "created_at": created.Add(offset),
		})
	}
	first := insert("task_access_request", 0)
	second := insert("task_access_request", time.Minute)
	comment := insert("new_comment", 2*time.Minute)
	latest := insert("status_changed", 3*time.Minute)

	counts, err := testHandler.Queries.CountUnreadInboxByWorkspace(ctx, parseUUID(testUserID))
	if err != nil {
		t.Fatal(err)
	}
	var count int64
	for _, row := range counts {
		if row.WorkspaceID == parseUUID(ws) {
			count = row.Count
		}
	}
	if count != 3 {
		t.Fatalf("two requests and one ordinary activity group = %d unread, want 3", count)
	}
	permissionCounts, err := testHandler.unreadInboxCountsWithinProjectPermissions(ctx, parseUUID(testUserID), true)
	if err != nil || permissionCounts[parseUUID(ws)] != 3 {
		t.Fatalf("permission summary=%v err=%v", permissionCounts, err)
	}
	for _, permissions := range []bool{false, true} {
		var workspace pgtype.UUID
		if err := testPool.QueryRow(ctx, unreadInboxWindowSummarySQL(permissions, true), []pgtype.UUID{parseUUID(ws)}, []int64{10000}, parseUUID(testUserID)).Scan(&workspace, &count); err != nil || count != 3 {
			t.Fatalf("window summary permissions=%v count=%d err=%v", permissions, count, err)
		}
	}

	action := func(handler http.HandlerFunc, id string) {
		t.Helper()
		req := withURLParams(inboxRequest(http.MethodPost, "/api/inbox/"+id, ws), "id", id)
		testutil.Call(t, inboxWorkspaceHandler(handler), req).Want(http.StatusOK)
	}
	assertArchived := func(id string, want bool) {
		t.Helper()
		row, err := testHandler.Queries.GetInboxItem(ctx, parseUUID(id))
		if err != nil || row.Archived != want {
			t.Fatalf("notice %s archived=%v want=%v err=%v", id, row.Archived, want, err)
		}
	}
	page := func(query string) archivedInboxPageResponse {
		t.Helper()
		var out archivedInboxPageResponse
		testutil.Call(t, inboxWorkspaceHandler(testHandler.ListArchivedInboxPage), inboxRequest(http.MethodGet, "/api/inbox/archived/page?"+query, ws)).Want(http.StatusOK).JSON(&out)
		return out
	}
	action(testHandler.ArchiveInboxItem, first)
	assertArchived(second, false)
	assertArchived(latest, false)
	out := page("group_id=" + first)
	if len(out.Items) != 1 || out.Items[0].ID != first {
		t.Fatalf("archived request hidden by another active notice on same task: %+v", out)
	}
	var legacy []InboxItemResponse
	testutil.Call(t, inboxWorkspaceHandler(testHandler.ListArchivedInbox), inboxRequest(http.MethodGet, "/api/inbox/archived", ws)).Want(http.StatusOK).JSON(&legacy)
	if len(legacy) != 1 || legacy[0].ID != first {
		t.Fatalf("legacy archive lost independent request: %+v", legacy)
	}
	var facets archivedInboxFacetsResponse
	testutil.Call(t, inboxWorkspaceHandler(testHandler.GetArchivedInboxFacets), inboxRequest(http.MethodGet, "/api/inbox/archived/facets", ws)).Want(http.StatusOK).JSON(&facets)
	if facets.UnreadCount != 1 {
		t.Fatalf("request facets unread=%d", facets.UnreadCount)
	}
	action(testHandler.ArchiveInboxItem, latest)
	assertArchived(comment, true)
	assertArchived(second, false)
	if out = page(""); len(out.Items) != 2 {
		t.Fatalf("archived request and task activity must be separate groups: %+v", out)
	}
	action(testHandler.UnarchiveInboxItem, first)
	assertArchived(latest, true)
	if out = page(""); len(out.Items) != 1 || out.Items[0].ID != latest {
		t.Fatalf("active request hid archived ordinary activity: %+v", out)
	}
	action(testHandler.ArchiveInboxItem, first)
	action(testHandler.UnarchiveInboxItem, latest)
	assertArchived(first, true)
	assertArchived(comment, false)
	action(testHandler.UnarchiveInboxItem, first)
	action(testHandler.MarkInboxRead, first)
	if _, err := testHandler.Queries.ArchiveAllReadInbox(ctx, db.ArchiveAllReadInboxParams{WorkspaceID: parseUUID(ws), RecipientID: parseUUID(testUserID)}); err != nil {
		t.Fatal(err)
	}
	assertArchived(first, true)
	assertArchived(second, false)
	assertArchived(latest, false)
	fx.Exec(t, `UPDATE issue SET status='done' WHERE id=$1`, issue)
	if _, err := testHandler.Queries.ArchiveCompletedInbox(ctx, db.ArchiveCompletedInboxParams{WorkspaceID: parseUUID(ws), RecipientID: parseUUID(testUserID), TerminalStatusKeys: []string{"done"}}); err != nil {
		t.Fatal(err)
	}
	assertArchived(second, false)
	assertArchived(latest, true)
}
