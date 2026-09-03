package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

// projectAccessGrantRequest is deliberately provider-neutral. The caller
// supplies a MissionOS user, role, organization, or everyone subject; the
// authorization service validates the subject and resource boundaries.
type projectAccessGrantRequest struct {
	SubjectType projectauth.SubjectType `json:"subject_type"`
	SubjectID   string                  `json:"subject_id"`
	Role        projectauth.ProjectRole `json:"role"`
	Permission  projectauth.Permission  `json:"permission"`
}

func (h *Handler) issueAccessSubject(w http.ResponseWriter, r *http.Request, issueID string) (projectauth.Subject, string, bool) {
	issueUUID, ok := parseUUIDOrBadRequest(w, issueID, "task id")
	if !ok {
		return projectauth.Subject{}, "", false
	}
	issueID = util.UUIDToString(issueUUID)
	userID, ok := requireUserID(w, r)
	if !ok {
		return projectauth.Subject{}, "", false
	}
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return projectauth.Subject{}, "", false
	}
	var issueWorkspaceID, projectID string
	if err := h.DB.QueryRow(r.Context(), `
		SELECT workspace_id::text, project_id::text
		FROM issue WHERE id = $1 AND project_id IS NOT NULL`, issueID).Scan(&issueWorkspaceID, &projectID); err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return projectauth.Subject{}, "", false
	}
	if issueWorkspaceID != workspaceID {
		writeError(w, http.StatusNotFound, "task not found")
		return projectauth.Subject{}, "", false
	}
	member, err := h.getWorkspaceMember(r.Context(), userID, workspaceID)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return projectauth.Subject{}, "", false
	}
	return projectauth.Subject{UserID: userID, WorkspaceID: workspaceID, WorkspaceRole: projectauth.WorkspaceRole(member.Role)}, projectID, true
}

// 2026-09-01 coder(lq): Normalize UUID subjects at the HTTP boundary so
// malformed user or organization IDs return 400 instead of reaching a
// PostgreSQL UUID cast and becoming an opaque 500.
func normalizeAccessGrantIDs(w http.ResponseWriter, grant *projectauth.AccessGrant) bool {
	projectUUID, ok := parseUUIDOrBadRequest(w, grant.ProjectID, "project id")
	if !ok {
		return false
	}
	grant.ProjectID = util.UUIDToString(projectUUID)
	if grant.IssueID != "" {
		issueUUID, valid := parseUUIDOrBadRequest(w, grant.IssueID, "task id")
		if !valid {
			return false
		}
		grant.IssueID = util.UUIDToString(issueUUID)
	}
	if grant.SubjectType == projectauth.SubjectUser || grant.SubjectType == projectauth.SubjectOrganization {
		subjectUUID, valid := parseUUIDOrBadRequest(w, grant.SubjectID, "subject id")
		if !valid {
			return false
		}
		grant.SubjectID = util.UUIDToString(subjectUUID)
	}
	return true
}

func (h *Handler) listProjectAccessGrants(w http.ResponseWriter, r *http.Request) {
	if h.ProjectAuth == nil || !h.ProjectAuth.Enabled() {
		writeErrorCode(w, http.StatusNotFound, "project_permission_disabled", "project permissions are disabled")
		return
	}
	projectID := chi.URLParam(r, "id")
	projectUUID, ok := parseUUIDOrBadRequest(w, projectID, "project id")
	if !ok {
		return
	}
	projectID = util.UUIDToString(projectUUID)
	subject, ok := h.projectSubject(w, r, projectID)
	if !ok {
		return
	}
	grants, err := h.ProjectAuth.ListAccessGrants(r.Context(), subject, projectID, "")
	if err != nil {
		writeProjectAccessGrantError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"grants": grants, "total": len(grants)})
}

// ListProjectAccessGrants is the router-facing export for the project grant
// endpoint. Keep the implementation unexported so the handler file's helper
// surface stays small while the server package can register it.
func (h *Handler) ListProjectAccessGrants(w http.ResponseWriter, r *http.Request) {
	h.listProjectAccessGrants(w, r)
}

