package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/projectauth"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	accessRequestPending   = "pending"
	accessRequestApproved  = "approved"
	accessRequestRejected  = "rejected"
	accessRequestCancelled = "cancelled"
	accessRequestExpired   = "expired"
)

type issueAccessRequestCreate struct {
	RequestedRole  projectauth.RoleKey `json:"requested_role"`
	Reason         string              `json:"reason,omitempty"`
	ExpiresAt      *time.Time          `json:"expires_at,omitempty"`
	IdempotencyKey string              `json:"idempotency_key"`
}

type issueAccessRequestReview struct {
	Action  string `json:"action"`
	Comment string `json:"comment,omitempty"`
}

// issueAccessRequestResponse intentionally excludes task body, ACL entries and
// unrelated metadata. It is safe for the requester-facing restricted screen.
type issueAccessRequestResponse struct {
	ID              string              `json:"id"`
	WorkspaceID     string              `json:"workspace_id"`
	IssueID         string              `json:"issue_id"`
	RequesterUserID string              `json:"requester_user_id"`
	RequestedRole   projectauth.RoleKey `json:"requested_role"`
	Reason          string              `json:"reason,omitempty"`
	Status          string              `json:"status"`
	ReviewerUserID  string              `json:"reviewer_user_id,omitempty"`
	ReviewComment   string              `json:"review_comment,omitempty"`
	ExpiresAt       *time.Time          `json:"expires_at,omitempty"`
	ReviewedAt      *time.Time          `json:"reviewed_at,omitempty"`
	CreatedAt       time.Time           `json:"created_at"`
	UpdatedAt       time.Time           `json:"updated_at"`
}

func decodeAccessRequestCreate(r *http.Request) (issueAccessRequestCreate, error) {
	var request issueAccessRequestCreate
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, err
	}
	request.RequestedRole = projectauth.RoleKey(strings.TrimSpace(string(request.RequestedRole)))
	request.Reason = strings.TrimSpace(request.Reason)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.RequestedRole == "" || request.IdempotencyKey == "" || len(request.IdempotencyKey) > 200 || len(request.Reason) > 2000 {
		return request, projectauth.ErrInvalidRole
	}
	if request.ExpiresAt != nil && !request.ExpiresAt.After(time.Now()) {
		return request, projectauth.ErrInvalidSubject
	}
	return request, nil
}

func decodeAccessRequestReview(r *http.Request) (issueAccessRequestReview, error) {
	var request issueAccessRequestReview
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, err
	}
	request.Action = strings.TrimSpace(request.Action)
	request.Comment = strings.TrimSpace(request.Comment)
	if (request.Action != "approve" && request.Action != "reject") || len(request.Comment) > 2000 {
		return request, errors.New("action must be approve or reject")
	}
	return request, nil
}

