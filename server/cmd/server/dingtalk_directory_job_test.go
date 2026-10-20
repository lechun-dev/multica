package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/scheduler"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

func TestDingTalkDirectoryDailyPlans(t *testing.T) {
	parse := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	slot := parse("2026-10-20T19:00:00Z")
	for _, tc := range []struct {
		name, now, want string
		latest          scheduler.LatestPlanInfo
	}{
		{name: "before Beijing 03:00", now: "2026-10-20T18:59:59Z", want: "2026-10-19T19:00:00Z"},
		{name: "exact Beijing 03:00", now: "2026-10-21T03:00:00+08:00", want: "2026-10-20T19:00:00Z"},
		{name: "daytime catchup", now: "2026-10-21T14:00:00+08:00", want: "2026-10-20T19:00:00Z"},
		{name: "year boundary", now: "2027-01-01T02:00:00+08:00", want: "2026-12-30T19:00:00Z"},
		{name: "already completed", now: "2026-10-21T12:00:00+08:00", latest: scheduler.LatestPlanInfo{Found: true, PlanTime: slot, Status: "SUCCESS"}},
		{name: "running", now: "2026-10-22T12:00:00+08:00", latest: scheduler.LatestPlanInfo{Found: true, PlanTime: slot, Status: "RUNNING"}},
		{name: "retry due before next daily slot", now: "2026-10-22T12:00:00+08:00", want: "2026-10-20T19:00:00Z", latest: scheduler.LatestPlanInfo{Found: true, PlanTime: slot, Status: "FAILED", Attempt: 1, MaxAttempts: 3}},
		{name: "retry not due", now: "2026-10-21T03:01:00+08:00", latest: scheduler.LatestPlanInfo{Found: true, PlanTime: slot, Status: "FAILED", Attempt: 1, MaxAttempts: 3, NextRetryAt: slot.Add(5 * time.Minute)}},
		{name: "exhausted today", now: "2026-10-21T12:00:00+08:00", latest: scheduler.LatestPlanInfo{Found: true, PlanTime: slot, Status: "FAILED", Attempt: 3, MaxAttempts: 3}},
		{name: "exhausted yesterday", now: "2026-10-22T12:00:00+08:00", want: "2026-10-21T19:00:00Z", latest: scheduler.LatestPlanInfo{Found: true, PlanTime: slot, Status: "FAILED", Attempt: 3, MaxAttempts: 3}},
		{name: "long outage coalesced", now: "2027-01-01T12:00:00+08:00", want: "2026-12-31T19:00:00Z", latest: scheduler.LatestPlanInfo{Found: true, PlanTime: slot, Status: "SUCCESS"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plans, err := dingtalkDirectoryDailyPlans(context.Background(), scheduler.Scope{}, parse(tc.now), tc.latest)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(plans) != 0 {
					t.Fatalf("unexpected plans %v", plans)
				}
				return
			}
			if len(plans) != 1 || !plans[0].Equal(parse(tc.want)) {
				t.Fatalf("plans %v want %s", plans, tc.want)
			}
		})
	}
}

