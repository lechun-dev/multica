package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 2026-10-10 coder(lq): Related-task references cannot widen the task ACL of
// their source. Exercise both relation directions and the shared list filler.
func TestDuplicateRelationsRespectPrivateTaskVisibility(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fixture := newIssueTableVisibilityFixture(t)
	h := permissionCacheHandler(t)
	visible := fixture.fx.Issue(t, "Readable duplicate", testutil.Cols{"creator_type": "member", "creator_id": fixture.reader, "status": "cancelled"})
	hidden := fixture.fx.Issue(t, "Secret original", testutil.Cols{"status": "todo"})
	fixture.fx.Exec(t, "UPDATE issue SET duplicate_of_issue_id=$1 WHERE id=$2", hidden, visible)
	req := withURLParam(newRequestAs(fixture.reader, http.MethodGet, "/api/issues/"+visible+"/duplicates", nil), "id", visible)
	req.Header.Set("X-Workspace-ID", fixture.ws)
	var relations duplicateRelations
	testutil.Call(t, h.ListIssueDuplicates, req).Want(http.StatusOK).JSON(&relations)
	if relations.DuplicateOf != nil {
		t.Fatalf("leaked restricted original: %+v", relations.DuplicateOf)
	}
	var stored db.Issue
	stored, err := h.Queries.GetIssueInWorkspace(context.Background(), db.GetIssueInWorkspaceParams{ID: parseUUID(visible), WorkspaceID: parseUUID(fixture.ws)})
	if err != nil {
		t.Fatal(err)
	}
	response := issueToResponse(stored, "TST")
	h.fillStatusCategory(duplicateReadContext(req), stored.WorkspaceID, &response)
	if response.DuplicateOf != nil {
		t.Fatalf("shared filler leaked original: %+v", response.DuplicateOf)
	}
	response = issueToResponse(stored, "TST")
	h.fillStatusCategory(context.Background(), stored.WorkspaceID, &response)
	if response.DuplicateOf != nil {
		t.Fatalf("background filler leaked original: %+v", response.DuplicateOf)
	}
	fixture.fx.Exec(t, "UPDATE issue SET duplicate_of_issue_id=NULL,status='todo' WHERE id=$1", visible)
	fixture.fx.Exec(t, "UPDATE issue SET duplicate_of_issue_id=$1,status='cancelled' WHERE id=$2", visible, hidden)
	testutil.Call(t, h.ListIssueDuplicates, req).Want(http.StatusOK).JSON(&relations)
	if len(relations.Duplicates) != 0 {
		t.Fatalf("leaked restricted reverse relation: %+v", relations.Duplicates)
	}
}

// 2026-10-10 coder(lq): Guessing another task's ID cannot create a duplicate
// reference, and a rejected mark must leave the writable source unchanged.
func TestDuplicateMarkRequiresPrivateTargetView(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fixture := newIssueTableVisibilityFixture(t)
	h := permissionCacheHandler(t)
	visible := fixture.fx.Issue(t, "Writable source", testutil.Cols{"creator_type": "member", "creator_id": fixture.reader})
	hidden := fixture.fx.Issue(t, "Secret target")
	req := withURLParam(newRequestAs(fixture.reader, http.MethodPut, "/api/issues/"+visible, map[string]any{"duplicate_of_issue_id": hidden}), "id", visible)
	req.Header.Set("X-Workspace-ID", fixture.ws)
	resp := testutil.Call(t, h.UpdateIssue, req)
	if resp.Code < 400 || resp.Code >= 500 {
		t.Fatalf("unexpected hidden target result: %d", resp.Code)
	}
	var status string
	var marked bool
	fixture.fx.QueryRow(t, "SELECT status,duplicate_of_issue_id IS NOT NULL FROM issue WHERE id=$1", visible).Scan(&status, &marked)
	if marked || status == "cancelled" {
		t.Fatalf("rejected mark changed source: status=%s marked=%v", status, marked)
	}
}
