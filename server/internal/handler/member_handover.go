package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type handoverResource struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	ProjectID string `json:"project_id"`
	Role      string `json:"role"`
	Reassign  bool   `json:"reassign"`
	Version   string `json:"version"`
}
type memberHandoverPreview struct {
	PreviewID           string             `json:"preview_id"`
	SourceUserID        string             `json:"source_user_id"`
	RecipientUserID     string             `json:"recipient_user_id"`
	ManagerUserID       string             `json:"manager_user_id"`
	ManagerName         string             `json:"manager_name"`
	ManagerStatus       string             `json:"manager_status"`
	ManagerSyncStatus   string             `json:"manager_sync_status"`
	ManagerSuccessfulAt *time.Time         `json:"manager_successful_at,omitempty"`
	Projects            []handoverResource `json:"projects"`
	Issues              []handoverResource `json:"issues"`
	ExpiresAt           *time.Time         `json:"expires_at,omitempty"`
	inboxItem           *db.InboxItem
	checklistStatus     string
	Completed           bool `json:"completed"`
}

type handoverHTTPError struct {
	status  int
	message string
}

func (e *handoverHTTPError) Error() string             { return e.message }
func handoverInvalid(status int, message string) error { return &handoverHTTPError{status, message} }
func writeHandoverError(w http.ResponseWriter, err error) {
	var invalid *handoverHTTPError
	var pgErr *pgconn.PgError
	if errors.As(err, &invalid) {
		writeError(w, invalid.status, invalid.message)
		return
	}
	if errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01") {
		writeError(w, http.StatusConflict, "handover changed concurrently; generate a new preview")
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to prepare or complete handover")
}
func decodeHandoverInput(w http.ResponseWriter, r *http.Request, input any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(input); err != nil {
		writeError(w, 400, "invalid request body")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, 400, "invalid request body")
		return false
	}
	return true
}

// Handover is a workspace-scoped human action. It does not grant access to
// private agents, runtimes, credentials, or other workspaces.
func (h *Handler) memberHandoverAuthority(w http.ResponseWriter, r *http.Request) (string, string, string, bool) {
	workspaceID := workspaceIDFromURL(r, "id")
	requester, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return "", "", "", false
	}
	if requester.Role != "owner" || isMachineCredentialActor(r) {
		writeError(w, 403, "only human workspace owners can hand over work")
		return "", "", "", false
	}
	memberID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "memberId"), "member id")
	if !ok {
		return "", "", "", false
	}
	return workspaceID, uuidToString(requester.UserID), uuidToString(memberID), true
}