func (h *Handler) listIssueAccessGrants(w http.ResponseWriter, r *http.Request) {
	if h.ProjectAuth == nil || !h.ProjectAuth.Enabled() {
		writeErrorCode(w, http.StatusNotFound, "project_permission_disabled", "project permissions are disabled")
		return
	}
	issueID := chi.URLParam(r, "id")
	subject, projectID, ok := h.issueAccessSubject(w, r, issueID)
	if !ok {
		return
	}
	issueUUID, ok := parseUUIDOrBadRequest(w, issueID, "task id")
	if !ok {
		return
	}
	grants, err := h.ProjectAuth.ListAccessGrants(r.Context(), subject, projectID, util.UUIDToString(issueUUID))
	if err != nil {
		writeProjectAccessGrantError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"grants": grants, "total": len(grants), "project_id": projectID})
}

func (h *Handler) ListIssueAccessGrants(w http.ResponseWriter, r *http.Request) {
	h.listIssueAccessGrants(w, r)
}

func decodeProjectAccessGrant(r *http.Request) (projectauth.AccessGrant, error) {
	var req projectAccessGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return projectauth.AccessGrant{}, err
	}
	return projectauth.AccessGrant{
		SubjectType: req.SubjectType,
		SubjectID:   strings.TrimSpace(req.SubjectID),
		Role:        req.Role,
		Permission:  req.Permission,
	}, nil
}

