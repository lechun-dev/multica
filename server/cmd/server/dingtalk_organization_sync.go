package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
	"github.com/multica-ai/multica/server/internal/events"
)

type dingtalkOrganizationSyncResult struct {
	OrganizationsCreated    int      `json:"organizations_created"`
	OrganizationsUpdated    int      `json:"organizations_updated"`
	OrganizationsDisabled   int      `json:"organizations_disabled"`
	MembersCreated          int      `json:"members_created"`
	MembersRemoved          int      `json:"members_removed"`
	UsersCreated            int      `json:"users_created"`
	UsersMatched            int      `json:"users_matched"`
	WorkspaceMembersCreated int      `json:"workspace_members_created"`
	Unmatched               []string `json:"unmatched"`
	DepartureSyncStatus     string   `json:"departure_sync_status"`
	AccountsDisabled        int      `json:"accounts_disabled"`
	AccountsAlreadyDisabled int      `json:"accounts_already_disabled"`
	DeparturesSkipped       int      `json:"departures_skipped"`
}

// 2026-09-03 coder(lq): Refresh the complete DingTalk directory into
// the provider-neutral authorization tables. It is deliberately owner/admin
// only and commits the old snapshot only after the provider read succeeds.
func (h *dingtalkLoginHandler) SyncDingTalkOrganizations(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.host == nil || h.pool == nil || h.host.ProjectAuth == nil || !h.host.ProjectAuth.Enabled() {
		writeDingTalkError(w, http.StatusNotFound, "project permissions are disabled")
		return
	}
	workspaceID := chi.URLParam(r, "id")
	if workspaceID == "" {
		writeDingTalkError(w, http.StatusNotFound, "workspace not found")
		return
	}
	allowed, err := h.host.CanSyncAccountAccess(r, workspaceID)
	if err != nil {
		h.writeDingTalkSyncFailure(w, r, workspaceID, http.StatusInternalServerError, err, "failed to verify account sync authority")
		return
	}
	snapshot, departed, result, err := h.loadDingTalkDirectory(r.Context(), workspaceID, allowed)
	if err != nil {
		h.writeDingTalkSyncFailure(w, r, workspaceID, http.StatusBadGateway, err, "failed to load DingTalk directory")
		return
	}
	// pgx.Tx is used by the host's transaction starter; keep the persistence
	// implementation in a helper so provider API errors never touch the DB.
	if err := h.persistDingTalkDirectory(r.Context(), workspaceID, r.Header.Get("X-User-ID"), snapshot, departed, &result); err != nil {
		h.writeDingTalkSyncFailure(w, r, workspaceID, http.StatusBadRequest, err, "failed to persist DingTalk directory")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// 2026-10-20 coder(lq): HTTP and the daily system job share provider validation
// and all-or-nothing departure reads; neither path fabricates a user request.
func (h *dingtalkLoginHandler) loadDingTalkDirectory(ctx context.Context, workspaceID string, allowed bool) (notify.DingTalkDirectorySnapshot, []string, dingtalkOrganizationSyncResult, error) {
	result := dingtalkOrganizationSyncResult{Unmatched: []string{}, DepartureSyncStatus: "owner_required"}
	snapshot, err := h.provider.LoadDirectory(ctx)
	if err != nil {
		return snapshot, nil, result, err
	}
	if len(snapshot.Departments) == 0 {
		// 2026-09-03 coder(lq): Never turn a transiently empty provider response
		// into a destructive deactivation of the last known directory snapshot.
		return snapshot, nil, result, errors.New("DingTalk directory is empty")
	}
	if len(snapshot.Departments) > 0 {
		validDepartments := 0
		for _, department := range snapshot.Departments {
			if strings.TrimSpace(department.ID) != "" && strings.TrimSpace(department.Name) != "" {
				validDepartments++
			}
		}
		if validDepartments == 0 {
			return snapshot, nil, result, errors.New("DingTalk directory has no valid departments")
		}
	}
	var departed []string
	if allowed {
		result.DepartureSyncStatus = "unavailable"
		// 2026-10-20 coder(lq): Query the lifetime of existing bound accounts in
		// bounded provider windows. All pages must succeed before any suspension.
		var since time.Time
		err := h.pool.QueryRow(ctx, `SELECT COALESCE(min(u.created_at),now()) FROM "user" u
   JOIN member m ON m.user_id=u.id
   WHERE m.workspace_id=$1 AND EXISTS (SELECT 1 FROM dingtalk_notify_identities i
    WHERE i.multica_user_id=u.id::text AND i.active=true)`, workspaceID).Scan(&since)
		if err != nil {
			return snapshot, nil, result, err
		}
		departed, err = h.provider.LoadDepartedUserIDs(ctx, since.Add(-24*time.Hour), time.Now())
		if err != nil {
			// 2026-10-20 coder(lq): Keep directory sync available, but never report departure
			// sync as successful on permission, pagination, or provider failures.
			logDingTalkDirectorySyncError(ctx, workspaceID, chimw.GetReqID(ctx), err)
			departed = nil
		} else {
			result.DepartureSyncStatus = "complete"
		}
	}
	return snapshot, departed, result, nil
}

// 2026-09-04 coder(lq): Keep every directory-sync failure correlated in logs
// and make the request ID machine-readable for clients without changing the
// existing `error` field consumed by the web toast.
func (h *dingtalkLoginHandler) writeDingTalkSyncFailure(w http.ResponseWriter, r *http.Request, workspaceID string, status int, err error, fallback string) {
	requestID := chimw.GetReqID(r.Context())
	logDingTalkDirectorySyncError(r.Context(), workspaceID, requestID, err)
	message := strings.TrimSpace(fallback)
	if err != nil {
		if errMessage := strings.TrimSpace(err.Error()); errMessage != "" {
			message = errMessage
		}
	}
	response := map[string]string{"error": message}
	if requestID != "" {
		response["request_id"] = requestID
	}
	writeJSON(w, status, response)
}

// 2026-09-04 coder(lq): Keep provider failures searchable without logging
// access tokens or member payloads. The request ID is returned to the caller
// so an operator can correlate the toast with this structured log entry.
func logDingTalkDirectorySyncError(ctx context.Context, workspaceID, requestID string, err error) {
	attrs := []any{"event", "dingtalk_directory_sync_failed", "workspace_id", workspaceID}
	if requestID != "" {
		attrs = append(attrs, "request_id", requestID)
	}
	var syncErr *notify.DingTalkDirectorySyncError
	if errors.As(err, &syncErr) {
		attrs = append(attrs, "stage", syncErr.Stage)
		if syncErr.DepartmentID != "" {
			attrs = append(attrs, "department_id", syncErr.DepartmentID)
		}
		if syncErr.ErrCode != 0 {
			attrs = append(attrs, "dingtalk_errcode", syncErr.ErrCode)
		}
		if syncErr.ErrMessage != "" {
			attrs = append(attrs, "dingtalk_errmsg", syncErr.ErrMessage)
		}
	}
	attrs = append(attrs, "error", err)
	slog.ErrorContext(ctx, "dingtalk directory sync failed", attrs...)
}

func (h *dingtalkLoginHandler) persistDingTalkDirectory(ctx context.Context, workspaceID, actorID string, snapshot notify.DingTalkDirectorySnapshot, departed []string, result *dingtalkOrganizationSyncResult) error {
	starter, ok := h.host.TxStarter.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return errors.New("DingTalk organization sync requires transaction starter")
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// 2026-10-20 coder(lq): Serialize complete snapshot replacement, including a
	// manual refresh racing the daily job, rather than interleaving memberships.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "dingtalk_directory:"+workspaceID); err != nil {
		return err
	}
	ids := make(map[string]string, len(snapshot.Departments))
	for _, dept := range snapshot.Departments {
		if strings.TrimSpace(dept.ID) == "" || strings.TrimSpace(dept.Name) == "" {
			continue
		}
		var id string
		var existed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projectauth_organizations WHERE workspace_id=$1 AND provider='dingtalk' AND external_id=$2)`, workspaceID, dept.ID).Scan(&existed); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO projectauth_organizations (workspace_id, provider, external_id, name, status) VALUES ($1,'dingtalk',$2,$3,'active') ON CONFLICT (workspace_id,provider,external_id) DO UPDATE SET name=EXCLUDED.name,status='active',updated_at=now() RETURNING id::text`, workspaceID, dept.ID, dept.Name).Scan(&id); err != nil {
			return err
		}
		ids[dept.ID] = id
		if existed {
			result.OrganizationsUpdated++
		} else {
			result.OrganizationsCreated++
		}
	}
	for _, dept := range snapshot.Departments {
		if id := ids[dept.ID]; id != "" {
			parent := ids[dept.ParentID]
			if _, err := tx.Exec(ctx, `UPDATE projectauth_organizations SET parent_id=NULLIF($1,'')::uuid, updated_at=now() WHERE id=$2::uuid`, parent, id); err != nil {
				return err
			}
		}
	}
	disabled, err := tx.Exec(ctx, `UPDATE projectauth_organizations SET status='disabled', updated_at=now() WHERE workspace_id=$1 AND provider='dingtalk' AND status='active' AND NOT (external_id = ANY($2::text[]))`, workspaceID, mapKeys(ids))
	if err != nil {
		return err
	}
	result.OrganizationsDisabled = int(disabled.RowsAffected())
	// Membership rows for a DingTalk snapshot are wholly provider-owned. Remove
	// the old set, then insert only currently matched workspace members.
	removed, err := tx.Exec(ctx, `DELETE FROM projectauth_organization_members om USING projectauth_organizations o WHERE om.organization_id=o.id AND om.workspace_id=$1 AND o.workspace_id=$1 AND o.provider='dingtalk'`, workspaceID)
	if err != nil {
		return err
	}
	result.MembersRemoved = int(removed.RowsAffected())
	currentUsers := make([]string, 0, len(snapshot.Members))
	for _, member := range snapshot.Members {
		userID, created, workspaceMemberCreated, err := h.resolveDingTalkDirectoryMember(ctx, tx, workspaceID, member)
		if err != nil {
			result.Unmatched = append(result.Unmatched, member.Name)
			continue
		}
		currentUsers = append(currentUsers, userID)
		if created {
			result.UsersCreated++
		} else {
			result.UsersMatched++
		}
		if workspaceMemberCreated {
			result.WorkspaceMembersCreated++
		}
		for _, deptID := range member.DepartmentIDs {
			orgID := ids[deptID]
			if orgID == "" {
				continue
			}
			inserted, err := tx.Exec(ctx, `INSERT INTO projectauth_organization_members (organization_id,workspace_id,user_id) VALUES ($1::uuid,$2,$3::uuid) ON CONFLICT (organization_id,user_id) DO NOTHING`, orgID, workspaceID, userID)
			if err != nil {
				return err
			}
			if inserted.RowsAffected() > 0 {
				result.MembersCreated++
			}
		}
	}

	suspension, err := h.host.SuspendDingTalkDepartures(ctx, tx, workspaceID, actorID, departed, currentUsers)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	result.AccountsDisabled = len(suspension.Changes)
	result.AccountsAlreadyDisabled = suspension.AlreadyDisabled
	result.DeparturesSkipped = suspension.Skipped
	h.host.PublishAccountSuspensions(ctx, workspaceID, actorID, suspension.Changes)
	// 2026-10-20 coder(lq): Background writes do not pass HTTP cache middleware.
	// Publish after commit so organization/permission/list generations invalidate
	// only this workspace, even when no account was suspended.
	if h.host.Bus != nil {
		h.host.Bus.Publish(events.Event{Type: "organization:synced", WorkspaceID: workspaceID,
			ActorType: "system", Payload: map[string]any{"workspace_id": workspaceID}})
	}
	slog.InfoContext(ctx, "dingtalk directory account sync completed", "workspace_id", workspaceID, "actor_id", actorID,
		"departure_sync_status", result.DepartureSyncStatus, "accounts_disabled", result.AccountsDisabled, "departures_skipped", result.DeparturesSkipped)
	return nil
}