func (h *Handler) CreateIssueAccessRequest(w http.ResponseWriter, r *http.Request) {
	if !h.requireProjectAuthorizationEnabled(w) {
		return
	}
	request, err := decodeAccessRequestCreate(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid access request")
		return
	}
	actor, issueID, _, ok := h.issueAccessRequestActor(w, r)
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
	result, created, err := h.createIssueAccessRequest(r.Context(), tx, actor, issueID, request)
	if err != nil {
		h.writeIssueAccessRequestError(w, err)
		return
	}
	var notifications []db.InboxItem
	if created {
		notifications, err = loadAccessRequestNotifications(r.Context(), tx, result, "requested")
		if err != nil {
			writeProjectAccessGrantError(w, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeProjectAccessGrantError(w, err)
		return
	}
	h.publishAccessRequestNotifications(actor, notifications)
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, result)
}

func (h *Handler) ListIssueAccessRequests(w http.ResponseWriter, r *http.Request) {
	actor, issueID, _, ok := h.issueAccessRequestActor(w, r)
	if !ok {
		return
	}
	requesterOnly := r.URL.Query().Get("mine") == "true"
	if !requesterOnly {
		isOwner, err := h.isEffectiveIssueOwner(r.Context(), h.EffectiveIssueAccess, actor, issueID)
		if err != nil {
			writeProjectAccessGrantError(w, err)
			return
		}
		if !isOwner {
			writeProjectAccessGrantError(w, projectauth.ErrForbidden)
			return
		}
	}
	if _, err := h.DB.Exec(r.Context(), `UPDATE projectauth_access_requests SET status='expired',updated_at=now() WHERE workspace_id=$1 AND issue_id=$2 AND status='pending' AND grant_expires_at<=now()`, actor.WorkspaceID, issueID); err != nil {
		writeProjectAccessGrantError(w, wrapProjectPermissionRepositoryError(err))
		return
	}
	query := `SELECT id::text,workspace_id::text,issue_id::text,requester_user_id::text,requested_role_key,reason,status,COALESCE(reviewer_user_id::text,''),review_comment,grant_expires_at,reviewed_at,created_at,updated_at FROM projectauth_access_requests WHERE workspace_id=$1 AND issue_id=$2`
	args := []any{actor.WorkspaceID, issueID}
	if requesterOnly {
		query += ` AND requester_user_id=$3`
		args = append(args, actor.UserID)
	}
	query += ` ORDER BY created_at DESC,id DESC`
	rows, err := h.DB.Query(r.Context(), query, args...)
	if err != nil {
		writeProjectAccessGrantError(w, wrapProjectPermissionRepositoryError(err))
		return
	}
	defer rows.Close()
	items := []issueAccessRequestResponse{}
	for rows.Next() {
		item, scanErr := scanIssueAccessRequest(rows)
		if scanErr != nil {
			writeProjectAccessGrantError(w, wrapProjectPermissionRepositoryError(scanErr))
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		writeProjectAccessGrantError(w, wrapProjectPermissionRepositoryError(rows.Err()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) ReviewIssueAccessRequest(w http.ResponseWriter, r *http.Request) {
	if !h.requireProjectAuthorizationEnabled(w) {
		return
	}
	request, err := decodeAccessRequestReview(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	actor, issueID, _, ok := h.issueAccessRequestActor(w, r)
	if !ok {
		return
	}
	requestID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "requestID"), "access request id")
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
	result, err := h.reviewIssueAccessRequest(r.Context(), tx, actor, issueID, util.UUIDToString(requestID), request)
	if err != nil {
		var conflict accessRequestStateConflict
		if errors.As(err, &conflict) && conflict.status == accessRequestExpired {
			if commitErr := tx.Commit(r.Context()); commitErr != nil {
				writeProjectAccessGrantError(w, commitErr)
				return
			}
		}
		h.writeIssueAccessRequestError(w, err)
		return
	}
	notifications, err := loadAccessRequestNotifications(r.Context(), tx, result, result.Status)
	if err != nil {
		writeProjectAccessGrantError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeProjectAccessGrantError(w, err)
		return
	}
	h.publishAccessRequestNotifications(actor, notifications)
	writeJSON(w, http.StatusOK, result)
}

// 2026-10-20 coder(lq): Read the newly created event's inbox rows inside the
// transaction, but publish only after commit. Retries must not re-publish old rows.
func loadAccessRequestNotifications(ctx context.Context, tx pgx.Tx, item issueAccessRequestResponse, event string) ([]db.InboxItem, error) {
	rows, err := tx.Query(ctx, `SELECT i.* FROM inbox_item i
		JOIN projectauth_access_request_notifications n
		  ON n.workspace_id=i.workspace_id AND n.inbox_item_id=i.id
		WHERE n.workspace_id=$1 AND n.request_id=$2 AND n.event=$3`, item.WorkspaceID, item.ID, event)
	if err != nil {
		return nil, wrapProjectPermissionRepositoryError(err)
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByName[db.InboxItem])
	return items, wrapProjectPermissionRepositoryError(err)
}

func (h *Handler) publishAccessRequestNotifications(actor projectauth.Subject, items []db.InboxItem) {
	if h.Bus == nil {
		return
	}
	for _, item := range items {
		response := inboxToResponse(item)
		// 2026-10-20 coder(lq): Personal realtime routing requires a nested map
		// with recipient_id; a response struct would be silently discarded.
		h.publish(protocol.EventInboxNew, actor.WorkspaceID, "member", actor.UserID, map[string]any{"item": map[string]any{
			"id": response.ID, "workspace_id": response.WorkspaceID,
			"recipient_type": response.RecipientType, "recipient_id": response.RecipientID,
			"type": response.Type, "severity": response.Severity, "issue_id": response.IssueID,
			"title": response.Title, "body": response.Body, "read": response.Read, "archived": response.Archived,
			"created_at": response.CreatedAt, "issue_status": response.IssueStatus, "issue_priority": response.IssuePriority,
			"actor_type": response.ActorType, "actor_id": response.ActorID, "details": response.Details,
		}})
	}
}

func (h *Handler) CancelIssueAccessRequest(w http.ResponseWriter, r *http.Request) {
	if !h.requireProjectAuthorizationEnabled(w) {
		return
	}
	actor, issueID, _, ok := h.issueAccessRequestActor(w, r)
	if !ok {
		return
	}
	requestID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "requestID"), "access request id")
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
	item, err := loadIssueAccessRequest(r.Context(), tx, actor.WorkspaceID, issueID, util.UUIDToString(requestID), true)
	if err != nil {
		h.writeIssueAccessRequestError(w, err)
		return
	}
	if item.RequesterUserID != actor.UserID {
		writeProjectAccessGrantError(w, projectauth.ErrForbidden)
		return
	}
	if item.Status == accessRequestPending && item.ExpiresAt != nil && !item.ExpiresAt.After(time.Now()) {
		if err := tx.QueryRow(r.Context(), `UPDATE projectauth_access_requests SET status='expired',updated_at=now() WHERE workspace_id=$1 AND id=$2 AND status='pending' RETURNING updated_at`, actor.WorkspaceID, item.ID).Scan(&item.UpdatedAt); err != nil {
			h.writeIssueAccessRequestError(w, wrapProjectPermissionRepositoryError(err))
			return
		}
		item.Status = accessRequestExpired
	}
	if item.Status == accessRequestPending {
		if err := tx.QueryRow(r.Context(), `UPDATE projectauth_access_requests SET status='cancelled',updated_at=now() WHERE workspace_id=$1 AND id=$2 AND status='pending' RETURNING updated_at`, actor.WorkspaceID, item.ID).Scan(&item.UpdatedAt); err != nil {
			h.writeIssueAccessRequestError(w, wrapProjectPermissionRepositoryError(err))
			return
		}
		item.Status = accessRequestCancelled
		if err := (&projectAuthRepository{db: tx}).RecordAuthorizationAudit(r.Context(), projectauth.AuthorizationAuditEvent{WorkspaceID: actor.WorkspaceID, IssueID: issueID, ActorUserID: actor.UserID, Action: "task_access_request_cancelled", Details: map[string]any{"request_id": item.ID, "requested_role": item.RequestedRole}}); err != nil {
			writeProjectAccessGrantError(w, err)
			return
		}
	} else if item.Status != accessRequestCancelled && item.Status != accessRequestExpired {
		h.writeIssueAccessRequestError(w, accessRequestStateConflict{status: item.Status})
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeProjectAccessGrantError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) issueAccessRequestActor(w http.ResponseWriter, r *http.Request) (projectauth.Subject, string, string, bool) {
	issueID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "task id")
	if !ok {
		return projectauth.Subject{}, "", "", false
	}
	subject, projectID, ok := h.issueAccessSubject(w, r, util.UUIDToString(issueID))
	return subject, util.UUIDToString(issueID), projectID, ok
}

type accessRequestStateConflict struct{ status string }

func (e accessRequestStateConflict) Error() string { return "access request is " + e.status }

func (h *Handler) writeIssueAccessRequestError(w http.ResponseWriter, err error) {
	var conflict accessRequestStateConflict
	if errors.As(err, &conflict) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": conflict.Error(), "code": "access_request_state_conflict", "status": conflict.status})
		return
	}
	if errors.Is(err, projectauth.ErrInvalidRole) || errors.Is(err, projectauth.ErrInvalidSubject) {
		writeError(w, http.StatusBadRequest, "invalid access request")
		return
	}
	writeProjectAccessGrantError(w, err)
}

func (h *Handler) createIssueAccessRequest(ctx context.Context, tx pgx.Tx, actor projectauth.Subject, issueID string, request issueAccessRequestCreate) (issueAccessRequestResponse, bool, error) {
	var lockedWorkspace string
	if err := tx.QueryRow(ctx, `SELECT workspace_id::text FROM issue WHERE id=$1 FOR SHARE`, issueID).Scan(&lockedWorkspace); err != nil || lockedWorkspace != actor.WorkspaceID {
		return issueAccessRequestResponse{}, false, projectauth.ErrNoProjectAccess
	}
	if err := validateProjectlessIssueRole(ctx, tx, actor.WorkspaceID, request.RequestedRole); err != nil {
		return issueAccessRequestResponse{}, false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE projectauth_access_requests SET status='expired',updated_at=now() WHERE workspace_id=$1 AND issue_id=$2 AND requester_user_id=$3 AND requested_role_key=$4 AND status='pending' AND grant_expires_at<=now()`, actor.WorkspaceID, issueID, actor.UserID, request.RequestedRole); err != nil {
		return issueAccessRequestResponse{}, false, wrapProjectPermissionRepositoryError(err)
	}
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO projectauth_access_requests (workspace_id,issue_id,requester_user_id,requested_role_key,reason,grant_expires_at,idempotency_key) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING RETURNING id::text`, actor.WorkspaceID, issueID, actor.UserID, request.RequestedRole, request.Reason, request.ExpiresAt, request.IdempotencyKey).Scan(&id)
	created := true
	if errors.Is(err, pgx.ErrNoRows) {
		created = false
		var storedIssue, storedRequester, storedRole, storedReason string
		var storedExpiry *time.Time
		lookupErr := tx.QueryRow(ctx, `SELECT issue_id::text,requester_user_id::text,requested_role_key,reason,grant_expires_at FROM projectauth_access_requests WHERE workspace_id=$1 AND idempotency_key=$2`, actor.WorkspaceID, request.IdempotencyKey).Scan(&storedIssue, &storedRequester, &storedRole, &storedReason, &storedExpiry)
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			item, loadErr := loadPendingIssueAccessRequest(ctx, tx, actor.WorkspaceID, issueID, actor.UserID, request.RequestedRole)
			return item, false, loadErr
		}
		if lookupErr != nil {
			return issueAccessRequestResponse{}, false, wrapProjectPermissionRepositoryError(lookupErr)
		}
		if storedIssue != issueID || storedRequester != actor.UserID || storedRole != string(request.RequestedRole) || storedReason != request.Reason || !sameOptionalTime(storedExpiry, request.ExpiresAt) {
			return issueAccessRequestResponse{}, false, accessRequestStateConflict{status: "idempotency_key_conflict"}
		}
	} else if err != nil {
		return issueAccessRequestResponse{}, false, wrapProjectPermissionRepositoryError(err)
	}
	if !created {
		return loadIssueAccessRequestByIdempotency(ctx, tx, actor.WorkspaceID, request.IdempotencyKey)
	}
	item, err := loadIssueAccessRequest(ctx, tx, actor.WorkspaceID, issueID, id, false)
	if err != nil {
		return issueAccessRequestResponse{}, false, err
	}
	if err := (&projectAuthRepository{db: tx}).RecordAuthorizationAudit(ctx, projectauth.AuthorizationAuditEvent{WorkspaceID: actor.WorkspaceID, IssueID: issueID, ActorUserID: actor.UserID, Action: "task_access_request_created", Details: map[string]any{"request_id": id, "requested_role": request.RequestedRole, "expires_at": request.ExpiresAt}}); err != nil {
		return issueAccessRequestResponse{}, false, err
	}
	owners, err := h.effectiveIssueOwnerIDs(ctx, tx, actor.WorkspaceID, issueID)
	if err != nil {
		return issueAccessRequestResponse{}, false, err
	}
	for _, ownerID := range owners {
		if err := h.enqueueAccessRequestNotification(ctx, tx, item, ownerID, actor.UserID, accessRequestPending); err != nil {
			return issueAccessRequestResponse{}, false, err
		}
	}
	return item, true, nil
}