func (h *Handler) PreviewMemberHandover(w http.ResponseWriter, r *http.Request) {
	workspaceID, actorID, memberID, ok := h.memberHandoverAuthority(w, r)
	if !ok {
		return
	}
	var input struct {
		RecipientUserID string `json:"recipient_user_id"`
	}
	if !decodeHandoverInput(w, r, &input) {
		return
	}
	if input.RecipientUserID != "" {
		if _, ok := parseUUIDOrBadRequest(w, input.RecipientUserID, "recipient user id"); !ok {
			return
		}
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeHandoverError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); err != nil {
		writeHandoverError(w, err)
		return
	}
	preview := memberHandoverPreview{Projects: []handoverResource{}, Issues: []handoverResource{}, ManagerStatus: "not_synced", ManagerSyncStatus: "not_synced"}
	preview.SourceUserID, err = validateHandoverPeople(r.Context(), tx, workspaceID, memberID, actorID, "")
	if err == nil {
		err = loadHandoverManager(r.Context(), tx, workspaceID, &preview)
	}
	if err != nil {
		writeHandoverError(w, err)
		return
	}
	recipient := input.RecipientUserID
	if recipient == "" && preview.ManagerStatus == "ready" && preview.ManagerSyncStatus == "complete" && preview.ManagerUserID != preview.SourceUserID {
		recipient = preview.ManagerUserID
	}
	if recipient != "" {
		_, err = validateHandoverPeople(r.Context(), tx, workspaceID, memberID, actorID, recipient)
		if err != nil {
			var invalid *handoverHTTPError
			if input.RecipientUserID == "" && errors.As(err, &invalid) && invalid.status == http.StatusConflict {
				recipient = ""
				preview.ManagerStatus = "inactive"
			} else {
				writeHandoverError(w, err)
				return
			}
		}
	}
	preview.RecipientUserID = recipient
	preview.Projects, preview.Issues, err = loadHandoverResources(r.Context(), tx, workspaceID, preview.SourceUserID, false)
	if err != nil {
		writeHandoverError(w, err)
		return
	}
	if recipient != "" && len(preview.Projects)+len(preview.Issues) > 0 {
		expires := time.Now().UTC().Add(15 * time.Minute)
		preview.ExpiresAt = &expires
		// Store the exact resource set and versions, bound to the requesting owner.
		body, _ := json.Marshal(preview)
		err = tx.QueryRow(r.Context(), `INSERT INTO member_handovers (workspace_id,source_member_id,source_user_id,recipient_user_id,actor_id,preview,expires_at)
   VALUES ($1,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::jsonb,$7) RETURNING id::text`, workspaceID, memberID, preview.SourceUserID, recipient, actorID, body, expires).Scan(&preview.PreviewID)
		if err != nil {
			writeHandoverError(w, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeHandoverError(w, err)
		return
	}
	writeJSON(w, 200, preview)
}

func validateHandoverPeople(ctx context.Context, tx pgx.Tx, workspaceID, memberID, actorID, recipient string) (string, error) {
	var actorAllowed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM member m JOIN "user" u ON u.id=m.user_id
  WHERE m.workspace_id=$1 AND m.user_id=$2::uuid AND m.role='owner' AND u.account_disabled_at IS NULL)`, workspaceID, actorID).Scan(&actorAllowed); err != nil {
		return "", err
	}
	if !actorAllowed {
		return "", handoverInvalid(403, "workspace owner authority is no longer available")
	}
	var actorEmail string
	if err := tx.QueryRow(ctx, `SELECT email FROM "user" WHERE id=$1::uuid`, actorID).Scan(&actorEmail); err != nil {
		return "", err
	}
	if auth.IsTemporarilyDisabledUser(actorID, actorEmail) {
		return "", handoverInvalid(403, "workspace owner is blocked by instance security policy")
	}
	var sourceID, role string
	var disabled bool
	err := tx.QueryRow(ctx, `SELECT m.user_id::text,m.role,u.account_disabled_at IS NOT NULL FROM member m JOIN "user" u ON u.id=m.user_id
  WHERE m.workspace_id=$1 AND m.id=$2::uuid`, workspaceID, memberID).Scan(&sourceID, &role, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", handoverInvalid(404, "member not found")
	}
	if err != nil {
		return "", err
	}
	if sourceID == actorID || role == "owner" || !disabled {
		return "", handoverInvalid(409, "handover requires a disabled non-owner member")
	}
	if recipient != "" {
		if recipient == sourceID {
			return "", handoverInvalid(400, "recipient must differ from source")
		}
		var available bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM member m JOIN "user" u ON u.id=m.user_id
   WHERE m.workspace_id=$1 AND m.user_id=$2::uuid AND u.account_disabled_at IS NULL)`, workspaceID, recipient).Scan(&available); err != nil {
			return "", err
		}
		if !available {
			return "", handoverInvalid(409, "recipient must be an active workspace member")
		}
		var email string
		if err := tx.QueryRow(ctx, `SELECT email FROM "user" WHERE id=$1::uuid`, recipient).Scan(&email); err != nil {
			return "", err
		}
		if auth.IsTemporarilyDisabledUser(recipient, email) {
			return "", handoverInvalid(409, "recipient is blocked by the instance security policy")
		}
	}
	return sourceID, nil
}

