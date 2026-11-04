package handler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestHandoverOwnerReceiverGetsPermissionsAndNotifications(t *testing.T) {
	for _, promotedAfterGrant := range []bool{false, true} {
		t.Run(map[bool]string{false: "already_owner", true: "promoted_before_routing"}[promotedAfterGrant], func(t *testing.T) {
			fx, ws, source, _, recipient := handoverFixture(t)
			ctx := context.Background()
			if !promotedAfterGrant {
				fx.Exec(t, `UPDATE member SET role='owner' WHERE workspace_id=$1 AND user_id=$2`, ws, recipient)
			}
			otherOwner := fx.User(t, "Other owner", ws+"-other-owner@example.test")
			fx.Member(t, ws, otherOwner, "owner")
			issue := fx.Issue(t, "Owner leader work", testutil.Cols{"creator_id": source, "assignee_type": "member", "assignee_id": source})
			verifyHandoverEvidence(t, ws, []string{source}, nil)
			result, err := testHandler.RunAutomaticMemberHandovers(ctx, ws, testUserID)
			if err != nil || result.Completed != 1 {
				t.Fatalf("owner leader permission opening: %+v %v", result, err)
			}
			if fx.Count(t, `SELECT count(*) FROM projectauth_issue_access_grants WHERE workspace_id=$1 AND issue_id=$2 AND subject_id=$3`, ws, issue, recipient) != 1 ||
				fx.Count(t, `SELECT count(*) FROM member_handovers WHERE workspace_id=$1 AND checklist_issue_id IS NOT NULL`, ws) != 1 ||
				fx.Count(t, `SELECT count(*) FROM issue WHERE id=$1 AND assignee_id=$2`, issue, source) != 1 {
				t.Fatal("owner receiver lost permissions/checklist or changed the original assignee")
			}
			if fx.Count(t, `SELECT count(*) FROM inbox_item WHERE workspace_id=$1 AND recipient_id=$2`, ws, recipient) != 1 ||
				fx.Count(t, `SELECT count(*) FROM member_handover_notifications WHERE workspace_id=$1 AND recipient_user_id=$2`, ws, recipient) != 1 ||
				fx.Count(t, `SELECT count(*) FROM inbox_item WHERE workspace_id=$1 AND recipient_id<>$2`, ws, recipient) != 0 ||
				fx.Count(t, `SELECT count(*) FROM member_handover_notifications WHERE workspace_id=$1 AND recipient_user_id<>$2`, ws, recipient) != 0 {
				t.Fatal("handover must notify only the receiving leader, including an owner")
			}
			if promotedAfterGrant {
				fx.Exec(t, `UPDATE member SET role='owner' WHERE workspace_id=$1 AND user_id=$2`, ws, recipient)
			}
			for key, value := range map[string]string{"DINGTALK_CLIENT_ID": "fixture-id", "DINGTALK_CLIENT_SECRET": "fixture-secret", "DINGTALK_ROBOT_CODE": "fixture-robot"} {
				t.Setenv(key, value)
			}
			dingID := "handover-owner-" + recipient
			fx.InsertNoID(t, "dingtalk_notify_identities", testutil.Cols{"ding_user_id": dingID, "multica_user_id": recipient, "active": true, "login_only": false, "updated_at": time.Now()}, "ding_user_id=$1", dingID)
			var checklist string
			fx.QueryRow(t, `SELECT checklist_issue_id::text FROM member_handovers WHERE workspace_id=$1`, ws).Scan(&checklist)
			for range 2 {
				if _, err = testHandler.RunAutomaticMemberHandovers(ctx, ws, testUserID); err != nil {
					t.Fatal(err)
				}
				if err = FlushMemberHandoverNotifications(ctx, testHandler.TxStarter, func(_ context.Context, w, u, i string) (bool, error) {
					return w == ws && u == recipient && i == checklist, nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if fx.Count(t, `SELECT count(*) FROM dingtalk_notify_outbox WHERE workspace_id=$1 AND target_id=$2`, ws, recipient) != 1 ||
				fx.Count(t, `SELECT count(*) FROM dingtalk_notify_outbox WHERE workspace_id=$1 AND target_id<>$2`, ws, recipient) != 0 ||
				fx.Count(t, `SELECT count(*) FROM inbox_item WHERE workspace_id=$1`, ws) != 1 ||
				fx.Count(t, `SELECT count(*) FROM member_handover_notifications WHERE workspace_id=$1 AND status='staged'`, ws) != 1 {
				t.Fatal("owner summary routing was suppressed, duplicated, or broadcast to other owners")
			}
		})
	}
}

func TestHandoverTaskScopeDoesNotOpenWholeProject(t *testing.T) {
	fx, ws, source, _, recipient := handoverFixture(t)
	project := fx.Project(t, "Other person's project")
	issue := fx.Issue(t, "Departed assignee only", testutil.Cols{"project_id": project, "assignee_type": "member", "assignee_id": source})
	verifyHandoverEvidence(t, ws, []string{source}, nil)
	result, err := testHandler.RunAutomaticMemberHandovers(context.Background(), ws, testUserID)
	if err != nil || result.Completed != 1 {
		t.Fatalf("task-scope opening: %+v %v", result, err)
	}
	if fx.Count(t, `SELECT count(*) FROM projectauth_access_grants WHERE workspace_id=$1 AND issue_id=$2 AND subject_id=$3 AND role_key='manager'`, ws, issue, recipient) != 1 {
		t.Fatal("assignee-only responsibility did not grant task management")
	}
	if fx.Count(t, `SELECT count(*) FROM projectauth_access_grants WHERE workspace_id=$1 AND project_id=$2 AND issue_id IS NULL AND subject_id=$3`, ws, project, recipient) != 0 ||
		fx.Count(t, `SELECT count(*) FROM project_members WHERE project_id=$1 AND user_id=$2`, project, recipient) != 0 {
		t.Fatal("task-only responsibility opened the entire project")
	}
}

func TestHandoverSupplementReusesChecklistAndPreservesNotes(t *testing.T) {
	fx, ws, source, _, _ := handoverFixture(t)
	fx.Issue(t, "First work", testutil.Cols{"creator_id": source})
	verifyHandoverEvidence(t, ws, []string{source}, nil)
	ctx := context.Background()
	if _, err := testHandler.RunAutomaticMemberHandovers(ctx, ws, testUserID); err != nil {
		t.Fatal(err)
	}
	var checklist string
	fx.QueryRow(t, `SELECT checklist_issue_id::text FROM member_handovers WHERE workspace_id=$1`, ws).Scan(&checklist)
	fx.Exec(t, `UPDATE issue SET description='Leader notes before' || E'\n' || description || E'\nLeader notes after',status='done' WHERE id=$1`, checklist)
	fx.Issue(t, "Newly discovered work", testutil.Cols{"creator_id": source})
	if _, err := testHandler.RunAutomaticMemberHandovers(ctx, ws, testUserID); err != nil {
		t.Fatal(err)
	}
	var description, status string
	fx.QueryRow(t, `SELECT description,status FROM issue WHERE id=$1`, checklist).Scan(&description, &status)
	if !strings.HasPrefix(description, "Leader notes before") || !strings.HasSuffix(description, "Leader notes after") ||
		!strings.Contains(description, "First work") || !strings.Contains(description, "Newly discovered work") || status != "todo" {
		t.Fatalf("supplement lost notes/work or failed to reopen: %s %s", status, description)
	}
	if _, err := testHandler.RunAutomaticMemberHandovers(ctx, ws, testUserID); err != nil {
		t.Fatal(err)
	}
	if fx.Count(t, `SELECT count(DISTINCT checklist_issue_id) FROM member_handovers WHERE workspace_id=$1`, ws) != 1 ||
		fx.Count(t, `SELECT count(*) FROM member_handover_notifications WHERE workspace_id=$1`, ws) != 2 {
		t.Fatal("supplement duplicated checklist or repeated a no-work notification")
	}
}

func TestHandoverDoesNotTrustManagerAfterFailedRefresh(t *testing.T) {
	for _, failedLineWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "read_failure", true: "diagnostic_write_failure"}[failedLineWrite], func(t *testing.T) {
			fx, ws, source, _, _ := handoverFixture(t)
			fx.Issue(t, "Untrusted manager work", testutil.Cols{"creator_id": source})
			verifyHandoverEvidence(t, ws, []string{source}, nil)
			if failedLineWrite {
				fx.Exec(t, `UPDATE member_handover_automation SET manager_sync_failed=true WHERE workspace_id=$1 AND source_user_id=$2`, ws, source)
			} else {
				fx.Exec(t, `UPDATE directory_reporting_lines SET sync_status='failed' WHERE workspace_id=$1 AND user_id=$2`, ws, source)
			}
			result, err := testHandler.RunAutomaticMemberHandovers(context.Background(), ws, testUserID)
			if err != nil || result.Pending != 1 || result.Completed != 0 {
				t.Fatalf("unreliable saved manager was used: %+v %v", result, err)
			}
			if fx.Count(t, `SELECT count(*) FROM member_handover_resources WHERE workspace_id=$1`, ws) != 0 ||
				fx.Count(t, `SELECT count(*) FROM member_handover_notifications WHERE workspace_id=$1`, ws) != 0 {
				t.Fatal("failed manager refresh granted permissions or sent a notification")
			}
		})
	}
}