func (h *Handler) reviewIssueAccessRequest(ctx context.Context, tx pgx.Tx, actor projectauth.Subject, issueID, requestID string, review issueAccessRequestReview) (issueAccessRequestResponse, error) {
	item, err := loadIssueAccessRequest(ctx, tx, actor.WorkspaceID, issueID, requestID, true)
	if err != nil {
		return item, err
	}
	isOwner, err := h.isEffectiveIssueOwner(ctx, projectauth.NewEffectiveAccessResolver(&projectAuthRepository{db: tx}), actor, issueID)
	if err != nil {
		return item, err
	}
	if !isOwner {
		return item, projectauth.ErrForbidden
	}
	desired := accessRequestApproved
	if review.Action == "reject" {
		desired = accessRequestRejected
	}
	if item.Status == accessRequestPending && item.ExpiresAt != nil && !item.ExpiresAt.After(time.Now()) {
		if _, err := tx.Exec(ctx, `UPDATE projectauth_access_requests SET status='expired',updated_at=now() WHERE workspace_id=$1 AND id=$2 AND status='pending'`, actor.WorkspaceID, requestID); err != nil {
			return item, wrapProjectPermissionRepositoryError(err)
		}
		item.Status = accessRequestExpired
		return item, accessRequestStateConflict{status: accessRequestExpired}
	}
	if item.Status != accessRequestPending {
		return item, accessRequestStateConflict{status: item.Status}
	}
	if desired == accessRequestApproved {
		if err := h.grantApprovedAccessRequest(ctx, tx, actor, item); err != nil {
			return item, err
		}
	}
	if err := tx.QueryRow(ctx, `UPDATE projectauth_access_requests SET status=$3,reviewer_user_id=$4,review_comment=$5,reviewed_at=now(),updated_at=now() WHERE workspace_id=$1 AND id=$2 AND status='pending' RETURNING reviewer_user_id::text,review_comment,reviewed_at,updated_at`, actor.WorkspaceID, requestID, desired, actor.UserID, review.Comment).Scan(&item.ReviewerUserID, &item.ReviewComment, &item.ReviewedAt, &item.UpdatedAt); err != nil {
		return item, wrapProjectPermissionRepositoryError(err)
	}
	item.Status = desired
	if err := (&projectAuthRepository{db: tx}).RecordAuthorizationAudit(ctx, projectauth.AuthorizationAuditEvent{WorkspaceID: actor.WorkspaceID, IssueID: issueID, ActorUserID: actor.UserID, Action: "task_access_request_" + desired, Details: map[string]any{"request_id": requestID, "requester_user_id": item.RequesterUserID, "requested_role": item.RequestedRole}}); err != nil {
		return item, err
	}
	if err := h.enqueueAccessRequestNotification(ctx, tx, item, item.RequesterUserID, actor.UserID, desired); err != nil {
		return item, err
	}
	return item, nil
}

