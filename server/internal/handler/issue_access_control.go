package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

// issueAccessControlRequest is a complete replacement of the task's manual ACL.
// System, migration and access-request grants are deliberately outside this API.
type issueAccessControlRequest struct {
	ExpectedVersion   int64                         `json:"expected_version"`
	ProjectAccessMode projectauth.ProjectAccessMode `json:"project_access_mode"`
	Grants            []issueAccessControlGrant     `json:"grants"`
}

type issueAccessControlGrant struct {
	SubjectType projectauth.SubjectType `json:"subject_type"`
	SubjectID   string                  `json:"subject_id,omitempty"`
	Role        projectauth.RoleKey     `json:"role"`
	Scope       projectauth.RoleScope   `json:"scope"`
	ExpiresAt   *time.Time              `json:"expires_at,omitempty"`
}

type issueAccessControlResponse struct {
	WorkspaceID       string                        `json:"workspace_id"`
	IssueID           string                        `json:"issue_id"`
	ProjectID         string                        `json:"project_id,omitempty"`
	Scope             projectauth.RoleScope         `json:"scope"`
	ProjectAccessMode projectauth.ProjectAccessMode `json:"project_access_mode"`
	PolicyVersion     int64                         `json:"policy_version"`
	Grants            []issueAccessControlGrant     `json:"grants"`
}

type issueAccessControlPreview struct {
	Before                  issueAccessControlResponse `json:"before"`
	After                   issueAccessControlResponse `json:"after"`
	SubjectsLosingAccess    []string                   `json:"subjects_losing_access"`
	SubjectsWithOtherSource []string                   `json:"subjects_with_other_source"`
	AffectedEffects         []string                   `json:"affected_effects"`
}

type issuePolicyVersionConflict struct{ current int64 }

func (e issuePolicyVersionConflict) Error() string { return "task access policy version conflict" }

func decodeIssueAccessControlRequest(r *http.Request) (issueAccessControlRequest, error) {
	var req issueAccessControlRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return req, err
	}
	if req.ExpectedVersion < 1 {
		return req, errors.New("expected_version must be positive")
	}
	if req.ProjectAccessMode != projectauth.ProjectAccessInherit && req.ProjectAccessMode != projectauth.ProjectAccessRestricted {
		return req, errors.New("invalid project_access_mode")
	}
	return req, nil
}

func (h *Handler) issueAccessControlActor(w http.ResponseWriter, r *http.Request, issueID string) (projectauth.Subject, string, string, bool) {
	issueUUID, ok := parseUUIDOrBadRequest(w, issueID, "task id")
	if !ok {
		return projectauth.Subject{}, "", "", false
	}
	issueID = util.UUIDToString(issueUUID)
	subject, projectID, ok := h.issueAccessSubject(w, r, issueID)
	if !ok {
		return projectauth.Subject{}, "", "", false
	}
	allowed, reason := h.effectiveIssueAccessAllowed(r.Context(), subject, issueID, projectauth.IssueManage, true)
	if !allowed {
		if reason == "internal" || reason == "unavailable" || reason == "migration" {
			writeProjectAccessGrantError(w, projectauth.ErrStorageUnavailable)
		} else {
			writeProjectAccessGrantError(w, projectauth.ErrForbidden)
		}
		return projectauth.Subject{}, "", "", false
	}
	return subject, issueID, projectID, true
}

