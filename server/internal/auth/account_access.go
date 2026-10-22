package auth

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var ErrSessionRevoked = errors.New("session revoked; please sign in again")

type AccountAccessReader interface {
	GetUserAccountAccess(context.Context, pgtype.UUID) (db.GetUserAccountAccessRow, error)
}

// 2026-10-19 coder(lq): Version zero accepts pre-migration sessions until the
// first account suspension. Renewal must preserve, never upgrade, this version.
func SessionVersion(claims jwt.MapClaims) (int64, error) {
	value, exists := claims["session_version"]
	if !exists {
		return 0, nil
	}
	switch n := value.(type) {
	case int64:
		if n >= 0 {
			return n, nil
		}
	case float64:
		if n >= 0 && n < math.MaxInt64 && math.Trunc(n) == n {
			return int64(n), nil
		}
	}
	return 0, ErrSessionRevoked
}

// CheckAccountAccess deliberately reads the primary database, not the token or
// membership cache: disabling an account must affect cached credentials too.
func CheckAccountAccess(ctx context.Context, reader AccountAccessReader, userID string, version *int64) error {
	state, err := readAccountAccess(ctx, reader, userID)
	if err != nil {
		return err
	}
	return checkAccountState(state, version)
}

func readAccountAccess(ctx context.Context, reader AccountAccessReader, userID string) (db.GetUserAccountAccessRow, error) {
	id, err := util.ParseUUID(userID)
	if err != nil {
		return db.GetUserAccountAccessRow{}, ErrSessionRevoked
	}
	state, err := reader.GetUserAccountAccess(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return state, ErrSessionRevoked
	}
	if err != nil {
		return state, fmt.Errorf("account access unavailable: %w", err)
	}
	if state.AccountDisabledAt.Valid || IsTemporarilyDisabledUserID(userID) {
		return state, ErrTemporarilyDisabledUser
	}
	return state, nil
}

func checkAccountState(state db.GetUserAccountAccessRow, version *int64) error {
	if version != nil && *version != state.SessionVersion {
		return ErrSessionRevoked
	}
	return nil
}

// CheckAccountToken is used only after credential authentication. Opaque tokens
// retain their own revocation rules; interactive/scoped JWTs also bind a version.
func CheckAccountToken(ctx context.Context, reader *db.Queries, userID, token string) error {
	var version *int64
	if !strings.HasPrefix(token, "mul_") && !strings.HasPrefix(token, "mat_") && !strings.HasPrefix(token, "mdt_") && !strings.HasPrefix(token, "mcn_") {
		// 2026-10-19 coder(lq): The caller already authenticated this credential.
		// An established socket may outlive its original JWT after HTTP renewal;
		// re-check revocation, not the old expiry, or active sessions get logged out.
		claims, err := parseSignedToken(token, jwt.WithoutClaimsValidation())
		if err != nil {
			return ErrSessionRevoked
		}
		v, err := SessionVersion(claims)
		if err != nil {
			return err
		}
		version = &v
	}
	state, err := readAccountAccess(ctx, reader, userID)
	if err != nil {
		return err
	}
	if err := checkAccountState(state, version); err != nil {
		return err
	}
	// 2026-10-19 coder(lq): After a suspension, an old PAT cache entry must not
	// revive a revoked token on re-enable, even if Redis invalidation failed.
	if strings.HasPrefix(token, "mul_") && state.SessionVersion > 0 {
		pat, err := reader.GetPersonalAccessTokenByHash(ctx, HashToken(token))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSessionRevoked
		}
		if err != nil {
			return fmt.Errorf("token access unavailable: %w", err)
		}
		if util.UUIDToString(pat.UserID) != userID {
			return ErrSessionRevoked
		}
	}
	return nil
}
