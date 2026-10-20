package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

func TestDingTalkDepartureOrganizationSync(t *testing.T) {
	ensureDingTalkProfileSyncSchema(t)
	for _, scenario := range []string{"complete", "permission_denied", "partial_page_failure", "admin_only", "empty_departures"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			fx := testutil.New(testPool, testWorkspaceID, testUserID)
			ws := fx.Workspace(t, "Departure sync fixture", "departure-sync-"+fmt.Sprint(time.Now().UnixNano()))
			fx = testutil.New(testPool, ws, testUserID)
			role := "owner"
			if scenario == "admin_only" {
				role = "admin"
			}
			fx.Member(t, ws, testUserID, role)
			uid := fx.User(t, "Directory departure", fmt.Sprint(time.Now().UnixNano())+"-departure@example.test")
			fx.Member(t, ws, uid, "member")
			dingID := "departure-sync-" + uid
			fx.InsertNoID(t, "dingtalk_notify_identities", testutil.Cols{"ding_user_id": dingID, "multica_user_id": uid, "updated_at": time.Now()}, "ding_user_id=$1", dingID)
			fx.Cleanup(t, `DELETE FROM projectauth_organizations WHERE workspace_id=$1`, ws)
			fx.Cleanup(t, `DELETE FROM projectauth_organization_members WHERE workspace_id=$1`, ws)
			departureCalls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/token":
					_, _ = w.Write([]byte(`{"accessToken":"application-only-token"}`))
				case "/departments":
					var input struct {
						DeptID int `json:"dept_id"`
					}
					_ = json.NewDecoder(r.Body).Decode(&input)
					if input.DeptID == 1 {
						_, _ = w.Write([]byte(`{"errcode":0,"result":[{"dept_id":42,"parent_id":1,"name":"Directory fixture"}]}`))
					} else {
						_, _ = w.Write([]byte(`{"errcode":0,"result":[]}`))
					}
				case "/users":
					_, _ = w.Write([]byte(`{"errcode":0,"result":{"list":[],"has_more":false}}`))
				case "/departures":
					departureCalls++
					if r.Header.Get("x-acs-dingtalk-access-token") != "application-only-token" {
						t.Error("not using application authentication")
					}
					if scenario == "permission_denied" || (scenario == "partial_page_failure" && departureCalls > 1) {
						w.WriteHeader(403)
						_, _ = w.Write([]byte(`{"code":"Forbidden.AccessDenied","message":"app permission missing"}`))
						return
					}
					if scenario == "empty_departures" {
						_, _ = w.Write([]byte(`{"records":[]}`))
						return
					}
					next := ""
					if scenario == "partial_page_failure" {
						next = "next-page"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"records": []map[string]string{{"userId": dingID, "leaveTime": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}}, "nextToken": next})
				default:
					t.Errorf("unexpected upstream path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer upstream.Close()
			host := &handler.Handler{Queries: db.New(testPool), TxStarter: testPool, Bus: events.New(), ProjectAuth: projectauth.New(nil, true)}
			login := &dingtalkLoginHandler{host: host, pool: testPool, provider: notify.DingTalkOAuthProvider{ClientID: "fixture", ClientSecret: "fixture", AppTokenURL: upstream.URL + "/token", DepartmentListURL: upstream.URL + "/departments", UserListURL: upstream.URL + "/users", DepartureListURL: upstream.URL + "/departures", DirectoryRequestInterval: -1}}
			req := httptest.NewRequest(http.MethodPost, "/api/workspaces/"+ws+"/projectauth/organizations/sync", nil)
			req.Header.Set("X-User-ID", testUserID)
			route := chi.NewRouteContext()
			route.URLParams.Add("id", ws)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
			var result dingtalkOrganizationSyncResult
			testutil.Call(t, login.SyncDingTalkOrganizations, req).Want(http.StatusOK).JSON(&result)
			expected := "unavailable"
			if scenario == "complete" || scenario == "empty_departures" {
				expected = "complete"
			}
			if scenario == "admin_only" {
				expected = "owner_required"
				if departureCalls != 0 {
					t.Fatal("admin triggered account access query")
				}
			}
			if result.DepartureSyncStatus != expected || result.OrganizationsCreated != 1 {
				t.Fatalf("sync=%+v", result)
			}
			id, err := util.ParseUUID(uid)
			if err != nil {
				t.Fatal(err)
			}
			state, err := host.Queries.GetUserAccountAccess(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			disabled := scenario == "complete"
			if state.AccountDisabledAt.Valid != disabled || result.AccountsDisabled != map[bool]int{true: 1, false: 0}[disabled] {
				t.Fatalf("state=%+v sync=%+v", state, result)
			}
		})
	}
}