func loadHandoverManager(ctx context.Context, tx pgx.Tx, workspaceID string, p *memberHandoverPreview) error {
	err := tx.QueryRow(ctx, `SELECT COALESCE(mu.id::text,''),COALESCE(NULLIF(mu.name,''),mu.email,''),
  CASE WHEN line.successful_at IS NULL THEN 'not_synced' WHEN line.manager_external_id='' THEN 'unset'
       WHEN mu.id IS NULL THEN 'unlinked' WHEN mu.account_disabled_at IS NOT NULL THEN 'inactive' ELSE 'ready' END,
  CASE WHEN COALESCE(a.manager_sync_failed,false) THEN 'failed' ELSE line.sync_status END,line.successful_at
  FROM directory_reporting_lines line
  LEFT JOIN member_handover_automation a ON a.workspace_id=line.workspace_id AND a.source_user_id=line.user_id
  LEFT JOIN directory_reporting_lines manager_line ON manager_line.workspace_id=line.workspace_id
   AND manager_line.provider=line.provider AND manager_line.external_user_id=line.manager_external_id
  LEFT JOIN member mm ON mm.workspace_id=line.workspace_id AND mm.user_id=manager_line.user_id
  LEFT JOIN "user" mu ON mu.id=mm.user_id
  WHERE line.workspace_id=$1 AND line.user_id=$2::uuid AND line.provider='dingtalk' LIMIT 1`, workspaceID, p.SourceUserID).
		Scan(&p.ManagerUserID, &p.ManagerName, &p.ManagerStatus, &p.ManagerSyncStatus, &p.ManagerSuccessfulAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// Only active work and explicit human responsibilities are selected. Custom
// terminal statuses are resolved through the shared lifecycle SQL function.
func loadHandoverResources(ctx context.Context, tx pgx.Tx, workspaceID, sourceID string, lock bool) ([]handoverResource, []handoverResource, error) {
	projectSQL := `SELECT p.id::text,p.title,''::text,
 CASE WHEN p.created_by=$2::uuid OR (p.lead_type='member' AND p.lead_id=$2::uuid)
  OR EXISTS (SELECT 1 FROM projectauth_access_grants g LEFT JOIN projectauth_grant_constraints c ON c.workspace_id=g.workspace_id AND c.grant_id=g.id
   WHERE g.workspace_id=p.workspace_id AND g.project_id=p.id AND g.issue_id IS NULL AND g.subject_type='user' AND g.subject_id=$2::text
    AND g.role_key='owner' AND (c.expires_at IS NULL OR c.expires_at>now()))

 THEN 'owner' ELSE 'manager' END,
 false,p.updated_at::text
 FROM project p WHERE p.workspace_id=$1 AND NOT EXISTS (SELECT 1 FROM member_handover_resources hr WHERE hr.workspace_id=p.workspace_id AND hr.source_user_id=$2::uuid AND hr.resource_type='project' AND hr.resource_id=p.id) AND p.status NOT IN ('completed','cancelled') AND (
 p.created_by=$2::uuid OR (p.lead_type='member' AND p.lead_id=$2::uuid)
 OR EXISTS (SELECT 1 FROM projectauth_access_grants g LEFT JOIN projectauth_grant_constraints c ON c.workspace_id=g.workspace_id AND c.grant_id=g.id
  WHERE g.workspace_id=p.workspace_id AND g.project_id=p.id AND g.issue_id IS NULL AND g.subject_type='user' AND g.subject_id=$2::text
   AND g.role_key IN ('owner','manager') AND (c.expires_at IS NULL OR c.expires_at>now()))) ORDER BY p.id`
	issueSQL := `SELECT i.id::text,i.title,COALESCE(i.project_id::text,''),
 CASE WHEN (i.creator_type='member' AND i.creator_id=$2::uuid) OR grants.owner THEN 'owner'
      ELSE 'manager' END,
 false,i.updated_at::text
 FROM issue i CROSS JOIN LATERAL (
  SELECT COALESCE(bool_or(g.role_key='owner'),false) AS owner, COALESCE(bool_or(g.role_key='manager'),false) AS manager
  FROM (
   SELECT g.id,g.role_key FROM projectauth_access_grants g WHERE g.workspace_id=i.workspace_id AND g.issue_id=i.id
    AND g.project_id=i.project_id AND g.subject_type='user' AND g.subject_id=$2::text AND g.role_key IN ('owner','manager')
   UNION ALL SELECT g.id,g.role_key FROM projectauth_issue_access_grants g WHERE g.workspace_id=i.workspace_id AND g.issue_id=i.id
    AND i.project_id IS NULL AND g.subject_type='user' AND g.subject_id=$2::text AND g.role_key IN ('owner','manager')
  ) g LEFT JOIN projectauth_grant_constraints c ON c.workspace_id=i.workspace_id AND c.grant_id=g.id
  WHERE c.expires_at IS NULL OR c.expires_at>now()
 ) grants
 WHERE i.workspace_id=$1 AND NOT EXISTS (SELECT 1 FROM member_handovers ch WHERE ch.workspace_id=i.workspace_id AND ch.checklist_issue_id=i.id) AND NOT EXISTS (SELECT 1 FROM member_handover_resources hr WHERE hr.workspace_id=i.workspace_id AND hr.source_user_id=$2::uuid AND hr.resource_type='issue' AND hr.resource_id=i.id) AND i.archived_at IS NULL AND issue_effective_status(i.workspace_id,i.status) NOT IN ('done','cancelled')
 AND ((i.assignee_type='member' AND i.assignee_id=$2::uuid) OR (i.creator_type='member' AND i.creator_id=$2::uuid) OR grants.owner OR grants.manager)
 ORDER BY i.id`
	if lock {
		projectSQL += " FOR UPDATE OF p"
		issueSQL += " FOR UPDATE OF i"
	}
	read := func(query string) ([]handoverResource, error) {
		rows, err := tx.Query(ctx, query, workspaceID, sourceID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		items := []handoverResource{}
		for rows.Next() {
			var item handoverResource
			if err := rows.Scan(&item.ID, &item.Title, &item.ProjectID, &item.Role, &item.Reassign, &item.Version); err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, rows.Err()
	}
	projects, err := read(projectSQL)
	if err != nil {
		return nil, nil, err
	}
	issues, err := read(issueSQL)
	return projects, issues, err
}

func (h *Handler) ExecuteMemberHandover(w http.ResponseWriter, r *http.Request) {
	workspaceID, actorID, memberID, ok := h.memberHandoverAuthority(w, r)
	if !ok {
		return
	}
	var input struct {
		PreviewID string `json:"preview_id"`
	}
	if !decodeHandoverInput(w, r, &input) {
		return
	}
	if _, ok := parseUUIDOrBadRequest(w, input.PreviewID, "preview id"); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	policy := service.ResolveIssueCountPolicy(ctx, h.Entitlements, parseUUID(workspaceID))
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeHandoverError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL SERIALIZABLE`); err != nil {
		writeHandoverError(w, err)
		return
	}
	if err = configureHandoverTransaction(ctx, tx); err != nil {
		writeHandoverError(w, err)
		return
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "member_handover:"+workspaceID+":"+memberID); err != nil {
		writeHandoverError(w, err)
		return
	}
	var body []byte
	var completed bool
	var expires time.Time
	err = tx.QueryRow(ctx, `SELECT preview,completed_at IS NOT NULL,expires_at FROM member_handovers
  WHERE id=$1::uuid AND workspace_id=$2 AND source_member_id=$3::uuid AND actor_id=$4::uuid FOR UPDATE`, input.PreviewID, workspaceID, memberID, actorID).Scan(&body, &completed, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "handover preview not found")
		return
	}
	if err != nil {
		writeHandoverError(w, err)
		return
	}
	var preview memberHandoverPreview
	if err = json.Unmarshal(body, &preview); err != nil {
		writeHandoverError(w, err)
		return
	}
	preview.PreviewID = input.PreviewID
	preview.Completed = completed
	if completed {
		writeJSON(w, 200, preview)
		return
	}
	if time.Now().After(expires) {
		writeError(w, 409, "handover preview expired; generate a new preview")
		return
	}
	var sourceID string
	sourceID, err = validateHandoverPeople(ctx, tx, workspaceID, memberID, actorID, preview.RecipientUserID)
	if err != nil {
		writeHandoverError(w, err)
		return
	}
	if sourceID != preview.SourceUserID {
		writeError(w, 409, "source member changed; generate a new preview")
		return
	}
	projects, issues, err := loadHandoverResources(ctx, tx, workspaceID, preview.SourceUserID, true)
	if err != nil {
		writeHandoverError(w, err)
		return
	}
	before, _ := json.Marshal([][]handoverResource{preview.Projects, preview.Issues})
	after, _ := json.Marshal([][]handoverResource{projects, issues})
	if !bytes.Equal(before, after) {
		writeError(w, 409, "work changed since preview; generate a new preview")
		return
	}
	if err = h.applyMemberHandover(ctx, tx, workspaceID, actorID, &preview, policy); err != nil {
		writeHandoverError(w, err)
		return
	}
	// Revalidate under short shared locks at commit. Serializable snapshots reject
	// concurrent account/role changes without locking directory rows during grants.
	if err = lockHandoverPeople(ctx, tx, []string{actorID, preview.SourceUserID, preview.RecipientUserID}); err != nil {
		writeHandoverError(w, err)
		return
	}
	if _, err = validateHandoverPeople(ctx, tx, workspaceID, memberID, actorID, preview.RecipientUserID); err != nil {
		writeHandoverError(w, err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeHandoverError(w, err)
		return
	}
	preview.Completed = true
	h.publishHandoverInbox(workspaceID, &preview)
	if h.Bus != nil {
		h.publish("member:handover", workspaceID, "member", actorID, map[string]any{"handover_id": input.PreviewID})
	}
	writeJSON(w, 200, preview)
}

func recordHandoverGrant(ctx context.Context, tx pgx.Tx, workspaceID, projectID, issueID, recipient, role, actorID, handoverID string) error {
	var id string
	if projectID != "" {
		_, err := tx.Exec(ctx, `INSERT INTO projectauth_access_grants(workspace_id,project_id,issue_id,subject_type,subject_id,role_key,source,granted_by)
   VALUES($1,$2::uuid,NULLIF($3,'')::uuid,'user',$4,$5,'system',$6::uuid) ON CONFLICT DO NOTHING`, workspaceID, projectID, issueID, recipient, role, actorID)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `SELECT id::text FROM projectauth_access_grants WHERE workspace_id=$1 AND project_id=$2::uuid
   AND issue_id IS NOT DISTINCT FROM NULLIF($3,'')::uuid AND subject_type='user' AND subject_id=$4 AND role_key=$5`, workspaceID, projectID, issueID, recipient, role).Scan(&id)
		if err != nil {
			return err
		}
	} else {
		_, err := tx.Exec(ctx, `INSERT INTO projectauth_issue_access_grants(workspace_id,issue_id,subject_type,subject_id,role_key,source,granted_by)
   VALUES($1,$2::uuid,'user',$3,$4,'system',$5::uuid) ON CONFLICT DO NOTHING`, workspaceID, issueID, recipient, role, actorID)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `SELECT id::text FROM projectauth_issue_access_grants WHERE workspace_id=$1 AND issue_id=$2::uuid AND subject_type='user' AND subject_id=$3 AND role_key=$4`, workspaceID, issueID, recipient, role).Scan(&id)
		if err != nil {
			return err
		}
	}
	// The receiver needs a durable grant even when an identical older share expired.
	_, err := tx.Exec(ctx, `INSERT INTO projectauth_grant_constraints(workspace_id,grant_id,origin_kind,origin_id)
  VALUES($1,$2::uuid,'system',$3::uuid) ON CONFLICT(workspace_id,grant_id) DO UPDATE SET
  expires_at=NULL,origin_kind='system',origin_id=EXCLUDED.origin_id,updated_at=now()`, workspaceID, id, handoverID)
	return err
}
