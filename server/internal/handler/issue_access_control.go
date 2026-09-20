package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type issueAccessNotificationTarget struct {
	UserID     string
	Role       projectauth.RoleKey
	SourceType projectauth.SubjectType
	SourceID   string
	SourceName string
}

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

// GetIssueEffectiveAccess returns the signed-in user's resolver explanation.
// It intentionally exposes no other subject's ACL and is therefore safe for
// the read-only section of the task sharing dialog.
func (h *Handler) GetIssueEffectiveAccess(w http.ResponseWriter, r *http.Request) {
	issueUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "task id")
	if !ok {
		return
	}
	issueID := util.UUIDToString(issueUUID)
	subject, _, ok := h.issueAccessSubject(w, r, issueID)
	if !ok {
		return
	}
	if h.EffectiveIssueAccess == nil {
		writeProjectAccessGrantError(w, projectauth.ErrStorageUnavailable)
		return
	}
	access, err := h.EffectiveIssueAccess.ResolveIssue(r.Context(), subject, issueID)
	if err != nil {
		writeProjectAccessGrantError(w, err)
		return
	}
	hasView := false
	for _, permission := range access.Permissions {
		if permission == projectauth.View {
			hasView = true
			break
		}
	}
	if !hasView {
		writeProjectAccessGrantError(w, projectauth.ErrForbidden)
		return
	}
	writeJSON(w, http.StatusOK, access)
}

