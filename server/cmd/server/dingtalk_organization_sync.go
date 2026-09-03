package main

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
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
	snapshot, err := h.provider.LoadDirectory(r.Context())
	if err != nil {
		writeDingTalkError(w, http.StatusBadGateway, "failed to load DingTalk directory")
		return
	}
	if len(snapshot.Departments) == 0 {
		// 2026-09-03 coder(lq): Never turn a transiently empty provider response
		// into a destructive deactivation of the last known directory snapshot.
		writeDingTalkError(w, http.StatusBadGateway, "DingTalk directory is empty")
		return
	}
	if len(snapshot.Departments) > 0 {
		validDepartments := 0
		for _, department := range snapshot.Departments {
			if strings.TrimSpace(department.ID) != "" && strings.TrimSpace(department.Name) != "" {
				validDepartments++
			}
		}
		if validDepartments == 0 {
			writeDingTalkError(w, http.StatusBadGateway, "DingTalk directory has no valid departments")
			return
		}
	}
	result := dingtalkOrganizationSyncResult{Unmatched: []string{}}
	// pgx.Tx is used by the host's transaction starter; keep the persistence
	// implementation in a helper so provider API errors never touch the DB.
	if err := h.persistDingTalkDirectory(r.Context(), workspaceID, snapshot, &result); err != nil {
		writeDingTalkError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *dingtalkLoginHandler) persistDingTalkDirectory(ctx context.Context, workspaceID string, snapshot notify.DingTalkDirectorySnapshot, result *dingtalkOrganizationSyncResult) error {
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
	for _, member := range snapshot.Members {
		userID, created, workspaceMemberCreated, err := h.resolveDingTalkDirectoryMember(ctx, tx, workspaceID, member)
		if err != nil {
			result.Unmatched = append(result.Unmatched, member.Name)
			continue
		}
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
	return tx.Commit(ctx)
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
