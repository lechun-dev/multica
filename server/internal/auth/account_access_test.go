package auth

import (
	"context"
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"testing"
	"time"
)

type accountReader struct {
	state db.GetUserAccountAccessRow
	err   error
}

func (a accountReader) GetUserAccountAccess(context.Context, pgtype.UUID) (db.GetUserAccountAccessRow, error) {
	return a.state, a.err
}

func TestAccountAccessVersions(t *testing.T) {
	const uid = "11111111-1111-4111-8111-111111111111"
	var old int64
	reader := accountReader{}
	if err := CheckAccountAccess(context.Background(), reader, uid, &old); err != nil {
		t.Fatal(err)
	}
	reader.state.AccountDisabledAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	reader.state.SessionVersion = 1
	if err := CheckAccountAccess(context.Background(), reader, uid, &old); !errors.Is(err, ErrTemporarilyDisabledUser) {
		t.Fatalf("disabled: %v", err)
	}
	reader.state.AccountDisabledAt.Valid = false
	reader.state.SessionVersion = 2
	if err := CheckAccountAccess(context.Background(), reader, uid, &old); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("old session resurrected: %v", err)
	}
	fresh := int64(2)
	if err := CheckAccountAccess(context.Background(), reader, uid, &fresh); err != nil {
		t.Fatal(err)
	}
	reader.err = errors.New("db offline")
	if err := CheckAccountAccess(context.Background(), reader, uid, &fresh); err == nil {
		t.Fatal("database failure allowed access")
	}
}
func TestRenewSessionPreservesAccountVersion(t *testing.T) {
	claims := jwt.MapClaims{"sub": "11111111-1111-4111-8111-111111111111", "session_version": int64(3)}
	token, _, err := RenewSessionToken(claims)
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := ParseSessionToken(token)
	if err != nil {
		t.Fatal(err)
	}
	version, err := SessionVersion(renewed)
	if err != nil || version != 3 {
		t.Fatalf("version=%d error=%v", version, err)
	}
}