// 2026-09-03 coder(lq): Resolve every directory person to a local account in
// the same transaction as the organization snapshot. This makes a full sync
// useful before first login while preserving the global user record on later
// directory removals.
func (h *dingtalkLoginHandler) resolveDingTalkDirectoryMember(ctx context.Context, tx pgx.Tx, workspaceID string, member notify.DingTalkDirectoryMember) (string, bool, bool, error) {
	dingID, unionID, email := strings.TrimSpace(member.DingUserID), strings.TrimSpace(member.UnionID), strings.ToLower(strings.TrimSpace(member.Email))
	if dingID == "" && unionID == "" && email == "" {
		return "", false, false, errors.New("directory member has no stable identity")
	}
	var userID string
	// Prefer an existing provider identity, then fall back to enterprise email.
	err := tx.QueryRow(ctx, `SELECT multica_user_id FROM dingtalk_notify_identities WHERE active=true AND (($1<>'' AND ding_user_id=$1) OR ($2<>'' AND union_id=$2) OR ($1='' AND $2='' AND $3<>'' AND email=$3)) LIMIT 1`, dingID, unionID, email).Scan(&userID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", false, false, err
	}
	created := false
	if errors.Is(err, pgx.ErrNoRows) {
		if email != "" {
			err = tx.QueryRow(ctx, `SELECT id::text FROM "user" WHERE lower(email)=lower($1) LIMIT 1`, email).Scan(&userID)
		}
		if err != nil || userID == "" {
			// Directory APIs may omit email; keep the account address synthetic and
			// non-routable so it cannot be mistaken for a login destination.
			accountEmail := email
			if accountEmail == "" {
				stable := dingID
				if stable == "" {
					stable = unionID
				}
				accountEmail = "dingtalk-" + stable + "@invalid.local"
			}
			name := strings.TrimSpace(member.Name)
			if name == "" {
				name = accountEmail
			}
			err = tx.QueryRow(ctx, `INSERT INTO "user" (name,email) VALUES ($1,$2) ON CONFLICT (email) DO UPDATE SET email=EXCLUDED.email RETURNING id::text`, name, accountEmail).Scan(&userID)
			if err != nil {
				return "", false, false, err
			}
			created = true
		}
	}
	// Ensure the imported person can participate in this workspace.
	memberInsert, err := tx.Exec(ctx, `INSERT INTO member (workspace_id,user_id,role) VALUES ($1,$2::uuid,'member') ON CONFLICT (workspace_id,user_id) DO NOTHING`, workspaceID, userID)
	if err != nil {
		return "", false, false, err
	}
	identityEmail := email
	if identityEmail == "" {
		var existing string
		_ = tx.QueryRow(ctx, `SELECT email FROM "user" WHERE id=$1::uuid`, userID).Scan(&existing)
		identityEmail = existing
	}
	updatedIdentity, err := tx.Exec(ctx, `UPDATE dingtalk_notify_identities
		SET multica_user_id=$5, email=COALESCE(NULLIF($3,''), email), name=COALESCE(NULLIF($4,''), name), active=true, login_only=false, updated_at=now()
		WHERE ($1<>'' AND ding_user_id=$1) OR ($2<>'' AND union_id=$2) OR ($1='' AND $2='' AND $3<>'' AND email=$3)`, dingID, unionID, identityEmail, strings.TrimSpace(member.Name), userID)
	if err != nil {
		return "", false, false, err
	}
	if updatedIdentity.RowsAffected() == 0 {
		if _, err = tx.Exec(ctx, `INSERT INTO dingtalk_notify_identities (ding_user_id,union_id,email,name,multica_user_id,active,login_only,updated_at) VALUES (NULLIF($1,''),NULLIF($2,''),NULLIF($3,''),NULLIF($4,''),$5,true,false,now()) ON CONFLICT DO NOTHING`, dingID, unionID, identityEmail, strings.TrimSpace(member.Name), userID); err != nil {
			return "", false, false, err
		}
	}
	return userID, created, memberInsert.RowsAffected() > 0, nil
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
