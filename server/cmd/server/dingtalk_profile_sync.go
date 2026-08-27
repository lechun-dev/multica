package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// syncDingTalkProfile is the host-owned profile bridge. DingTalk identity data
// remains in the isolated extension table; only safe defaults are copied into
// Multica's user profile, so a later login never overwrites user edits.
func (h *dingtalkLoginHandler) syncDingTalkProfile(ctx context.Context, user db.User, identity notify.OAuthUser) (db.User, error) {
	if h == nil || h.host == nil || h.host.Queries == nil || h.pool == nil {
		return db.User{}, errors.New("DingTalk profile sync is unavailable")
	}

	name := strings.TrimSpace(user.Name)
	profileEmail := strings.TrimSpace(identity.Email)
	if profileEmail == "" {
		profileEmail = strings.TrimSpace(user.Email)
	}
	emailLocalPart := profileEmail
	if at := strings.Index(emailLocalPart, "@"); at > 0 {
		emailLocalPart = emailLocalPart[:at]
	}
	dingTalkName := strings.TrimSpace(identity.Name)
	if dingTalkName != "" && (name == "" || strings.EqualFold(name, emailLocalPart)) {
		name = dingTalkName
	}
	avatarURL := strings.TrimSpace(identity.AvatarURL)
	if name != user.Name || (strings.TrimSpace(user.AvatarUrl.String) == "" && avatarURL != "") {
		params := db.UpdateUserParams{ID: user.ID, Name: name}
		if strings.TrimSpace(user.AvatarUrl.String) == "" && avatarURL != "" {
			params.AvatarUrl = pgtype.Text{String: avatarURL, Valid: true}
		}
		updated, err := h.host.Queries.UpdateUser(ctx, params)
		if err != nil {
			return db.User{}, fmt.Errorf("update DingTalk profile: %w", err)
		}
		user = updated
	}

	if err := h.saveDingTalkIdentityProfile(ctx, util.UUIDToString(user.ID), identity); err != nil {
		return db.User{}, err
	}
	if dingTalkName == "" || avatarURL == "" {
		slog.Warn("dingtalk login: identity profile is incomplete",
			"multica_user_id", util.UUIDToString(user.ID),
			"has_name", dingTalkName != "",
			"has_avatar", avatarURL != "")
	} else {
		slog.Info("dingtalk login: identity profile refreshed", "multica_user_id", util.UUIDToString(user.ID))
	}
	return user, nil
}

// saveDingTalkIdentityProfile refreshes an existing identity matched by any
// stable DingTalk id before attempting an insert. This repairs older records
// with empty profile fields and also handles rows created before a directory
// permission supplied ding_user_id.
func (h *dingtalkLoginHandler) saveDingTalkIdentityProfile(ctx context.Context, userID string, identity notify.OAuthUser) error {
	dingUserID := strings.TrimSpace(identity.DingUserID)
	unionID := strings.TrimSpace(identity.UnionID)
	openID := strings.TrimSpace(identity.OpenID)
	email := strings.ToLower(strings.TrimSpace(identity.Email))
	name := strings.TrimSpace(identity.Name)
	avatarURL := strings.TrimSpace(identity.AvatarURL)
	loginOnly := dingUserID == ""

	updateExisting := func() (int64, error) {
		result, err := h.pool.Exec(ctx, `
			UPDATE dingtalk_notify_identities
			SET ding_user_id = COALESCE(NULLIF($1, ''), ding_user_id),
			    union_id = COALESCE(NULLIF($2, ''), union_id),
			    open_id = COALESCE(NULLIF($3, ''), open_id),
			    email = COALESCE(NULLIF($4, ''), email),
			    name = COALESCE(NULLIF($5, ''), name),
			    avatar_url = COALESCE(NULLIF($6, ''), avatar_url),
			    multica_user_id = $7,
			    active = true,
			    login_only = CASE WHEN $1 <> '' THEN false ELSE login_only END,
			    updated_at = now()
			WHERE ($1 <> '' AND ding_user_id = $1)
			   OR ($2 <> '' AND union_id = $2)
			   OR ($3 <> '' AND open_id = $3)`,
			dingUserID, unionID, openID, email, name, avatarURL, userID)
		if err != nil {
			return 0, err
		}
		return result.RowsAffected(), nil
	}

	updated, err := updateExisting()
	if err != nil {
		return fmt.Errorf("refresh DingTalk identity: %w", err)
	}
	if updated > 0 {
		return nil
	}

	result, err := h.pool.Exec(ctx, `
		INSERT INTO dingtalk_notify_identities
		    (ding_user_id, union_id, open_id, email, name, avatar_url, multica_user_id, active, login_only, updated_at)
		VALUES (NULLIF($1, ''), NULLIF($2, ''), NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''), $7, true, $8, now())
		ON CONFLICT DO NOTHING`,
		dingUserID, unionID, openID, email, name, avatarURL, userID, loginOnly)
	if err != nil {
		return fmt.Errorf("save DingTalk identity: %w", err)
	}
	if result.RowsAffected() > 0 {
		return nil
	}

	// A concurrent login may have inserted one of the other unique identities
	// between our UPDATE and INSERT. Refresh once more instead of surfacing a
	// false conflict to the user.
	updated, err = updateExisting()
	if err != nil {
		return fmt.Errorf("refresh concurrently saved DingTalk identity: %w", err)
	}
	if updated == 0 {
		return errors.New("save DingTalk identity: stable identity conflict")
	}
	return nil
}