func (h *Handler) grantApprovedAccessRequest(ctx context.Context, tx pgx.Tx, actor projectauth.Subject, item issueAccessRequestResponse) error {
	if err := validateProjectlessIssueRole(ctx, tx, actor.WorkspaceID, item.RequestedRole); err != nil {
		return err
	}
	var projectID string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(project_id::text,'') FROM issue WHERE workspace_id=$1 AND id=$2`, actor.WorkspaceID, item.IssueID).Scan(&projectID); err != nil {
		return projectauth.ErrNoProjectAccess
	}
	var grantID string
	if projectID == "" {
		err := tx.QueryRow(ctx, `INSERT INTO projectauth_issue_access_grants (workspace_id,issue_id,subject_type,subject_id,role_key,source,granted_by) VALUES ($1,$2,'user',$3,$4,'manual',$5) ON CONFLICT DO NOTHING RETURNING id::text`, actor.WorkspaceID, item.IssueID, item.RequesterUserID, item.RequestedRole, actor.UserID).Scan(&grantID)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `SELECT id::text FROM projectauth_issue_access_grants WHERE workspace_id=$1 AND issue_id=$2 AND subject_type='user' AND subject_id=$3 AND role_key=$4 AND source='manual'`, actor.WorkspaceID, item.IssueID, item.RequesterUserID, item.RequestedRole).Scan(&grantID)
		}
		if err != nil {
			return wrapProjectPermissionRepositoryError(err)
		}
	} else {
		err := tx.QueryRow(ctx, `INSERT INTO projectauth_access_grants (workspace_id,project_id,issue_id,subject_type,subject_id,role_key,permission,source,granted_by) VALUES ($1,$2,$3,'user',$4,$5,NULL,'manual',$6) ON CONFLICT DO NOTHING RETURNING id::text`, actor.WorkspaceID, projectID, item.IssueID, item.RequesterUserID, item.RequestedRole, actor.UserID).Scan(&grantID)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `SELECT id::text FROM projectauth_access_grants WHERE workspace_id=$1 AND project_id=$2 AND issue_id=$3 AND subject_type='user' AND subject_id=$4 AND role_key=$5 AND permission IS NULL`, actor.WorkspaceID, projectID, item.IssueID, item.RequesterUserID, item.RequestedRole).Scan(&grantID)
		}
		if err != nil {
			return wrapProjectPermissionRepositoryError(err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO projectauth_grant_constraints (workspace_id,grant_id,expires_at,origin_kind,origin_id) VALUES ($1,$2,$3,'access_request',$4) ON CONFLICT (workspace_id,grant_id) DO UPDATE SET expires_at=EXCLUDED.expires_at,origin_kind='access_request',origin_id=EXCLUDED.origin_id,updated_at=now()`, actor.WorkspaceID, grantID, item.ExpiresAt, item.ID); err != nil {
		return wrapProjectPermissionRepositoryError(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO projectauth_issue_policies (workspace_id,issue_id,created_by,updated_by) VALUES ($1,$2,$3,$3) ON CONFLICT (workspace_id,issue_id) DO UPDATE SET policy_version=projectauth_issue_policies.policy_version+1,updated_by=EXCLUDED.updated_by,updated_at=now()`, actor.WorkspaceID, item.IssueID, actor.UserID); err != nil {
		return wrapProjectPermissionRepositoryError(err)
	}
	return (&projectAuthRepository{db: tx}).RecordAuthorizationAudit(ctx, projectauth.AuthorizationAuditEvent{WorkspaceID: actor.WorkspaceID, IssueID: item.IssueID, ActorUserID: actor.UserID, Action: "task_access_grant_created_from_request", Details: map[string]any{"request_id": item.ID, "grant_id": grantID, "role": item.RequestedRole, "scope": projectauth.RoleScopeTask, "expires_at": item.ExpiresAt}})
}

func (h *Handler) isEffectiveIssueOwner(ctx context.Context, resolver projectauth.EffectiveAccessResolver, actor projectauth.Subject, issueID string) (bool, error) {
	if resolver == nil {
		return false, projectauth.ErrStorageUnavailable
	}
	explanation, err := resolver.ExplainIssue(ctx, actor, issueID, projectauth.IssueManage)
	if err != nil {
		return false, err
	}
	return explanation.Allowed, nil
}

func (h *Handler) effectiveIssueOwnerIDs(ctx context.Context, tx pgx.Tx, workspaceID, issueID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT user_id::text,role FROM member WHERE workspace_id=$1 ORDER BY user_id`, workspaceID)
	if err != nil {
		return nil, wrapProjectPermissionRepositoryError(err)
	}
	defer rows.Close()
	type candidate struct {
		id   string
		role projectauth.WorkspaceRole
	}
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.role); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	resolver := projectauth.NewEffectiveAccessResolver(&projectAuthRepository{db: tx})
	owners := []string{}
	for _, candidate := range candidates {
		ok, resolveErr := h.isEffectiveIssueOwner(ctx, resolver, projectauth.Subject{UserID: candidate.id, WorkspaceID: workspaceID, WorkspaceRole: candidate.role}, issueID)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if ok {
			owners = append(owners, candidate.id)
		}
	}
	sort.Strings(owners)
	return owners, nil
}

