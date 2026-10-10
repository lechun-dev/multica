package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// 2026-10-10 coder(lq): Notification state belongs to its recipient, not the
// referenced task ACL. A broadcast must never grant task access implicitly.
func TestInboxRecipientCanManageDeniedTaskNotification(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ws := dbfx.Workspace(t, "Denied inbox", "denied-inbox")
	fx := testutil.New(testPool, ws, testUserID)
	user := dbfx.User(t, "Broadcast recipient", "broadcast-recipient@example.test")
	other := dbfx.User(t, "Other recipient", "other-recipient@example.test")
	fx.Member(t, ws, user, "member")
	fx.Member(t, testWorkspaceID, user, "member")
	fx.Member(t, ws, other, "member")
	fx.Member(t, ws, testUserID, "member")
	project := fx.Project(t, "Private project")
	issue := fx.Issue(t, "Private task", testutil.Cols{"project_id": project})
	inbox := fx.Insert(t, "inbox_item", testutil.Cols{
		"workspace_id": ws, "recipient_type": "member", "recipient_id": user,
		"actor_type": "member", "actor_id": testUserID, "type": "mentioned",
		"issue_id": issue, "title": "Broadcast notification",
	})
	h := permissionCacheHandler(t)
	call := func(t *testing.T, actor, workspace, method, path, id string, handler http.HandlerFunc, want int) {
		t.Helper()
		req := newRequestAs(actor, method, path, nil)
		req.Header.Set("X-Workspace-ID", workspace)
		req = withURLParam(req, "id", id)
		response := testutil.Call(t, handler, req).Want(want)
		if want == http.StatusOK {
			var payload InboxItemResponse
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.IssueStatus != nil || payload.IssuePriority != nil {
				t.Fatal("notification state exposed inaccessible task metadata")
			}
		}
	}
	call(t, user, ws, http.MethodGet, "/api/issues/"+issue, issue, h.GetIssue, http.StatusForbidden)
	for _, action := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"read", h.MarkInboxRead}, {"unread", h.MarkInboxUnread},
		{"archive", h.ArchiveInboxItem}, {"unarchive", h.UnarchiveInboxItem},
	} {
		t.Run(action.name, func(t *testing.T) {
			call(t, user, ws, http.MethodPost, "/api/inbox/"+inbox+"/"+action.name, inbox, action.handler, http.StatusOK)
			call(t, other, ws, http.MethodPost, "/api/inbox/"+inbox+"/"+action.name, inbox, action.handler, http.StatusNotFound)
			call(t, user, testWorkspaceID, http.MethodPost, "/api/inbox/"+inbox+"/"+action.name, inbox, action.handler, http.StatusNotFound)
		})
	}
	call(t, user, ws, http.MethodGet, "/api/issues/"+issue, issue, h.GetIssue, http.StatusForbidden)
	fx.Exec(t, "DELETE FROM issue WHERE id=$1", issue)
	call(t, user, ws, http.MethodPost, "/api/inbox/"+inbox+"/archive", inbox, h.ArchiveInboxItem, http.StatusNotFound)
	if count := fx.Count(t, "SELECT count(*) FROM inbox_item WHERE id=$1", inbox); count != 0 {
		t.Fatal("deleted task left an inbox row")
	}
	// 2026-10-10 coder(lq): Use a non-task notification to check removed members
	// cannot change personal notification state after the task has gone.
	remaining := fx.Insert(t, "inbox_item", testutil.Cols{
		"workspace_id": ws, "recipient_type": "member", "recipient_id": user,
		"actor_type": "member", "actor_id": testUserID, "type": "mentioned", "title": "Notification",
	})
	fx.Exec(t, "DELETE FROM member WHERE workspace_id=$1 AND user_id=$2", ws, user)
	call(t, user, ws, http.MethodPost, "/api/inbox/"+remaining+"/read", remaining, h.MarkInboxRead, http.StatusForbidden)
}