func TestDingTalkDirectoryDailyJob(t *testing.T) {
	ensureDingTalkProfileSyncSchema(t)
	for _, scenario := range []string{"complete", "departure_unavailable", "empty_directory", "no_owner", "disabled_owner", "unlinked", "owner_changed", "current_employee"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			fx := testutil.New(testPool, testWorkspaceID, testUserID)
			ws := fx.Workspace(t, "Daily directory fixture", fmt.Sprint("daily-directory-", time.Now().UnixNano()))
			fx = testutil.New(testPool, ws, testUserID)
			owner := fx.User(t, "Daily owner", fmt.Sprint(time.Now().UnixNano())+"-owner@example.test")
			role := "owner"
			if scenario == "no_owner" {
				role = "admin"
			}
			fx.Member(t, ws, owner, role)
			if scenario == "disabled_owner" {
				if _, err := testPool.Exec(ctx, `UPDATE "user" SET account_disabled_at=now() WHERE id=$1`, owner); err != nil {
					t.Fatal(err)
				}
			}
			uid := fx.User(t, "Daily departure", fmt.Sprint(time.Now().UnixNano())+"-departure@example.test")
			fx.Member(t, ws, uid, "member")
			dingID := "daily-" + uid
			fx.InsertNoID(t, "dingtalk_notify_identities", testutil.Cols{"ding_user_id": dingID, "multica_user_id": uid, "updated_at": time.Now()}, "ding_user_id=$1", dingID)
			fx.Cleanup(t, `DELETE FROM projectauth_organizations WHERE workspace_id=$1`, ws)
			fx.Cleanup(t, `DELETE FROM projectauth_organization_members WHERE workspace_id=$1`, ws)
			fx.Cleanup(t, `DELETE FROM sys_cron_executions WHERE job_name=$1 AND scope_id=$2`, dingtalkDirectoryJobName, ws)
			if scenario != "unlinked" {
				if _, err := testPool.Exec(ctx, `INSERT INTO projectauth_organizations (workspace_id,provider,external_id,name,status) VALUES ($1,'dingtalk','42','Before daily sync','active')`, ws); err != nil {
					t.Fatal(err)
				}
			}
			var directoryCalls, departureCalls, syncEvents atomic.Int32
			var departureAvailable atomic.Bool
			departureAvailable.Store(scenario != "departure_unavailable")
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/token":
					_, _ = w.Write([]byte(`{"accessToken":"fixture-app"}`))
				case "/departments":
					directoryCalls.Add(1)
					var input struct {
						DeptID int `json:"dept_id"`
					}
					_ = json.NewDecoder(r.Body).Decode(&input)
					if scenario == "empty_directory" || input.DeptID != 1 {
						_, _ = w.Write([]byte(`{"errcode":0,"result":[]}`))
					} else {
						_, _ = w.Write([]byte(`{"errcode":0,"result":[{"dept_id":42,"parent_id":1,"name":"After daily sync"}]}`))
					}
				case "/users":
					if scenario == "current_employee" {
						_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "result": map[string]any{"list": []map[string]any{{"userid": dingID, "name": "Current employee", "dept_id_list": []int{42}}}, "has_more": false}})
					} else {
						_, _ = w.Write([]byte(`{"errcode":0,"result":{"list":[],"has_more":false}}`))
					}
				case "/departures":
					departureCalls.Add(1)
					if !departureAvailable.Load() {
						w.WriteHeader(403)
						_, _ = w.Write([]byte(`{"code":"Forbidden","message":"permission missing"}`))
						return
					}
					if scenario == "owner_changed" {
						if _, err := testPool.Exec(ctx, `UPDATE member SET role='admin' WHERE workspace_id=$1 AND user_id=$2`, ws, owner); err != nil {
							t.Error(err)
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"records": []map[string]string{{"userId": dingID, "leaveTime": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}}})
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer upstream.Close()
			bus := events.New()
			bus.Subscribe("organization:synced", func(e events.Event) {
				if e.WorkspaceID != ws || e.ActorType != "system" {
					t.Errorf("bad sync event %+v", e)
				}
				syncEvents.Add(1)
			})
			host := &handler.Handler{Queries: db.New(testPool), TxStarter: testPool, Bus: bus, ProjectAuth: projectauth.New(nil, true)}
			login := &dingtalkLoginHandler{host: host, pool: testPool, provider: notify.DingTalkOAuthProvider{ClientID: "fixture", ClientSecret: "fixture", AppTokenURL: upstream.URL + "/token", DepartmentListURL: upstream.URL + "/departments", UserListURL: upstream.URL + "/users", DepartureListURL: upstream.URL + "/departures", DirectoryRequestInterval: -1}}
			job := login.directorySyncJob()
			scopes := job.Scopes
			job.Scopes = func(ctx context.Context, now time.Time) ([]scheduler.Scope, error) {
				all, err := scopes(ctx, now)
				if err != nil {
					return nil, err
				}
				for _, s := range all {
					if s.ID == ws {
						return []scheduler.Scope{s}, nil
					}
				}
				return nil, nil
			}
			managers := []*scheduler.Manager{scheduler.NewManager(testPool, scheduler.Options{RunnerID: "daily-A"}), scheduler.NewManager(testPool, scheduler.Options{RunnerID: "daily-B"})}
			for _, m := range managers {
				if err := m.Register(job); err != nil {
					t.Fatal(err)
				}
			}
			runManagersTogether(t, ctx, managers...)
			// 2026-10-20 coder(lq): A fresh process and a repeated tick cannot run a successful daily slot again.
			if err := managers[0].RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			var status string
			var attempt int
			err := testPool.QueryRow(ctx, `SELECT status,attempt FROM sys_cron_executions WHERE job_name=$1 AND scope_id=$2`, dingtalkDirectoryJobName, ws).Scan(&status, &attempt)
			if scenario == "unlinked" {
				if err == nil || directoryCalls.Load() != 0 {
					t.Fatalf("unlinked scope ran: %s calls=%d", status, directoryCalls.Load())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantStatus := "FAILED"
			if scenario == "complete" || scenario == "current_employee" {
				wantStatus = "SUCCESS"
			}
			if status != wantStatus || attempt != 1 {
				t.Fatalf("status=%s attempt=%d", status, attempt)
			}
			id, _ := util.ParseUUID(uid)
			state, err := host.Queries.GetUserAccountAccess(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if state.AccountDisabledAt.Valid != (scenario == "complete") {
				t.Fatalf("unexpected account state %+v", state)
			}
			if scenario == "no_owner" || scenario == "disabled_owner" {
				if directoryCalls.Load() != 0 || departureCalls.Load() != 0 {
					t.Fatal("unavailable owner reached upstream")
				}
				return
			}
			if scenario == "empty_directory" || scenario == "owner_changed" {
				if syncEvents.Load() != 0 {
					t.Fatal("invalid snapshot committed")
				}
				return
			}
			if syncEvents.Load() != 1 || departureCalls.Load() != 1 {
				t.Fatalf("duplicate or missing daily run: sync=%d departures=%d", syncEvents.Load(), departureCalls.Load())
			}
			if scenario == "departure_unavailable" {
				// 2026-10-20 coder(lq): The directory still refreshed, but failure is durable and retries once permission recovers.
				var name string
				if err := testPool.QueryRow(ctx, `SELECT name FROM projectauth_organizations WHERE workspace_id=$1 AND external_id='42'`, ws).Scan(&name); err != nil || name != "After daily sync" {
					t.Fatalf("directory did not refresh: %s %v", name, err)
				}
				departureAvailable.Store(true)
				if _, err := testPool.Exec(ctx, `UPDATE sys_cron_executions SET next_retry_at=now()-interval '1 second' WHERE job_name=$1 AND scope_id=$2`, dingtalkDirectoryJobName, ws); err != nil {
					t.Fatal(err)
				}
				runManagersTogether(t, ctx, managers...)
				if err := testPool.QueryRow(ctx, `SELECT status,attempt FROM sys_cron_executions WHERE job_name=$1 AND scope_id=$2`, dingtalkDirectoryJobName, ws).Scan(&status, &attempt); err != nil {
					t.Fatal(err)
				}
				state, err = host.Queries.GetUserAccountAccess(ctx, id)
				if err != nil || status != "SUCCESS" || attempt != 2 || !state.AccountDisabledAt.Valid || syncEvents.Load() != 2 {
					t.Fatalf("retry failed status=%s attempt=%d state=%+v err=%v", status, attempt, state, err)
				}
			}
		})
	}
}