func (h *Handler) mutateProjectAccessGrant(w http.ResponseWriter, r *http.Request, grant projectauth.AccessGrant, issueID string) {
	if h.TxStarter == nil {
		writeErrorCode(w, http.StatusServiceUnavailable, "project_permission_unavailable", "project permission storage is unavailable")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeProjectAccessGrantError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	// 2026-08-31 coder(lq): Lock the canonical resource during a grant change
	// so concurrent project deletion or task moves cannot create an orphaned
	// authorization row.
	if issueID == "" {
		var workspaceID string
		if err := tx.QueryRow(r.Context(), `SELECT workspace_id::text FROM project WHERE id=$1 FOR UPDATE`, grant.ProjectID).Scan(&workspaceID); err != nil {
			writeProjectAccessGrantError(w, projectauth.ErrNoProjectAccess)
			return
		}
		grant.WorkspaceID = workspaceID
	} else {
		var workspaceID, projectID string
		if err := tx.QueryRow(r.Context(), `SELECT workspace_id::text, project_id::text FROM issue WHERE id=$1 AND project_id IS NOT NULL FOR UPDATE`, issueID).Scan(&workspaceID, &projectID); err != nil || projectID != grant.ProjectID {
			writeProjectAccessGrantError(w, projectauth.ErrCrossWorkspace)
			return
		}
		grant.WorkspaceID = workspaceID
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	member, err := h.getWorkspaceMember(r.Context(), userID, grant.WorkspaceID)
	if err != nil {
		writeProjectAccessGrantError(w, projectauth.ErrNotWorkspaceMember)
		return
	}
	actor := projectauth.Subject{UserID: userID, WorkspaceID: grant.WorkspaceID, WorkspaceRole: projectauth.WorkspaceRole(member.Role)}
	service := projectauth.New(newProjectAuthRepository(tx), true)
	if err := service.GrantAccess(r.Context(), actor, grant); err != nil {
		writeProjectAccessGrantError(w, err)
		return
	}
	// Return the canonical row rather than an empty 204. The web client uses
	// this response to update its grant cache without guessing generated IDs or
	// server-normalized fields. Older adapters may not implement the optional
	// reader, so retain a source-compatible fallback during migration.
	created := grant
	created.WorkspaceID = actor.WorkspaceID
	created.Source = projectauth.GrantSourceManual
	created.GrantedBy = actor.UserID
	if reader, ok := any(newProjectAuthRepository(tx)).(projectauth.AccessGrantReader); ok {
		persisted, readErr := reader.GetAccessGrant(r.Context(), grant.WorkspaceID, grant.ProjectID, grant.IssueID,
			grant.SubjectType, grant.SubjectID, grant.Role, grant.Permission)
		if readErr != nil {
			writeProjectAccessGrantError(w, readErr)
			return
		}
		created = persisted
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeProjectAccessGrantError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *Handler) createProjectAccessGrant(w http.ResponseWriter, r *http.Request) {
	if h.ProjectAuth == nil || !h.ProjectAuth.Enabled() {
		writeErrorCode(w, http.StatusNotFound, "project_permission_disabled", "project permissions are disabled")
		return
	}
	grant, err := decodeProjectAccessGrant(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid access grant payload")
		return
	}
	grant.ProjectID = chi.URLParam(r, "id")
	if !normalizeAccessGrantIDs(w, &grant) {
		return
	}
	h.mutateProjectAccessGrant(w, r, grant, "")
}

func (h *Handler) CreateProjectAccessGrant(w http.ResponseWriter, r *http.Request) {
	h.createProjectAccessGrant(w, r)
}

func (h *Handler) createIssueAccessGrant(w http.ResponseWriter, r *http.Request) {
	if h.ProjectAuth == nil || !h.ProjectAuth.Enabled() {
		writeErrorCode(w, http.StatusNotFound, "project_permission_disabled", "project permissions are disabled")
		return
	}
	grant, err := decodeProjectAccessGrant(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid access grant payload")
		return
	}
	grant.IssueID = chi.URLParam(r, "id")
	_, projectID, ok := h.issueAccessSubject(w, r, grant.IssueID)
	if !ok {
		return
	}
	grant.ProjectID = projectID
	if !normalizeAccessGrantIDs(w, &grant) {
		return
	}
	h.mutateProjectAccessGrant(w, r, grant, grant.IssueID)
}

func (h *Handler) CreateIssueAccessGrant(w http.ResponseWriter, r *http.Request) {
	h.createIssueAccessGrant(w, r)
}

func (h *Handler) revokeProjectAccessGrant(w http.ResponseWriter, r *http.Request) {
	h.revokeAccessGrant(w, r, chi.URLParam(r, "id"), "")
}

func (h *Handler) RevokeProjectAccessGrant(w http.ResponseWriter, r *http.Request) {
	h.revokeProjectAccessGrant(w, r)
}

func (h *Handler) revokeIssueAccessGrant(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	_, projectID, ok := h.issueAccessSubject(w, r, issueID)
	if !ok {
		return
	}
	h.revokeAccessGrant(w, r, projectID, issueID)
}

func (h *Handler) RevokeIssueAccessGrant(w http.ResponseWriter, r *http.Request) {
	h.revokeIssueAccessGrant(w, r)
}

func (h *Handler) revokeAccessGrant(w http.ResponseWriter, r *http.Request, projectID, issueID string) {
	if h.ProjectAuth == nil || !h.ProjectAuth.Enabled() {
		writeErrorCode(w, http.StatusNotFound, "project_permission_disabled", "project permissions are disabled")
		return
	}
	grant, err := decodeProjectAccessGrant(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid access grant payload")
		return
	}
	grant.ProjectID, grant.IssueID = projectID, issueID
	if !normalizeAccessGrantIDs(w, &grant) {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	member, err := h.getWorkspaceMember(r.Context(), userID, workspaceID)
	if err != nil {
		writeProjectAccessGrantError(w, projectauth.ErrNotWorkspaceMember)
		return
	}
	actor := projectauth.Subject{UserID: userID, WorkspaceID: workspaceID, WorkspaceRole: projectauth.WorkspaceRole(member.Role)}
	if err := h.withProjectPermissionRoleTransaction(r.Context(), func(service *projectauth.Service) error {
		grant.WorkspaceID = workspaceID
		return service.RevokeAccess(r.Context(), actor, grant)
	}); err != nil {
		writeProjectAccessGrantError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeProjectAccessGrantError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, projectauth.ErrMigrationRequired):
		writeErrorCode(w, http.StatusServiceUnavailable, "project_permission_migration_required", "project permission migration is required")
	case errors.Is(err, projectauth.ErrStorageUnavailable):
		writeErrorCode(w, http.StatusServiceUnavailable, "project_permission_unavailable", "project permission storage is unavailable")
	case errors.Is(err, projectauth.ErrInvalidRole), errors.Is(err, projectauth.ErrInvalidIssuePermission), errors.Is(err, projectauth.ErrInvalidSubject):
		writeErrorCode(w, http.StatusBadRequest, "invalid_access_grant", "invalid access grant")
	case errors.Is(err, projectauth.ErrForbidden):
		writeErrorCode(w, http.StatusForbidden, "project_permission_forbidden", "insufficient project permissions")
	case errors.Is(err, projectauth.ErrNotWorkspaceMember), errors.Is(err, projectauth.ErrNoProjectAccess), errors.Is(err, projectauth.ErrCrossWorkspace):
		writeErrorCode(w, http.StatusNotFound, "resource_not_found", "resource not found")
	case errors.Is(err, projectauth.ErrDisabled):
		writeErrorCode(w, http.StatusServiceUnavailable, "project_permission_unavailable", "project permission storage is unavailable")
	default:
		writeErrorCode(w, http.StatusInternalServerError, "project_permission_failed", "failed to update project permissions")
	}
}
