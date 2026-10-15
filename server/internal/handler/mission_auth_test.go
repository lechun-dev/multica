package handler

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestMissionIdentityExchangeUsesExistingBindings(t *testing.T) {
	var exists bool
	dbfx.QueryRow(t, "SELECT to_regclass('dingtalk_notify_identities') IS NOT NULL").Scan(&exists)
	if !exists {
		t.Skip("optional DingTalk identity extension not installed in test database")
	}
	bind := func(t *testing.T, user string, active bool) string {
		t.Helper()
		ding := fmt.Sprintf("mission-test-%s-%d", user, time.Now().UnixNano())
		dbfx.InsertNoID(t, "dingtalk_notify_identities", testutil.Cols{
			"ding_user_id": ding, "multica_user_id": user, "active": active,
			"login_only": true, "updated_at": time.Now(),
		}, "ding_user_id = $1", ding)
		return ding
	}
	member := createPlainMember(t, "mission-exchange-member")
	outsider := dbfx.User(t, "Mission outsider", fmt.Sprintf("mission-outsider-%d@example.test", time.Now().UnixNano()))
	active := bind(t, member, true)
	for _, tc := range []struct {
		name, ding string
		status     int
	}{
		{"active workspace member", active, http.StatusOK},
		{"workspace owner", bind(t, testUserID, true), http.StatusOK},
		{"inactive mapping", bind(t, member, false), http.StatusForbidden},
		{"nonmember", bind(t, outsider, true), http.StatusForbidden},
		{"unbound", "mission-test-not-bound", http.StatusForbidden},
		{"invalid mapping", bind(t, "not-a-uuid", true), http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := testutil.JSONRequest(http.MethodPost, "/internal/auth/dingtalk/exchange", map[string]string{
				"ding_user_id": tc.ding, "workspace_id": testWorkspaceID,
			})
			response := testutil.Call(t, testHandler.ExchangeDingTalkReadToken, req).Want(tc.status)
			if tc.status != http.StatusOK {
				return
			}
			out := response.Map()
			if out["expires_in"] != float64(300) || out["workspace_id"] != testWorkspaceID || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("exchange contract = %v", out)
			}
			parsed, err := jwt.Parse(out["access_token"].(string), func(token *jwt.Token) (any, error) { return auth.JWTSecret(), nil }, jwt.WithValidMethods([]string{"HS256"}))
			if err != nil {
				t.Fatal(err)
			}
			claims := parsed.Claims.(jwt.MapClaims)
			if claims["source"] != auth.MissionReadSource || claims["workspace_id"] != testWorkspaceID || claims["sid"] != nil {
				t.Fatalf("unscoped or browser-session claims: %v", claims)
			}
			if tc.ding == active && (claims["sub"] != member || out["role"] != "member") {
				t.Fatalf("wrong bound identity/role: %v", out)
			}
		})
	}
	for _, body := range []string{
		`{"ding_user_id":"test","workspace_id":"invalid"}`,
		`{"ding_user_id":"test","workspace_id":"` + testWorkspaceID + `","user_id":"forged"}`,
		`{"ding_user_id":"test","workspace_id":"` + testWorkspaceID + `"} {}`,
	} {
		testutil.Call(t, testHandler.ExchangeDingTalkReadToken, testutil.JSONRequest(http.MethodPost, "/internal/auth/dingtalk/exchange", body)).Want(http.StatusBadRequest)
	}
}
