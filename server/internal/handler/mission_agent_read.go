package handler

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

type missionAgentOwnerReadContextKey struct{}
type missionAgentReadContextKey struct{}

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
	r = r.WithContext(context.WithValue(r.Context(), missionAgentReadContextKey{}, true))
	return r, true
}

// AgentMemberResponse deliberately excludes directory management fields and email.
// UserID is the user UUID used by member-type issue assignees, not member.id.
type AgentMemberResponse struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
}

func agentMemberPage(rows []db.ListMembersWithUserRow, q string, limit, offset int) ([]AgentMemberResponse, int) {
	items := make([]AgentMemberResponse, 0, len(rows))
	q = strings.ToLower(strings.TrimSpace(q))
	for _, row := range rows {
		if q == "" || strings.Contains(strings.ToLower(row.UserName), q) {
			items = append(items, AgentMemberResponse{UserID: uuidToString(row.UserID), Name: row.UserName})
		}
	}
	// UUID ordering is deterministic even when multiple users have the same name.
	sort.Slice(items, func(i, j int) bool { return items[i].UserID < items[j].UserID })
	total := len(items)
	start := min(offset, total)
	return items[start:min(start+limit, total)], total
}

func (h *Handler) ListAgentReadMembers(w http.ResponseWriter, r *http.Request) {
	r, ok := h.authorizeMissionAgentRead(w, r)
	if !ok {
		return
	}
	q, limit, offset, ok := parseAgentDirectoryQuery(w, r)
	if !ok {
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	// Reuse the existing workspace directory query and membership boundary.
	rows, err := h.Queries.ListMembersWithUser(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read workspace members")
		return
	}
	items, total := agentMemberPage(rows, q, limit, offset)
	writeJSON(w, http.StatusOK, map[string]any{"members": items, "total": total})
}

func parseAgentDirectoryQuery(w http.ResponseWriter, r *http.Request) (q string, limit, offset int, ok bool) {
	values := r.URL.Query()
	for key, entries := range values {
		if (key != "workspace_id" && key != "q" && key != "limit" && key != "offset") || len(entries) != 1 {
			writeError(w, http.StatusBadRequest, "invalid directory query parameter")
			return "", 0, 0, false
		}
	}
	q = strings.TrimSpace(values.Get("q"))
	if values.Has("q") && (q == "" || utf8.RuneCountInString(q) > 100) {
		writeError(w, http.StatusBadRequest, "q must contain 1 to 100 characters")
		return "", 0, 0, false
	}
	limit, offset = 50, 0
	for _, field := range []string{"limit", "offset"} {
		if !values.Has(field) {
			continue
		}
		n, err := strconv.ParseInt(values.Get(field), 10, 32)
		if err != nil || n < 0 || (field == "limit" && (n < 1 || n > 100)) {
			writeError(w, http.StatusBadRequest, "invalid directory pagination")
			return "", 0, 0, false
		}
		if field == "limit" {
			limit = int(n)
		} else {
			offset = int(n)
		}
	}
	return q, limit, offset, true
}

// AgentProjectResponse is a name-to-ID mapping, not a project detail or stats API.
type AgentProjectResponse struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func (h *Handler) ListAgentReadProjects(w http.ResponseWriter, r *http.Request) {
	r, ok := h.authorizeMissionAgentRead(w, r)
	if !ok {
		return
	}
	q, limit, offset, ok := parseAgentDirectoryQuery(w, r)
	if !ok {
		return
	}
	workspace := h.resolveWorkspaceID(r)
	workspaceID, ok := parseUUIDOrBadRequest(w, workspace, "workspace_id")
	if !ok {
		return
	}
	// Reuse the normal project visibility service. Only the validated internal
	// owner marker can bypass it, irrespective of the browser owner switch.
	var allowed map[string]struct{}
	if !missionAgentOwnerRead(r) && h.ProjectAuth != nil && h.ProjectAuth.Enabled() {
		ids, err := h.ProjectAuth.ScopeWithWorkspaceOwned(r.Context(), projectauth.Subject{
			UserID: requestUserID(r), WorkspaceID: workspace,
		}, true)
		if err != nil {
			writeProjectAuthError(w, err)
			return
		}
		allowed = make(map[string]struct{}, len(ids))
		for _, id := range ids {
			allowed[id] = struct{}{}
		}
	}
	rows, err := h.Queries.ListProjects(r.Context(), db.ListProjectsParams{WorkspaceID: workspaceID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read workspace projects")
		return
	}
	items := make([]AgentProjectResponse, 0, len(rows))
	q = strings.ToLower(q)
	for _, row := range rows {
		id := uuidToString(row.ID)
		if allowed != nil {
			if _, visible := allowed[id]; !visible {
				continue
			}
		}
		if q == "" || strings.Contains(strings.ToLower(row.Title), q) {
			items = append(items, AgentProjectResponse{ID: id, Title: row.Title})
		}
	}
	// Filter before pagination so hidden projects cannot affect pages or totals.
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	total := len(items)
	start := min(offset, total)
	writeJSON(w, http.StatusOK, map[string]any{"projects": items[start:min(start+limit, total)], "total": total})
}

// fillAgentAssigneeNames runs only after authorization by an internal agent read
// handler. One workspace directory query serves the entire page; no per-task
// lookups or changes to normal browser/API response projections are needed.
func (h *Handler) fillAgentAssigneeNames(r *http.Request, workspaceID pgtype.UUID, issues []IssueResponse) error {
	if enabled, _ := r.Context().Value(missionAgentReadContextKey{}).(bool); !enabled {
		return nil
	}
	hasMember := false
	for _, issue := range issues {
		if issue.AssigneeType != nil && *issue.AssigneeType == "member" && issue.AssigneeID != nil {
			hasMember = true
			break
		}
	}
	if !hasMember {
		return nil
	}
	rows, err := h.Queries.ListMembersWithUser(r.Context(), workspaceID)
	if err != nil {
		return err
	}
	names := make(map[string]string, len(rows))
	for _, row := range rows {
		names[uuidToString(row.UserID)] = row.UserName
	}
	for i := range issues {
		issue := &issues[i]
		if issue.AssigneeType != nil && *issue.AssigneeType == "member" && issue.AssigneeID != nil {
			if name, ok := names[*issue.AssigneeID]; ok {
				issue.AssigneeName = &name
			}
		}
	}
	return nil
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
