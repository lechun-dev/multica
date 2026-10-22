package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

// 2026-10-14 coder(lq): The denied-task screen resolves only a scoped reference;
// identifiers are derived from workspace prefix and issue number, not a column.
func TestIssueAccessRequestTargetResolvesDeniedTaskReference(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	h := *testHandler
	h.ProjectAuth = projectauth.NewWithRollout(newProjectAuthRepository(testPool), projectauth.RolloutRestricted)
	h.EffectiveIssueAccess = projectauth.NewEffectiveAccessResolver(&projectAuthRepository{db: testPool})
	reader := dbfx.User(t, "Reference reader", "reference-reader@example.test")
	dbfx.Member(t, testWorkspaceID, reader, "member")
	project := dbfx.Project(t, "Reference project")
	issue := dbfx.Issue(t, "Must not disclose task content", testutil.Cols{"project_id": project})
	var identifier string
	dbfx.QueryRow(t, `SELECT w.issue_prefix || '-' || i.number::text FROM issue i JOIN workspace w ON w.id=i.workspace_id WHERE i.id=$1`, issue).Scan(&identifier)
	denied := newRequestAs(reader, http.MethodGet, "/api/issues/"+issue, nil)
	denied.Header.Set("X-Workspace-ID", testWorkspaceID)
	testutil.Call(t, h.GetIssue, withURLParam(denied, "id", issue)).Want(http.StatusForbidden)
	for _, ref := range []string{issue, identifier, strings.ToLower(identifier), strings.ToUpper(issue)} {
		t.Run(ref, func(t *testing.T) {
			req := newRequestAs(reader, http.MethodGet, "/api/issues/access-request-target/"+ref, nil)
			req.Header.Set("X-Workspace-ID", testWorkspaceID)
			var result map[string]any
			testutil.Call(t, h.GetIssueAccessRequestTarget, withURLParam(req, "id", ref)).Want(http.StatusOK).JSON(&result)
			if len(result) != 2 || result["id"] != issue || result["identifier"] != identifier {
				t.Fatalf("unexpected reference: %#v", result)
			}
		})
	}
	for _, ref := range []string{"OTHER-" + strings.Split(identifier, "-")[1], "missing-task"} {
		req := newRequestAs(reader, http.MethodGet, "/api/issues/access-request-target/"+ref, nil)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		testutil.Call(t, h.GetIssueAccessRequestTarget, withURLParam(req, "id", ref)).Want(http.StatusNotFound)
	}
	outsider := dbfx.User(t, "Reference outsider", "reference-outsider@example.test")
	req := newRequestAs(outsider, http.MethodGet, "/api/issues/access-request-target/"+issue, nil)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	testutil.Call(t, h.GetIssueAccessRequestTarget, withURLParam(req, "id", issue)).Want(http.StatusNotFound)

	t.Run("legacy prefix and foreign workspace", func(t *testing.T) {
		workspace := dbfx.Workspace(t, "Legacy reference workspace", "legacy-reference-workspace", testutil.Cols{"issue_prefix": ""})
		fx := testutil.New(testPool, workspace, testUserID)
		fx.Member(t, workspace, reader, "member")
		task := fx.Issue(t, "Legacy reference task")
		row, err := h.Queries.GetIssue(context.Background(), parseUUID(task))
		if err != nil {
			t.Fatal(err)
		}
		legacyIdentifier := fmt.Sprintf("%s-%d", h.getIssuePrefix(context.Background(), parseUUID(workspace)), row.Number)
		for _, ref := range []string{task, legacyIdentifier} {
			req := newRequestAs(reader, http.MethodGet, "/api/issues/access-request-target/"+ref, nil)
			req.Header.Set("X-Workspace-ID", workspace)
			var result map[string]any
			testutil.Call(t, h.GetIssueAccessRequestTarget, withURLParam(req, "id", ref)).Want(http.StatusOK).JSON(&result)
			if result["identifier"] != legacyIdentifier {
				t.Fatalf("legacy identifier: %#v", result)
			}
		}
		req := newRequestAs(reader, http.MethodGet, "/api/issues/access-request-target/"+task, nil)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		testutil.Call(t, h.GetIssueAccessRequestTarget, withURLParam(req, "id", task)).Want(http.StatusNotFound)
	})
}
