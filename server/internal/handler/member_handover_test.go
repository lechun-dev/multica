package handler

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func handoverRequest(t *testing.T, actor, ws, member string, execute bool, body any) *testutil.Response {
	t.Helper()
	path := "/api/workspaces/" + ws + "/members/" + member + "/handover"
	fn := testHandler.ExecuteMemberHandover
	if !execute {
		path += "/preview"
		fn = testHandler.PreviewMemberHandover
	}
	req := newRequestAs(actor, http.MethodPost, path, body)
	req.Header.Set("X-Workspace-ID", ws)
	return testutil.Call(t, fn, withURLParams(req, "id", ws, "memberId", member))
}

func TestManualHandoverStatusDoesNotRequireOrGrantAutomaticEligibility(t *testing.T) {
	for _, evidence := range []string{"absent", "ineligible", "eligible"} {
		t.Run(evidence, func(t *testing.T) {
			fx, ws, source, member, recipient := handoverFixture(t)
			ctx := context.Background()
			fx.Issue(t, "Manual transfer", testutil.Cols{"creator_id": source, "assignee_type": "member", "assignee_id": source})
			if evidence != "absent" {
				fx.InsertNoID(t, "member_handover_automation", testutil.Cols{"workspace_id": ws, "source_user_id": source, "eligible": evidence == "eligible", "status": "pending", "reason": "manager_unlinked", "attempted_at": time.Now().Add(-time.Hour)}, "workspace_id=$1 AND source_user_id=$2", ws, source)
			}
			var preview memberHandoverPreview
			handoverRequest(t, testUserID, ws, member, false, map[string]any{}).Want(200).JSON(&preview)
			handoverRequest(t, testUserID, ws, member, true, map[string]any{"preview_id": preview.PreviewID}).Want(200)
			readStatus := func(actor string) map[string]*memberHandoverStatus {
				t.Helper()
				tx, err := testHandler.TxStarter.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(ctx)
				statuses, err := loadMemberHandoverStatuses(ctx, tx, ws, actor)
				if err != nil {
					t.Fatal(err)
				}
				return statuses
			}
			status := readStatus(testUserID)[source]
			if status == nil || status.Status != "completed" || status.Reason != "" || status.RecipientName != "Leader" || status.AttemptedAt == nil || status.NotificationStatus != "pending" {
				t.Fatalf("manual success missing or misreported: %+v", status)
			}
			if evidence == "absent" && fx.Count(t, `SELECT count(*) FROM member_handover_automation WHERE workspace_id=$1`, ws) != 0 {
				t.Fatal("manual transfer manufactured departure evidence")
			}
			if evidence == "ineligible" && fx.Count(t, `SELECT count(*) FROM member_handover_automation WHERE workspace_id=$1 AND eligible`, ws) != 0 {
				t.Fatal("manual transfer re-enabled automatic eligibility")
			}
			if len(readStatus(recipient)) != 0 {
				t.Fatal("ordinary member received handover diagnostics")
			}
			// A later verified daily run must retain the completed label and not
			// reassign the same resources or create another notification batch.
			verifyHandoverEvidence(t, ws, []string{source}, nil)
			result, err := testHandler.RunAutomaticMemberHandovers(ctx, ws, testUserID)
			if err != nil || result.Completed != 0 || result.NoWork != 1 {
				t.Fatalf("manual success was transferred again: %+v %v", result, err)
			}
			if status = readStatus(testUserID)[source]; status == nil || status.Status != "completed" {
				t.Fatalf("daily check lost completed manual status: %+v", status)
			}
			if fx.Count(t, `SELECT count(*) FROM member_handovers WHERE workspace_id=$1 AND completed_at IS NOT NULL`, ws) != 1 || fx.Count(t, `SELECT count(*) FROM member_handover_notifications WHERE workspace_id=$1`, ws) != 1 {
				t.Fatal("daily check duplicated handover or notification intent")
			}
			fx.Exec(t, `UPDATE member_handover_automation SET status='failed',reason='execution_failed',attempted_at=now()+interval '1 minute' WHERE workspace_id=$1 AND source_user_id=$2`, ws, source)
			if status = readStatus(testUserID)[source]; status == nil || status.Status != "failed" || status.Reason != "execution_failed" {
				t.Fatalf("historical success concealed a newer failure: %+v", status)
			}
			fx.Exec(t, `UPDATE "user" SET account_disabled_at=NULL WHERE id=$1`, source)
			if status = readStatus(testUserID)[source]; status != nil {
				t.Fatalf("active employee retained departure diagnostics: %+v", status)
			}
		})
	}
}