func (h *Handler) enqueueAccessRequestNotification(ctx context.Context, tx pgx.Tx, item issueAccessRequestResponse, recipientID, senderID, event string) error {
	deliveryEvent := event
	if event == accessRequestPending {
		deliveryEvent = "requested"
	}
	// 2026-10-20 coder(lq): Resolve display context in the same transaction;
	// never expose internal user IDs in human-facing permission notifications.
	var applicantName, issueTitle, workspaceSlug, issuePrefix, roleName string
	var issueNumber int32
	if err := tx.QueryRow(ctx, `SELECT COALESCE(u.name,''),i.title,w.slug,w.issue_prefix,i.number,COALESCE(r.name,'')
		FROM issue i JOIN workspace w ON w.id=i.workspace_id
		LEFT JOIN "user" u ON u.id=$3::uuid
		LEFT JOIN projectauth_task_roles r ON r.workspace_id=i.workspace_id AND r.role_key=$4::text
		WHERE i.workspace_id=$1::uuid AND i.id=$2::uuid`, item.WorkspaceID, item.IssueID, item.RequesterUserID, string(item.RequestedRole)).
		Scan(&applicantName, &issueTitle, &workspaceSlug, &issuePrefix, &issueNumber, &roleName); err != nil {
		return wrapProjectPermissionRepositoryError(err)
	}
	if projectauth.IsSystemTaskRole(projectauth.TaskRole(item.RequestedRole)) {
		roleName = issueAccessRoleLabel(item.RequestedRole)
	} else if strings.TrimSpace(roleName) == "" {
		roleName = "任务访问"
	}
	notification := notify.AccessRequestNotification{
		ApplicantName: applicantName, RoleName: roleName,
		IssueIdentifier: service.IssueIdentifier(issuePrefix, issueNumber), IssueTitle: issueTitle,
	}
	title := "任务权限申请"
	severity := "action_required"
	if event != accessRequestPending {
		title = "任务权限申请结果"
		notification.Outcome = map[string]string{accessRequestApproved: "批准", accessRequestRejected: "拒绝", accessRequestCancelled: "取消", accessRequestExpired: "过期"}[event]
		severity = "info"
	}
	body := notify.AccessRequestSummary(notification)

	details, err := json.Marshal(map[string]any{"access_request_id": item.ID, "event": event, "requested_role": item.RequestedRole})
	if err != nil {
		return fmt.Errorf("marshal task access request notification details: %w", err)
	}
	var inboxID string
	err = tx.QueryRow(ctx, `WITH delivery AS (INSERT INTO projectauth_access_request_notifications (workspace_id,request_id,recipient_user_id,event,inbox_item_id) VALUES ($1,$2,$3,$4,gen_random_uuid()) ON CONFLICT (workspace_id,request_id,recipient_user_id,event) DO NOTHING RETURNING inbox_item_id) INSERT INTO inbox_item (id,workspace_id,recipient_type,recipient_id,type,severity,issue_id,title,body,actor_type,actor_id,details) SELECT inbox_item_id,$1,'member',$3,'task_access_request',$5,$6,$7,$8,'member',$9,$10::jsonb FROM delivery RETURNING id::text`, item.WorkspaceID, item.ID, recipientID, deliveryEvent, severity, item.IssueID, title+" · "+notification.IssueIdentifier+" · "+issueTitle, body, senderID, details).Scan(&inboxID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return wrapProjectPermissionRepositoryError(err)
	}
	if inboxID == "" {
		return nil
	}
	// 2026-10-20 coder(lq): A browser link focuses this request in the existing review
	// dialog even after inbox dismissal; visiting it never grants access.
	if base := strings.TrimRight(strings.TrimSpace(h.cfg.AppURL), "/"); base != "" {
		notification.SourceURL = base + "/" + url.PathEscape(workspaceSlug) + "/issues/" + url.PathEscape(notification.IssueIdentifier)
		notification.ActionURL = notification.SourceURL
		if event == accessRequestPending {
			notification.ActionURL += "?access_request=" + url.QueryEscape(item.ID)
		}
	}
	markdown := notify.FormatAccessRequestText(notification)
	// 2026-10-20 coder(lq): Persist both independent delivery intents before commit;
	// a failed/retried transaction must not lose or duplicate a robot notification.
	if err := h.enqueueAccessRequestDingTalkChannel(ctx, tx, item, recipientID, senderID, "robot", func() error {
		return enqueueAccessRequestRobot(ctx, tx, item, recipientID, event, markdown)
	}); err != nil {
		return err
	}
	return h.enqueueAccessRequestDingTalkChannel(ctx, tx, item, recipientID, senderID, "personal", func() error {
		// 2026-10-20 coder(lq): Fix UUID parameter types before converting them
		// for text identity bindings and delivery keys.
		_, err := tx.Exec(ctx, `INSERT INTO dingtalk_personal_message (
		workspace_id,comment_id,sender_user_id,sender_ding_user_id,
		sender_union_id,sender_corp_id,recipient_user_id,recipient_ding_user_id,markdown,idempotency_key)
		SELECT $1::uuid,$2::uuid,$3::uuid,sender.ding_user_id,NULLIF(sender.union_id,''),NULL,
		       $4::uuid,recipient.ding_user_id,$5::text,
		       'task-access-request:'||$2::uuid::text||':'||$4::uuid::text||':'||$6::text
		FROM LATERAL (
			SELECT COALESCE(ding_user_id,'') ding_user_id,COALESCE(union_id,'') union_id
			FROM dingtalk_notify_identities WHERE multica_user_id=$3::uuid::text AND active=true
			ORDER BY updated_at DESC LIMIT 1
		) sender CROSS JOIN LATERAL (
			SELECT ding_user_id FROM dingtalk_notify_identities
			WHERE multica_user_id=$4::uuid::text AND active=true AND login_only=false AND COALESCE(ding_user_id,'')<>''
			ORDER BY updated_at DESC LIMIT 1
		) recipient ON CONFLICT (idempotency_key) DO NOTHING`, item.WorkspaceID, item.ID, senderID, recipientID, markdown, event)
		return err
	})
}

