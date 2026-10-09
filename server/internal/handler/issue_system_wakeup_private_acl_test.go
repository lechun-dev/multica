package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// 2026-10-10 coder(lq): A task viewer cannot rewrite the platform instruction
// that its assignee executes; read access never implies management permission.
func TestSystemWakeupMutationRequiresPrivateTaskManage(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fixture := newIssueTableVisibilityFixture(t)
	h := permissionCacheHandler(t)
	var issue string
	fixture.fx.QueryRow(t, "SELECT id::text FROM issue WHERE workspace_id=$1 AND title='Shared todo'", fixture.ws).Scan(&issue)
	req := withURLParam(newRequestAs(fixture.reader, http.MethodPut, "/api/issues/"+issue+"/system-wakeups/child_done", map[string]any{"enabled": false}), "id", issue)
	req = withURLParam(req, "rule", "child_done")
	req.Header.Set("X-Workspace-ID", fixture.ws)
	testutil.Call(t, h.UpdateIssueSystemWakeup, req).Want(http.StatusForbidden)
}