func handoverFixture(t *testing.T) (*testutil.Fixture, string, string, string, string) {
	t.Helper()
	if testHandler == nil {
		t.Skip("database not available")
	}
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(handoverTestLogWriter{t}, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	ws := dbfx.Workspace(t, "Handover fixture", "handover-"+time.Now().Format("150405.000000000"))
	fx := testutil.New(dbfx.Pool, ws, testUserID)
	fx.Member(t, ws, testUserID, "owner")
	source := fx.User(t, "Departed", ws+"-source@example.test", testutil.Cols{"account_disabled_at": time.Now()})
	member := fx.Member(t, ws, source, "member")
	recipient := fx.User(t, "Leader", ws+"-leader@example.test")
	fx.Member(t, ws, recipient, "member")
	// Only fixture-owned rows created by the endpoint are removed by these tests.
	fx.Cleanup(t, `DELETE FROM dingtalk_notify_outbox WHERE workspace_id=$1`, ws)
	fx.Cleanup(t, `DELETE FROM member_handover_notifications WHERE workspace_id=$1`, ws)
	fx.Cleanup(t, `DELETE FROM member_handover_resources WHERE workspace_id=$1`, ws)
	fx.Cleanup(t, `DELETE FROM member_handover_automation WHERE workspace_id=$1`, ws)
	fx.Cleanup(t, `DELETE FROM member_handovers WHERE workspace_id=$1`, ws)
	fx.Cleanup(t, `DELETE FROM issue WHERE workspace_id=$1 AND id IN (SELECT checklist_issue_id FROM member_handovers WHERE workspace_id=$1)`, ws)
	fx.Cleanup(t, `DELETE FROM inbox_item WHERE workspace_id=$1`, ws)
	fx.Cleanup(t, `DELETE FROM projectauth_grant_constraints WHERE workspace_id=$1`, ws)
	fx.Cleanup(t, `DELETE FROM projectauth_access_grants WHERE workspace_id=$1`, ws)
	fx.Cleanup(t, `DELETE FROM projectauth_issue_access_grants WHERE workspace_id=$1`, ws)
	fx.Cleanup(t, `DELETE FROM project_members WHERE project_id IN (SELECT project_id FROM projectauth_access_grants WHERE workspace_id=$1)`, ws)
	fx.InsertNoID(t, "directory_reporting_lines", testutil.Cols{"workspace_id": ws, "user_id": source, "provider": "dingtalk", "external_user_id": "source", "manager_external_id": "0201243712852502", "sync_status": "complete", "successful_at": time.Now()}, "workspace_id=$1 AND user_id=$2", ws, source)
	fx.InsertNoID(t, "directory_reporting_lines", testutil.Cols{"workspace_id": ws, "user_id": recipient, "provider": "dingtalk", "external_user_id": "0201243712852502", "sync_status": "complete", "successful_at": time.Now()}, "workspace_id=$1 AND user_id=$2", ws, recipient)
	return fx, ws, source, member, recipient
}

func TestMemberHandoverDefaultManagerAtomicAndIdempotent(t *testing.T) {
	fx, ws, source, member, recipient := handoverFixture(t)
	project := fx.Project(t, "Unfinished", testutil.Cols{"created_by": source, "lead_type": "member", "lead_id": source})
	issue := fx.Issue(t, "Projectless work", testutil.Cols{"creator_id": source, "assignee_type": "member", "assignee_id": source})
	fx.Issue(t, "Finished", testutil.Cols{"creator_id": source, "assignee_type": "member", "assignee_id": source, "status": "done"})
	fx.Issue(t, "Archived", testutil.Cols{"creator_id": source, "archived_at": time.Now()})
	agent := fx.Agent(t, "Private agent", "", testutil.Cols{"owner_id": source})
	var agentBefore, agentAfter string
	fx.QueryRow(t, `SELECT row_to_json(a)::text FROM agent a WHERE id=$1`, agent).Scan(&agentBefore)
	var preview memberHandoverPreview
	handoverRequest(t, testUserID, ws, member, false, map[string]any{}).Want(200).JSON(&preview)
	if preview.RecipientUserID != recipient || preview.ManagerStatus != "ready" || len(preview.Projects) != 1 || len(preview.Issues) != 1 || preview.PreviewID == "" {
		t.Fatalf("unexpected preview %+v", preview)
	}
	var assigned string
	fx.QueryRow(t, `SELECT assignee_id::text FROM issue WHERE id=$1`, issue).Scan(&assigned)
	if assigned != source {
		t.Fatal("preview changed business data")
	}
	for range 2 {
		var result memberHandoverPreview
		handoverRequest(t, testUserID, ws, member, true, map[string]any{"preview_id": preview.PreviewID}).Want(200).JSON(&result)
		if !result.Completed {
			t.Fatal("handover was not committed")
		}
	}
	var creator, lead string
	fx.QueryRow(t, `SELECT created_by::text,lead_id::text FROM project WHERE id=$1`, project).Scan(&creator, &lead)
	if creator != source || lead != source {
		t.Fatalf("creator=%s lead=%s", creator, lead)
	}
	fx.QueryRow(t, `SELECT creator_id::text,assignee_id::text FROM issue WHERE id=$1`, issue).Scan(&creator, &assigned)
	if creator != source || assigned != source {
		t.Fatalf("creator=%s assignee=%s", creator, assigned)
	}
	if fx.Count(t, `SELECT count(*) FROM projectauth_issue_access_grants WHERE workspace_id=$1 AND issue_id=$2 AND subject_id=$3 AND role_key='owner'`, ws, issue, recipient) != 1 {
		t.Fatal("receiver did not gain durable projectless work ownership")
	}
	fx.QueryRow(t, `SELECT row_to_json(a)::text FROM agent a WHERE id=$1`, agent).Scan(&agentAfter)
	if agentBefore != agentAfter || fx.Count(t, `SELECT count(*) FROM "user" WHERE id=$1 AND account_disabled_at IS NOT NULL`, source) != 1 {
		t.Fatal("handover changed private agent or reactivated departed account")
	}
}

func TestMemberHandoverRejectsStaleResourceOrRecipient(t *testing.T) {
	for _, change := range []string{"work", "recipient", "expired", "source_reenabled"} {
		t.Run(change, func(t *testing.T) {
			fx, ws, source, member, recipient := handoverFixture(t)
			issue := fx.Issue(t, "Unfinished", testutil.Cols{"creator_id": source, "assignee_type": "member", "assignee_id": source})
			var preview memberHandoverPreview
			handoverRequest(t, testUserID, ws, member, false, map[string]any{}).Want(200).JSON(&preview)
			switch change {
			case "work":
				fx.Exec(t, `UPDATE issue SET title='Changed',updated_at=updated_at+interval '1 second' WHERE id=$1`, issue)
			case "recipient":
				fx.Exec(t, `UPDATE "user" SET account_disabled_at=now() WHERE id=$1`, recipient)
			case "expired":
				fx.Exec(t, `UPDATE member_handovers SET expires_at=now()-interval '1 second' WHERE id=$1`, preview.PreviewID)
			case "source_reenabled":
				fx.Exec(t, `UPDATE "user" SET account_disabled_at=NULL WHERE id=$1`, source)
			}
			handoverRequest(t, testUserID, ws, member, true, map[string]any{"preview_id": preview.PreviewID}).Want(409)
			var assigned string
			fx.QueryRow(t, `SELECT assignee_id::text FROM issue WHERE id=$1`, issue).Scan(&assigned)
			if assigned != source || fx.Count(t, `SELECT count(*) FROM member_handovers WHERE id=$1 AND completed_at IS NOT NULL`, preview.PreviewID) != 0 {
				t.Fatal("rejected preview changed work")
			}
		})
	}
}

func TestMemberHandoverAuthorityAndManualReceiver(t *testing.T) {
	fx, ws, source, member, recipient := handoverFixture(t)
	fx.Issue(t, "Unfinished", testutil.Cols{"creator_id": source})
	admin := fx.User(t, "Admin", ws+"-admin@example.test")
	fx.Member(t, ws, admin, "admin")
	handoverRequest(t, admin, ws, member, false, map[string]any{}).Want(403)
	handoverRequest(t, testUserID, testWorkspaceID, member, false, map[string]any{}).Want(404)
	handoverRequest(t, testUserID, ws, member, false, map[string]any{"recipient_user_id": source}).Want(400)
	fx.Exec(t, `UPDATE directory_reporting_lines SET manager_external_id='not-linked' WHERE workspace_id=$1 AND user_id=$2`, ws, source)
	var missing, manual memberHandoverPreview
	handoverRequest(t, testUserID, ws, member, false, map[string]any{}).Want(200).JSON(&missing)
	if missing.PreviewID != "" || missing.ManagerStatus != "unlinked" {
		t.Fatalf("unlinked manager received executable default %+v", missing)
	}
	handoverRequest(t, testUserID, ws, member, false, map[string]any{"recipient_user_id": recipient}).Want(200).JSON(&manual)
	if manual.PreviewID == "" || manual.RecipientUserID != recipient {
		t.Fatal("manual active receiver did not produce a preview")
	}
}

// Attach diagnostics to the test event so CI's failure-only output retains them.
type handoverTestLogWriter struct{ t *testing.T }

func (w handoverTestLogWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

func TestHandoverErrorDiagnosticsPreserveHTTPContract(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"database failure", &pgconn.PgError{Code: "23514", Message: "fixture constraint failed", Detail: "sensitive fixture detail"}, 500},
		{"serialization conflict", &pgconn.PgError{Code: "40001"}, 409},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, 409},
		{"validation", handoverInvalid(403, "recipient unavailable"), 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			w := httptest.NewRecorder()
			writeHandoverError(w, fmt.Errorf("handover checklist create issue: %w", tc.err))
			if w.Code != tc.status {
				t.Fatalf("status: got %d, want %d", w.Code, tc.status)
			}
			if strings.Contains(w.Body.String(), "fixture") || strings.Contains(w.Body.String(), "SQLSTATE") || strings.Contains(w.Body.String(), "checklist") {
				t.Fatalf("API leaked internal diagnostic: %s", w.Body.String())
			}
			if tc.status == 500 && (!strings.Contains(logs.String(), "handover checklist create issue") || !strings.Contains(logs.String(), "sqlstate=23514")) {
				t.Fatalf("stage or SQLSTATE missing from server diagnostics: %s", logs.String())
			}
			if strings.Contains(logs.String(), "sensitive fixture detail") {
				t.Fatal("database detail leaked into diagnostics")
			}
		})
	}
}
