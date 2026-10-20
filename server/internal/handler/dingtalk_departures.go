package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// 2026-10-20 coder(lq): An organization administrator may sync departments,
// but that must not become a back door into owner-only global suspension.
func (h *Handler) CanSyncAccountAccess(r *http.Request, workspaceID string) (bool, error) {
	if isMachineCredentialActor(r) {
		return false, nil
	}
	workspace, err := util.ParseUUID(workspaceID)
	if err != nil {
		return false, err
	}
	actor, err := util.ParseUUID(requestUserID(r))
	if err != nil {
		return false, err
	}
	member, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{UserID: actor, WorkspaceID: workspace})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return member.Role == "owner", nil
}

type DingTalkSuspensionResult struct {
	Changes         []auth.AccountAccessChange
	Skipped         int
	AlreadyDisabled int
}

// 2026-10-20 coder(lq): Match only an existing active provider binding, never
// names or email. Absence from a directory snapshot is NOT proof of departure.
// Rejoined people (including a new DingTalk ID bound to the same local user)
// are excluded. Commit/rollback belongs to the directory snapshot transaction.
func (h *Handler) SuspendDingTalkDepartures(ctx context.Context, tx pgx.Tx, workspaceID, actorID string, departed, currentUsers []string) (DingTalkSuspensionResult, error) {
	result := DingTalkSuspensionResult{}
	if len(departed) == 0 {
		return result, nil
	}
	workspace, err := util.ParseUUID(workspaceID)
	if err != nil {
		return result, err
	}
	actor, err := util.ParseUUID(actorID)
	if err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, `SELECT i.ding_user_id,i.multica_user_id
  FROM dingtalk_notify_identities i JOIN member m ON m.user_id::text=i.multica_user_id
  WHERE m.workspace_id=$1 AND i.active=true AND i.ding_user_id=ANY($2::text[])
  ORDER BY i.ding_user_id,i.multica_user_id FOR UPDATE OF i`, workspace, departed)
	if err != nil {
		return result, err
	}
	matches := map[string]map[string]bool{}
	for rows.Next() {
		var dingID, userID string
		if err := rows.Scan(&dingID, &userID); err != nil {
			rows.Close()
			return result, err
		}
		if matches[dingID] == nil {
			matches[dingID] = map[string]bool{}
		}
		matches[dingID][userID] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	activeUsers := map[string]bool{}
	for _, userID := range currentUsers {
		activeUsers[userID] = true
	}

	candidates := map[string]bool{}
	seen := map[string]bool{}
	for _, dingID := range departed {
		if seen[dingID] {
			continue
		}
		seen[dingID] = true
		if len(matches[dingID]) != 1 {
			result.Skipped++
			continue
		}
		for userID := range matches[dingID] {
			if activeUsers[userID] {
				result.Skipped++
				continue
			}
			candidates[userID] = true
		}
	}
	if len(candidates) == 0 {
		return result, nil
	}
	ids := []string{}
	if !candidates[actorID] {
		ids = append(ids, actorID)
	}
	for id := range candidates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	// 2026-10-20 coder(lq): Match manual suspension/owner-promotion lock order.
	for _, query := range []string{
		`SELECT id FROM "user" WHERE id::text=ANY($1::text[]) ORDER BY id FOR UPDATE`,
		`SELECT id FROM member WHERE user_id::text=ANY($1::text[]) ORDER BY id FOR UPDATE`,
	} {
		rows, err := tx.Query(ctx, query, ids)
		if err != nil {
			return result, err
		}
		for rows.Next() {
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return result, err
		}
	}
	qtx := h.Queries.WithTx(tx)
	// 2026-10-20 coder(lq): A slow provider read may outlive account revocation.
	// Recheck the authority account under the same locks as the target mutation.
	actorUser, err := qtx.GetUser(ctx, actor)
	if err != nil {
		return result, err
	}
	if actorUser.AccountDisabledAt.Valid || auth.IsTemporarilyDisabledUser(actorID, actorUser.Email) {
		result.Skipped += len(candidates)
		return result, nil
	}

	allowed, err := qtx.ListManageableAccounts(ctx, db.ListManageableAccountsParams{WorkspaceID: workspace, ActorID: actor})
	if err != nil {
		return result, err
	}
	manageable := map[string]bool{}
	for _, id := range allowed {
		manageable[util.UUIDToString(id)] = true
	}
	for _, id := range ids {
		if !candidates[id] {
			continue
		}
		if !manageable[id] {
			result.Skipped++
			continue
		}
		userID, err := util.ParseUUID(id)
		if err != nil {
			return result, err
		}
		user, err := qtx.GetUser(ctx, userID)
		if err != nil {
			return result, err
		}
		if user.AccountDisabledAt.Valid || auth.IsTemporarilyDisabledUser(id, user.Email) {
			result.AlreadyDisabled++
			continue
		}
		change, err := auth.ChangeAccountAccess(ctx, tx, userID, true)
		if err != nil {
			return result, err
		}
		if change.Changed {
			result.Changes = append(result.Changes, change)
		}
	}
	return result, nil
}

// 2026-10-20 coder(lq): Publish only after commit. Authentication and every
// replica's existing socket checks enforce revocation independently of events.
func (h *Handler) PublishAccountSuspensions(ctx context.Context, workspaceID, actorID string, changes []auth.AccountAccessChange) {
	for _, change := range changes {
		for _, hash := range change.RevokedHashes {
			h.PATCache.Invalidate(ctx, hash)
		}
		id := util.UUIDToString(change.UserID)
		h.publish(protocol.EventMemberUpdated, workspaceID, "member", actorID, map[string]any{"user_id": id})
		if h.Hub != nil {
			payload, _ := json.Marshal(map[string]any{"type": "account:disabled", "payload": map[string]string{"user_id": id}})
			h.Hub.SendToUser(id, payload)
		}
	}
}
