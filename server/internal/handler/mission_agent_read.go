package handler

import (
	"context"
	"net/http"

	"github.com/multica-ai/multica/server/internal/auth"
)

type missionAgentOwnerReadContextKey struct{}

func withMissionAgentOwnerRead(ctx context.Context) context.Context {
	return context.WithValue(ctx, missionAgentOwnerReadContextKey{}, true)
}

func missionAgentOwnerRead(r *http.Request) bool {
	allowed, _ := r.Context().Value(missionAgentOwnerReadContextKey{}).(bool)
	return allowed
}

// authorizeMissionAgentRead validates the purpose-specific JWT scope and then
// re-reads the current workspace membership. The role in the token is only a
// hint from the exchange moment; owner access is granted only while the
// database still says this user is the workspace owner.
func (h *Handler) authorizeMissionAgentRead(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	scope := auth.MissionReadScope(r.Context())
	if scope != auth.MissionTaskReadScope && scope != auth.MissionOwnerReadScope {
		writeError(w, http.StatusForbidden, "mission token is not valid for agent task reads")
		return nil, false
	}
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return nil, false
	}
	if scope == auth.MissionOwnerReadScope && member.Role == "owner" {
		r = r.WithContext(withMissionAgentOwnerRead(r.Context()))
	}
	return r, true
}

// ListAgentReadIssues is the only list route where a validated owner-scope
// Mission token can bypass project/task ACLs. All filters, archive handling,
// pagination, completion projections, and issue-window limits remain those of
// the existing ListIssues implementation.
func (h *Handler) ListAgentReadIssues(w http.ResponseWriter, r *http.Request) {
	r, ok := h.authorizeMissionAgentRead(w, r)
	if !ok {
		return
	}
	h.ListIssues(w, r)
}

// GetAgentReadIssue delegates projection and workspace/window checks to the
// existing detail handler. The owner marker only suppresses the project/task
// permission gate in loadIssueForUser and is set exclusively above.
func (h *Handler) GetAgentReadIssue(w http.ResponseWriter, r *http.Request) {
	r, ok := h.authorizeMissionAgentRead(w, r)
	if !ok {
		return
	}
	h.GetIssue(w, r)
}