func (h *Handler) GetIssueAccessControl(w http.ResponseWriter, r *http.Request) {
	subject, issueID, projectID, ok := h.issueAccessControlActor(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	state, err := readIssueAccessControl(r.Context(), h.DB, subject.WorkspaceID, issueID, projectID, false)
	if err != nil {
		writeProjectAccessGrantError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (h *Handler) PreviewIssueAccessControl(w http.ResponseWriter, r *http.Request) {
	request, err := decodeIssueAccessControlRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	subject, issueID, projectID, ok := h.issueAccessControlActor(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	preview, err := h.previewIssueAccessControl(r.Context(), subject, issueID, projectID, request)
	if err != nil {
		h.writeIssueAccessControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (h *Handler) PatchIssueAccessControl(w http.ResponseWriter, r *http.Request) {
	request, err := decodeIssueAccessControlRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	subject, issueID, projectID, ok := h.issueAccessControlActor(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if h.TxStarter == nil {
		writeProjectAccessGrantError(w, projectauth.ErrStorageUnavailable)
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeProjectAccessGrantError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	state, changed, err := h.applyIssueAccessControl(r.Context(), tx, subject, issueID, projectID, request)
	if err != nil {
		h.writeIssueAccessControlError(w, err)
		return
	}
	if changed {
		if err := tx.Commit(r.Context()); err != nil {
			writeProjectAccessGrantError(w, err)
			return
		}
	} else if err := tx.Rollback(r.Context()); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		writeProjectAccessGrantError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (h *Handler) writeIssueAccessControlError(w http.ResponseWriter, err error) {
	var conflict issuePolicyVersionConflict
	if errors.As(err, &conflict) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": conflict.Error(), "code": "policy_version_conflict", "current_version": conflict.current})
		return
	}
	switch {
	case errors.Is(err, projectauth.ErrInvalidRole), errors.Is(err, projectauth.ErrInvalidRoleScope),
		errors.Is(err, projectauth.ErrInvalidSubject), errors.Is(err, projectauth.ErrInvalidIssuePermission):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeProjectAccessGrantError(w, err)
	}
}

func (h *Handler) previewIssueAccessControl(ctx context.Context, actor projectauth.Subject, issueID, projectID string, request issueAccessControlRequest) (issueAccessControlPreview, error) {
	if h.TxStarter == nil {
		return issueAccessControlPreview{}, projectauth.ErrStorageUnavailable
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return issueAccessControlPreview{}, err
	}
	defer tx.Rollback(ctx)
	before, err := readIssueAccessControl(ctx, tx, actor.WorkspaceID, issueID, projectID, true)
	if err != nil {
		return issueAccessControlPreview{}, err
	}
	after, _, err := h.applyIssueAccessControl(ctx, tx, actor, issueID, projectID, request)
	if err != nil {
		return issueAccessControlPreview{}, err
	}
	preview := issueAccessControlPreview{Before: before, After: after,
		AffectedEffects: []string{"subscriptions", "notifications", "queued_agent_runs"}}
	afterSubjects := map[string]struct{}{}
	for _, grant := range after.Grants {
		afterSubjects[accessControlSubjectKey(grant)] = struct{}{}
	}
	for _, grant := range before.Grants {
		key := accessControlSubjectKey(grant)
		if _, retained := afterSubjects[key]; retained {
			preview.SubjectsWithOtherSource = append(preview.SubjectsWithOtherSource, key)
		} else {
			preview.SubjectsLosingAccess = append(preview.SubjectsLosingAccess, key)
		}
	}
	preview.SubjectsLosingAccess = sortedUnique(preview.SubjectsLosingAccess)
	preview.SubjectsWithOtherSource = sortedUnique(preview.SubjectsWithOtherSource)
	return preview, nil
}

func accessControlSubjectKey(grant issueAccessControlGrant) string {
	return string(grant.SubjectType) + ":" + grant.SubjectID
}

func sortedUnique(values []string) []string {
	set := map[string]struct{}{}
	for _, value := range values {
		set[value] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (h *Handler) applyIssueAccessControl(ctx context.Context, tx dbExecutor, actor projectauth.Subject, issueID, projectID string, request issueAccessControlRequest) (issueAccessControlResponse, bool, error) {
	var lockedWorkspace, lockedProject string
	if err := tx.QueryRow(ctx, `SELECT workspace_id::text, COALESCE(project_id::text, '') FROM issue WHERE id=$1 FOR UPDATE`, issueID).Scan(&lockedWorkspace, &lockedProject); err != nil {
		return issueAccessControlResponse{}, false, projectauth.ErrNoProjectAccess
	}
	if lockedWorkspace != actor.WorkspaceID || lockedProject != projectID {
		return issueAccessControlResponse{}, false, projectauth.ErrCrossWorkspace
	}
	if _, err := tx.Exec(ctx, `INSERT INTO projectauth_issue_policies (workspace_id, issue_id, created_by, updated_by) VALUES ($1,$2,$3,$3) ON CONFLICT DO NOTHING`, actor.WorkspaceID, issueID, actor.UserID); err != nil {
		return issueAccessControlResponse{}, false, wrapProjectPermissionRepositoryError(err)
	}
	current, err := readIssueAccessControl(ctx, tx, actor.WorkspaceID, issueID, projectID, true)
	if err != nil {
		return issueAccessControlResponse{}, false, err
	}
	normalized, err := validateIssueAccessControlGrants(ctx, tx, actor.WorkspaceID, request.Grants)
	if err != nil {
		return issueAccessControlResponse{}, false, err
	}
	proposed := current
	proposed.ProjectAccessMode = request.ProjectAccessMode
	proposed.Grants = normalized
	if sameIssueAccessControl(current, proposed) {
		return current, false, nil
	}
	if request.ExpectedVersion != current.PolicyVersion {
		return issueAccessControlResponse{}, false, issuePolicyVersionConflict{current: current.PolicyVersion}
	}
	table := "projectauth_access_grants"
	if projectID == "" {
		table = "projectauth_issue_access_grants"
	}
	if _, err := tx.Exec(ctx, `DELETE FROM projectauth_grant_constraints c USING `+table+` g WHERE c.workspace_id=$1 AND c.grant_id=g.id AND g.workspace_id=$1 AND g.issue_id=$2 AND g.source='manual'`, actor.WorkspaceID, issueID); err != nil {
		return issueAccessControlResponse{}, false, wrapProjectPermissionRepositoryError(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE workspace_id=$1 AND issue_id=$2 AND source='manual'`, actor.WorkspaceID, issueID); err != nil {
		return issueAccessControlResponse{}, false, wrapProjectPermissionRepositoryError(err)
	}
	for _, grant := range normalized {
		var grantID string
		if projectID == "" {
			err = tx.QueryRow(ctx, `INSERT INTO projectauth_issue_access_grants (workspace_id,issue_id,subject_type,subject_id,role_key,source,granted_by) VALUES ($1,$2,$3,$4,$5,'manual',$6) RETURNING id::text`, actor.WorkspaceID, issueID, grant.SubjectType, grant.SubjectID, grant.Role, actor.UserID).Scan(&grantID)
		} else {
			err = tx.QueryRow(ctx, `INSERT INTO projectauth_access_grants (workspace_id,project_id,issue_id,subject_type,subject_id,role_key,permission,source,granted_by) VALUES ($1,$2,$3,$4,$5,$6,NULL,'manual',$7) RETURNING id::text`, actor.WorkspaceID, projectID, issueID, grant.SubjectType, grant.SubjectID, grant.Role, actor.UserID).Scan(&grantID)
		}
		if err != nil {
			return issueAccessControlResponse{}, false, wrapProjectPermissionRepositoryError(err)
		}
		if grant.ExpiresAt != nil {
			if _, err := tx.Exec(ctx, `INSERT INTO projectauth_grant_constraints (workspace_id,grant_id,expires_at,origin_kind) VALUES ($1,$2,$3,'manual')`, actor.WorkspaceID, grantID, grant.ExpiresAt); err != nil {
				return issueAccessControlResponse{}, false, wrapProjectPermissionRepositoryError(err)
			}
		}
	}
	var nextVersion int64
	if err := tx.QueryRow(ctx, `UPDATE projectauth_issue_policies SET project_access_mode=$3, policy_version=policy_version+1, updated_by=$4, updated_at=now() WHERE workspace_id=$1 AND issue_id=$2 RETURNING policy_version`, actor.WorkspaceID, issueID, request.ProjectAccessMode, actor.UserID).Scan(&nextVersion); err != nil {
		return issueAccessControlResponse{}, false, wrapProjectPermissionRepositoryError(err)
	}
	if err := (&projectAuthRepository{db: tx}).RecordAuthorizationAudit(ctx, projectauth.AuthorizationAuditEvent{WorkspaceID: actor.WorkspaceID, IssueID: issueID, ActorUserID: actor.UserID, Action: "task_access_control_updated", Details: map[string]any{"issue_id": issueID, "old_policy_version": current.PolicyVersion, "new_policy_version": nextVersion, "old_project_access_mode": current.ProjectAccessMode, "new_project_access_mode": request.ProjectAccessMode, "manual_grant_count": len(normalized)}}); err != nil {
		return issueAccessControlResponse{}, false, wrapProjectPermissionRepositoryError(err)
	}
	proposed.PolicyVersion = nextVersion
	return proposed, true, nil
}

func validateIssueAccessControlGrants(ctx context.Context, tx dbExecutor, workspaceID string, grants []issueAccessControlGrant) ([]issueAccessControlGrant, error) {
	now := time.Now()
	seen := map[string]struct{}{}
	result := make([]issueAccessControlGrant, 0, len(grants))
	for _, grant := range grants {
		grant.SubjectID = strings.TrimSpace(grant.SubjectID)
		if grant.Scope == "" {
			grant.Scope = projectauth.RoleScopeTask
		}
		if grant.Scope != projectauth.RoleScopeTask {
			return nil, projectauth.ErrInvalidRoleScope
		}
		if err := validateProjectlessIssueRole(ctx, tx, workspaceID, grant.Role); err != nil {
			return nil, err
		}
		if grant.ExpiresAt != nil && !grant.ExpiresAt.After(now) {
			return nil, projectauth.ErrInvalidSubject
		}
		switch grant.SubjectType {
		case projectauth.SubjectUser:
			if _, err := util.ParseUUID(grant.SubjectID); err != nil {
				return nil, projectauth.ErrInvalidSubject
			}
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM member WHERE workspace_id=$1 AND user_id=$2::uuid)`, workspaceID, grant.SubjectID).Scan(&exists); err != nil || !exists {
				return nil, projectauth.ErrInvalidSubject
			}
		case projectauth.SubjectOrganization:
			if _, err := util.ParseUUID(grant.SubjectID); err != nil {
				return nil, projectauth.ErrInvalidSubject
			}
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projectauth_organizations WHERE workspace_id=$1 AND id=$2::uuid AND status='active')`, workspaceID, grant.SubjectID).Scan(&exists); err != nil || !exists {
				return nil, projectauth.ErrInvalidSubject
			}
		case projectauth.SubjectEveryone:
			if grant.SubjectID != "" {
				return nil, projectauth.ErrInvalidSubject
			}
		default:
			return nil, projectauth.ErrInvalidSubject
		}
		key := string(grant.SubjectType) + "|" + grant.SubjectID + "|" + string(grant.Role)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, grant)
	}
	sort.Slice(result, func(i, j int) bool {
		return accessControlGrantSortKey(result[i]) < accessControlGrantSortKey(result[j])
	})
	return result, nil
}

func readIssueAccessControl(ctx context.Context, executor dbExecutor, workspaceID, issueID, projectID string, lock bool) (issueAccessControlResponse, error) {
	state := issueAccessControlResponse{WorkspaceID: workspaceID, IssueID: issueID, ProjectID: projectID, Scope: projectauth.RoleScopeTask, ProjectAccessMode: projectauth.ProjectAccessInherit, PolicyVersion: 1, Grants: []issueAccessControlGrant{}}
	query := `SELECT project_access_mode, policy_version FROM projectauth_issue_policies WHERE workspace_id=$1 AND issue_id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	err := executor.QueryRow(ctx, query, workspaceID, issueID).Scan(&state.ProjectAccessMode, &state.PolicyVersion)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return state, wrapProjectPermissionRepositoryError(err)
	}
	table := "projectauth_access_grants"
	if projectID == "" {
		table = "projectauth_issue_access_grants"
	}
	rows, err := executor.Query(ctx, `SELECT g.subject_type, g.subject_id, g.role_key, COALESCE(c.expires_at, NULL) FROM `+table+` g LEFT JOIN projectauth_grant_constraints c ON c.workspace_id=g.workspace_id AND c.grant_id=g.id WHERE g.workspace_id=$1 AND g.issue_id=$2 AND g.source='manual' ORDER BY g.subject_type,g.subject_id,g.role_key`, workspaceID, issueID)
	if err != nil {
		return state, wrapProjectPermissionRepositoryError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var grant issueAccessControlGrant
		grant.Scope = projectauth.RoleScopeTask
		if err := rows.Scan(&grant.SubjectType, &grant.SubjectID, &grant.Role, &grant.ExpiresAt); err != nil {
			return state, wrapProjectPermissionRepositoryError(err)
		}
		state.Grants = append(state.Grants, grant)
	}
	return state, wrapProjectPermissionRepositoryError(rows.Err())
}

func sameIssueAccessControl(a, b issueAccessControlResponse) bool {
	if a.ProjectAccessMode != b.ProjectAccessMode || len(a.Grants) != len(b.Grants) {
		return false
	}
	for i := range a.Grants {
		if accessControlGrantSortKey(a.Grants[i]) != accessControlGrantSortKey(b.Grants[i]) {
			return false
		}
		if (a.Grants[i].ExpiresAt == nil) != (b.Grants[i].ExpiresAt == nil) {
			return false
		}
		if a.Grants[i].ExpiresAt != nil && !a.Grants[i].ExpiresAt.Equal(*b.Grants[i].ExpiresAt) {
			return false
		}
	}
	return true
}

func accessControlGrantSortKey(g issueAccessControlGrant) string {
	return string(g.SubjectType) + "|" + g.SubjectID + "|" + string(g.Role)
}