func TestDingTalkDirectoryDailyRouterWiring(t *testing.T) {
	// 2026-10-20 coder(lq): Router construction must register, not start jobs;
	// missing application configuration cannot cause a provider call at boot.
	t.Setenv("DINGTALK_CLIENT_ID", "")
	t.Setenv("DINGTALK_CLIENT_SECRET", "")
	for _, enabled := range []string{"", "false"} {
		t.Run("enabled="+enabled, func(t *testing.T) {
			t.Setenv("DINGTALK_DIRECTORY_SYNC_ENABLED", enabled)
			mgr := scheduler.NewManager(testPool, scheduler.Options{})
			_, _ = NewRouterWithOptions(testPool, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil, RouterOptions{DirectoryScheduler: mgr})
			// A second registration conflicts only when the constructor registered it.
			err := mgr.Register((&dingtalkLoginHandler{}).directorySyncJob())
			if (err != nil) != (enabled == "") {
				t.Fatalf("enabled=%q registration=%v", enabled, err)
			}
			if err := mgr.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDingTalkDirectoryDailyInertWhenUnconfigured(t *testing.T) {
	for _, login := range []*dingtalkLoginHandler{nil, {}, {pool: testPool, host: &handler.Handler{}},
		{pool: testPool, host: &handler.Handler{ProjectAuth: projectauth.New(nil, false)}, provider: notify.DingTalkOAuthProvider{ClientID: "fixture", ClientSecret: "fixture"}},
	} {
		scopes, err := login.directorySyncScopes(context.Background(), time.Now())
		if err != nil || len(scopes) != 0 {
			t.Fatalf("unconfigured scheduler scopes=%v error=%v", scopes, err)
		}
	}
}