// GetIssueAccessRequestTarget resolves either a UUID or identifier without
// disclosing task content. It exists solely so a denied detail route can show
// an access-request screen instead of falsely reporting "not found".
func (h *Handler) GetIssueAccessRequestTarget(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	var id, identifier string
	err := h.DB.QueryRow(r.Context(), `
		SELECT issue.id::text, issue.identifier
		FROM issue
		JOIN member ON member.workspace_id=issue.workspace_id AND member.user_id=$2
		WHERE issue.workspace_id=$1 AND (issue.id::text=$3 OR lower(issue.identifier)=lower($3))
		LIMIT 1`, workspaceID, userID, chi.URLParam(r, "id")).Scan(&id, &identifier)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "identifier": identifier})
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
	if !h.requireProjectAuthorizationEnabled(w) {
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
	var lockedWorkspace, lockedProject, issueTitle string
	if err := tx.QueryRow(ctx, `SELECT workspace_id::text, COALESCE(project_id::text, ''), title FROM issue WHERE id=$1 FOR UPDATE`, issueID).Scan(&lockedWorkspace, &lockedProject, &issueTitle); err != nil {
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
	if err := createIssueAccessGrantNotifications(ctx, tx, actor, issueID, issueTitle, current.Grants, normalized); err != nil {
		return issueAccessControlResponse{}, false, err
	}
	proposed.PolicyVersion = nextVersion
	return proposed, true, nil
}

// 2026-09-18 coder(lq): Expand group grants to recipients at save time so inbox messages describe the access each person actually received.
func createIssueAccessGrantNotifications(ctx context.Context, tx dbExecutor, actor projectauth.Subject, issueID, issueTitle string, before, after []issueAccessControlGrant) error {
	now := time.Now()
	beforeTargets, err := expandIssueAccessNotificationTargets(ctx, tx, actor.WorkspaceID, before, now)
	if err != nil {
		return err
	}
	afterTargets, err := expandIssueAccessNotificationTargets(ctx, tx, actor.WorkspaceID, after, now)
	if err != nil {
		return err
	}

	actorName := actor.UserID
	err = tx.QueryRow(ctx, `SELECT COALESCE(NULLIF(name,''), NULLIF(email,''), $2) FROM "user" WHERE id=$1`, actor.UserID, actor.UserID).Scan(&actorName)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return wrapProjectPermissionRepositoryError(err)
	}

	recipientIDs := make([]string, 0, len(afterTargets))
	for userID, target := range afterTargets {
		if userID == actor.UserID {
			continue
		}
		previous, existed := beforeTargets[userID]
		if existed && previous.Role == target.Role {
			continue
		}
		recipientIDs = append(recipientIDs, userID)
	}
	sort.Strings(recipientIDs)

	for _, userID := range recipientIDs {
		target := afterTargets[userID]
		_, existed := beforeTargets[userID]
		body := issueAccessNotificationBody(actorName, target, existed)
		details, err := json.Marshal(map[string]any{
			"event":            "task_access_granted",
			"role":             target.Role,
			"permission_label": issueAccessRoleLabel(target.Role),
			"source_type":      target.SourceType,
			"source_id":        target.SourceID,
			"source_name":      target.SourceName,
		})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO inbox_item (workspace_id,recipient_type,recipient_id,type,severity,issue_id,title,body,actor_type,actor_id,details) VALUES ($1,'member',$2,'task_access_granted','info',$3,$4,$5,'member',$6,$7::jsonb)`, actor.WorkspaceID, userID, issueID, issueTitle, body, actor.UserID, details); err != nil {
			return wrapProjectPermissionRepositoryError(err)
		}
	}
	return nil
}

func expandIssueAccessNotificationTargets(ctx context.Context, tx dbExecutor, workspaceID string, grants []issueAccessControlGrant, now time.Time) (map[string]issueAccessNotificationTarget, error) {
	targets := map[string]issueAccessNotificationTarget{}
	for _, grant := range grants {
		if grant.ExpiresAt != nil && !grant.ExpiresAt.After(now) {
			continue
		}
		target := issueAccessNotificationTarget{Role: grant.Role, SourceType: grant.SubjectType, SourceID: grant.SubjectID}
		var rows pgx.Rows
		var err error
		switch grant.SubjectType {
		case projectauth.SubjectUser:
			mergeIssueAccessNotificationTarget(targets, grant.SubjectID, target)
			continue
		case projectauth.SubjectOrganization:
			err = tx.QueryRow(ctx, `SELECT name FROM projectauth_organizations WHERE workspace_id=$1 AND id=$2::uuid AND status='active'`, workspaceID, grant.SubjectID).Scan(&target.SourceName)
			if err != nil {
				return nil, wrapProjectPermissionRepositoryError(err)
			}
			rows, err = tx.Query(ctx, `WITH RECURSIVE selected_orgs(id) AS (
				SELECT id FROM projectauth_organizations WHERE workspace_id=$1 AND id=$2::uuid AND status='active'
				UNION
				SELECT child.id FROM projectauth_organizations child JOIN selected_orgs parent ON child.parent_id=parent.id
				WHERE child.workspace_id=$1 AND child.status='active'
			) SELECT DISTINCT om.user_id::text FROM selected_orgs org JOIN projectauth_organization_members om ON om.organization_id=org.id AND om.workspace_id=$1 JOIN member m ON m.workspace_id=om.workspace_id AND m.user_id=om.user_id`, workspaceID, grant.SubjectID)
		case projectauth.SubjectEveryone:
			rows, err = tx.Query(ctx, `SELECT user_id::text FROM member WHERE workspace_id=$1`, workspaceID)
		default:
			continue
		}
		if err != nil {
			return nil, wrapProjectPermissionRepositoryError(err)
		}
		for rows.Next() {
			var userID string
			if err := rows.Scan(&userID); err != nil {
				rows.Close()
				return nil, wrapProjectPermissionRepositoryError(err)
			}
			mergeIssueAccessNotificationTarget(targets, userID, target)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, wrapProjectPermissionRepositoryError(err)
		}
		rows.Close()
	}
	return targets, nil
}

func mergeIssueAccessNotificationTarget(targets map[string]issueAccessNotificationTarget, userID string, candidate issueAccessNotificationTarget) {
	current, exists := targets[userID]
	if !exists || issueAccessRoleRank(candidate.Role) > issueAccessRoleRank(current.Role) ||
		(issueAccessRoleRank(candidate.Role) == issueAccessRoleRank(current.Role) && issueAccessSourceRank(candidate.SourceType) > issueAccessSourceRank(current.SourceType)) {
		targets[userID] = candidate
	}
}

func issueAccessRoleRank(role projectauth.RoleKey) int {
	switch role {
	case projectauth.RoleKey(projectauth.TaskOwner), projectauth.RoleKey(projectauth.TaskManager):
		return 3
	case projectauth.RoleKey(projectauth.TaskMember):
		return 2
	case projectauth.RoleKey(projectauth.TaskViewer):
		return 1
	default:
		return 0
	}
}

func issueAccessSourceRank(subjectType projectauth.SubjectType) int {
	switch subjectType {
	case projectauth.SubjectUser:
		return 3
	case projectauth.SubjectOrganization:
		return 2
	case projectauth.SubjectEveryone:
		return 1
	default:
		return 0
	}
}

func issueAccessRoleLabel(role projectauth.RoleKey) string {
	switch role {
	case projectauth.RoleKey(projectauth.TaskOwner), projectauth.RoleKey(projectauth.TaskManager):
		return "可管理"
	case projectauth.RoleKey(projectauth.TaskMember):
		return "可编辑"
	default:
		return "可查看"
	}
}

func issueAccessNotificationBody(actorName string, target issueAccessNotificationTarget, adjusted bool) string {
	verb := "授予你"
	if adjusted {
		verb = "将你的权限调整为"
	}
	label := issueAccessRoleLabel(target.Role)
	switch target.SourceType {
	case projectauth.SubjectOrganization:
		if adjusted {
			return fmt.Sprintf("%s通过部门「%s」%s「%s」", actorName, target.SourceName, verb, label)
		}
		return fmt.Sprintf("%s通过部门「%s」%s「%s」权限", actorName, target.SourceName, verb, label)
	case projectauth.SubjectEveryone:
		if adjusted {
			return fmt.Sprintf("%s通过「全员」%s「%s」", actorName, verb, label)
		}
		return fmt.Sprintf("%s通过「全员」%s「%s」权限", actorName, verb, label)
	default:
		if adjusted {
			return fmt.Sprintf("%s%s「%s」", actorName, verb, label)
		}
		return fmt.Sprintf("%s%s「%s」权限", actorName, verb, label)
	}
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
