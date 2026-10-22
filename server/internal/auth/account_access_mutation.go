package auth

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type AccountAccessChange struct {
	UserID        pgtype.UUID
	State         db.GetUserAccountAccessRow
	RevokedHashes []string
	Changed       bool
}

// 2026-10-20 coder(lq): Authorization belongs to the caller. Both manual and
// directory suspension share this atomic, idempotent session/PAT revocation.
// The caller commits before publishing events or invalidating token caches.
func ChangeAccountAccess(ctx context.Context, tx pgx.Tx, userID pgtype.UUID, disabled bool) (AccountAccessChange, error) {
	change := AccountAccessChange{UserID: userID}
	err := tx.QueryRow(ctx, `SELECT account_disabled_at,session_version FROM "user" WHERE id=$1 FOR UPDATE`, userID).Scan(&change.State.AccountDisabledAt, &change.State.SessionVersion)
	if err != nil {
		return change, err
	}
	if change.State.AccountDisabledAt.Valid == disabled {
		return change, nil
	}
	state, err := db.New(tx).SetUserAccountAccess(ctx, db.SetUserAccountAccessParams{ID: userID, Disabled: disabled})
	if err != nil {
		return change, err
	}
	change.State.AccountDisabledAt, change.State.SessionVersion = state.AccountDisabledAt, state.SessionVersion
	change.Changed = true
	if disabled {
		rows, err := tx.Query(ctx, `UPDATE personal_access_token SET revoked=true WHERE user_id=$1 AND NOT revoked RETURNING token_hash`, userID)
		if err != nil {
			return change, err
		}
		defer rows.Close()
		for rows.Next() {
			var hash string
			if err := rows.Scan(&hash); err != nil {
				return change, err
			}
			change.RevokedHashes = append(change.RevokedHashes, hash)
		}
		if err := rows.Err(); err != nil {
			return change, err
		}
	}
	return change, nil
}
