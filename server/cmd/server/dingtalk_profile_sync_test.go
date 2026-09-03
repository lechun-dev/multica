package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
	"github.com/multica-ai/multica/server/internal/handler"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestDingTalkProfileSyncRefreshesExistingIdentityAndPreservesUserEdits(t *testing.T) {
	ensureDingTalkProfileSyncSchema(t)

	tests := []struct {
		name          string
		multicaName   string
		multicaAvatar string
		wantName      string
		wantAvatar    string
	}{
		{
			name:       "fills default Multica profile",
			wantName:   "张畅",
			wantAvatar: "https://example.test/dingtalk-avatar.png",
		},
		{
			name:          "preserves manually edited Multica profile",
			multicaName:   "自定义姓名",
			multicaAvatar: "https://example.test/custom-avatar.png",
			wantName:      "自定义姓名",
			wantAvatar:    "https://example.test/custom-avatar.png",
		},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stamp := fmt.Sprintf("%d-%d", time.Now().UnixNano(), index)
			emailLocalPart := "dingtalk-profile-sync-" + stamp
			email := emailLocalPart + "@example.test"
			multicaName := tt.multicaName
			if multicaName == "" {
				multicaName = emailLocalPart
			}
			dingUserID := "ding-user-" + stamp
			unionID := "union-" + stamp
			openID := "open-" + stamp

			var userID string
			if err := testPool.QueryRow(context.Background(), `
				INSERT INTO "user" (name, email, avatar_url)
				VALUES ($1, $2, NULLIF($3, ''))
				RETURNING id`, multicaName, email, tt.multicaAvatar).Scan(&userID); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = testPool.Exec(context.Background(), `DELETE FROM dingtalk_notify_identities WHERE multica_user_id = $1`, userID)
				_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID)
			})
			if _, err := testPool.Exec(context.Background(), `
				INSERT INTO dingtalk_notify_identities
				    (ding_user_id, union_id, open_id, email, multica_user_id, active, login_only, updated_at)
				VALUES ($1, $2, $3, $4, $5, true, false, now())`,
				dingUserID, unionID, openID, email, userID); err != nil {
				t.Fatal(err)
			}

			login := &dingtalkLoginHandler{
				host: &handler.Handler{Queries: db.New(testPool)},
				pool: testPool,
			}
			got, err := login.resolveUser(context.Background(), notify.OAuthUser{
				DingUserID: dingUserID,
				UnionID:    unionID,
				OpenID:     openID,
				Email:      email,
				Name:       "张畅",
				AvatarURL:  "https://example.test/dingtalk-avatar.png",
				Departments: []notify.DingTalkDepartment{
					{ID: "42", Name: "信息技术中心"},
					{ID: "43", Name: "数字化产品部"},
				},
				DepartmentsSynced: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != tt.wantName || got.AvatarUrl.String != tt.wantAvatar {
				t.Fatalf("Multica profile = name %q avatar %q, want name %q avatar %q", got.Name, got.AvatarUrl.String, tt.wantName, tt.wantAvatar)
			}

			var identityName, identityAvatar string
			if err := testPool.QueryRow(context.Background(), `
				SELECT COALESCE(name, ''), COALESCE(avatar_url, '')
				FROM dingtalk_notify_identities
				WHERE ding_user_id = $1`, dingUserID).Scan(&identityName, &identityAvatar); err != nil {
				t.Fatal(err)
			}
			if identityName != "张畅" || identityAvatar != "https://example.test/dingtalk-avatar.png" {
				t.Fatalf("DingTalk identity profile = name %q avatar %q", identityName, identityAvatar)
			}
			var departmentsJSON []byte
			var departmentsSyncedAt *time.Time
			if err := testPool.QueryRow(context.Background(), `
				SELECT departments, departments_synced_at
				FROM dingtalk_notify_identities
				WHERE ding_user_id = $1`, dingUserID).Scan(&departmentsJSON, &departmentsSyncedAt); err != nil {
				t.Fatal(err)
			}
			var departments []notify.DingTalkDepartment
			if err := json.Unmarshal(departmentsJSON, &departments); err != nil {
				t.Fatal(err)
			}
			if len(departments) != 2 || departments[0].Name != "信息技术中心" || departments[1].Name != "数字化产品部" {
				t.Fatalf("departments=%+v", departments)
			}
			if departmentsSyncedAt == nil {
				t.Fatal("departments_synced_at was not set")
			}

			// A later OAuth response can temporarily omit userId while still
			// carrying unionId/openId. That must not downgrade an existing
			// notification-capable identity to login_only.
			if _, err := login.resolveUser(context.Background(), notify.OAuthUser{
				UnionID:   unionID,
				OpenID:    openID,
				Email:     email,
				Name:      "张畅",
				AvatarURL: "https://example.test/dingtalk-avatar.png",
			}); err != nil {
				t.Fatal(err)
			}
			var loginOnly bool
			if err := testPool.QueryRow(context.Background(), `
				SELECT login_only FROM dingtalk_notify_identities WHERE ding_user_id = $1`, dingUserID).Scan(&loginOnly); err != nil {
				t.Fatal(err)
			}
			if loginOnly {
				t.Fatal("existing DingTalk identity was downgraded to login_only")
			}
			var preservedDepartmentsJSON []byte
			if err := testPool.QueryRow(context.Background(), `
				SELECT departments FROM dingtalk_notify_identities WHERE ding_user_id = $1`, dingUserID).Scan(&preservedDepartmentsJSON); err != nil {
				t.Fatal(err)
			}
			if string(preservedDepartmentsJSON) != string(departmentsJSON) {
				t.Fatalf("unsynchronized login replaced departments: got %s want %s", preservedDepartmentsJSON, departmentsJSON)
			}
		})
	}
}

func ensureDingTalkProfileSyncSchema(t *testing.T) {
	t.Helper()
	if testPool == nil {
		t.Skip("integration database is unavailable")
	}
	connConfig := *testPool.Config().ConnConfig
	connConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	schemaDB := stdlib.OpenDB(connConfig)
	t.Cleanup(func() { _ = schemaDB.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := notify.EnsureSchema(ctx, schemaDB); err != nil {
		t.Fatalf("ensure DingTalk profile schema: %v", err)
	}
}