type issueAccessRequestScanner interface{ Scan(dest ...any) error }

func scanIssueAccessRequest(row issueAccessRequestScanner) (issueAccessRequestResponse, error) {
	var item issueAccessRequestResponse
	err := row.Scan(&item.ID, &item.WorkspaceID, &item.IssueID, &item.RequesterUserID, &item.RequestedRole, &item.Reason, &item.Status, &item.ReviewerUserID, &item.ReviewComment, &item.ExpiresAt, &item.ReviewedAt, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

const issueAccessRequestSelect = `SELECT id::text,workspace_id::text,issue_id::text,requester_user_id::text,requested_role_key,reason,status,COALESCE(reviewer_user_id::text,''),review_comment,grant_expires_at,reviewed_at,created_at,updated_at FROM projectauth_access_requests`

func loadIssueAccessRequest(ctx context.Context, tx pgx.Tx, workspaceID, issueID, requestID string, lock bool) (issueAccessRequestResponse, error) {
	query := issueAccessRequestSelect + ` WHERE workspace_id=$1 AND issue_id=$2 AND id=$3`
	if lock {
		query += ` FOR UPDATE`
	}
	item, err := scanIssueAccessRequest(tx.QueryRow(ctx, query, workspaceID, issueID, requestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return item, projectauth.ErrNoProjectAccess
	}
	return item, wrapProjectPermissionRepositoryError(err)
}

func loadIssueAccessRequestByIdempotency(ctx context.Context, tx pgx.Tx, workspaceID, key string) (issueAccessRequestResponse, bool, error) {
	item, err := scanIssueAccessRequest(tx.QueryRow(ctx, issueAccessRequestSelect+` WHERE workspace_id=$1 AND idempotency_key=$2`, workspaceID, key))
	return item, false, wrapProjectPermissionRepositoryError(err)
}

func loadPendingIssueAccessRequest(ctx context.Context, tx pgx.Tx, workspaceID, issueID, requesterID string, role projectauth.RoleKey) (issueAccessRequestResponse, error) {
	item, err := scanIssueAccessRequest(tx.QueryRow(ctx, issueAccessRequestSelect+` WHERE workspace_id=$1 AND issue_id=$2 AND requester_user_id=$3 AND requested_role_key=$4 AND status='pending'`, workspaceID, issueID, requesterID, role))
	return item, wrapProjectPermissionRepositoryError(err)
}

func sameOptionalTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
