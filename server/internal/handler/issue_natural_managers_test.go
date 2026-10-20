package handler

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/projectauth"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestIssueNaturalManagersReceiveAndReviewAccessRequests(t *testing.T) {
	enableProjectAuthorizationForTest(t)
	t.Setenv("PROJECT_OWNER_BYPASS_ENABLED", "false")
	ctx := context.Background()
	for _, projectBound := range []bool{false, true} {
		for _, kind := range []string{"human", "agent", "shared-agent", "agent-creator", "same-owner", "foreign-agent", "unowned-agent", "system-agent"} {
			t.Run(fmt.Sprintf("project=%v/%s", projectBound, kind), func(t *testing.T) {
				ws := dbfx.Workspace(t, "Natural managers", "natural-managers")
				creator := dbfx.User(t, "Natural creator", "natural-creator@example.test")
				assignee := dbfx.User(t, "Natural assignee", "natural-assignee@example.test")
				requester := dbfx.User(t, "Natural requester", "natural-requester@example.test")
				fx := testutil.New(testPool, ws, creator)
				for _, user := range []string{creator, assignee, requester} {
					fx.Member(t, ws, user, "member")
				}
				fx.Member(t, ws, testUserID, "owner")
				cols := testutil.Cols{"assignee_type": "member", "assignee_id": assignee}
				if projectBound {
					cols["project_id"] = fx.Project(t, "Natural manager project")
				}
				wanted := map[string]int{creator: 1, assignee: 1}
				if kind == "same-owner" {
					assignee = creator
					wanted = map[string]int{creator: 1}
				}
				if kind == "agent-creator" {
					cols["creator_type"] = "agent"
					cols["creator_id"] = fx.Agent(t, "Creator agent", "")
				} else if kind != "human" {
					agentCols := testutil.Cols{"owner_id": assignee}
					switch kind {
					case "shared-agent":
						agentCols["visibility"] = "workspace"
						agentCols["permission_mode"] = "public_to"
					case "foreign-agent":
						agentCols["workspace_id"] = dbfx.Workspace(t, "Foreign agent", "foreign-agent")
						delete(wanted, assignee)
					case "unowned-agent":
						agentCols["owner_id"] = nil
						delete(wanted, assignee)
					case "system-agent":
						agentCols["kind"] = "system"
						delete(wanted, assignee)
					}
					cols["assignee_type"] = "agent"
					cols["assignee_id"] = fx.Agent(t, "Assigned agent", "", agentCols)
				}
				issue := fx.Issue(t, "Natural manager task", cols)
				fx.InsertNoID(t, "projectauth_issue_policies", testutil.Cols{"workspace_id": ws, "issue_id": issue, "project_access_mode": "restricted", "policy_version": 1}, "workspace_id=$1 AND issue_id=$2", ws, issue)
				h := *testHandler
				h.Bus = events.New()
				received := map[string]int{}
				h.Bus.Subscribe(protocol.EventInboxNew, func(e events.Event) {
					payload := e.Payload.(map[string]any)
					item := payload["item"].(map[string]any)
					received[item["recipient_id"].(string)]++
				})
				var request issueAccessRequestResponse
				testutil.Call(t, h.CreateIssueAccessRequest, issueAccessHTTPRequest(requester, ws, http.MethodPost, issue, "", map[string]string{
					"requested_role": "viewer", "idempotency_key": "natural-manager",
				})).Want(http.StatusCreated).JSON(&request)
				if !reflect.DeepEqual(received, wanted) {
					t.Fatalf("notification recipients=%v, want %v", received, wanted)
				}
				repo := &projectAuthRepository{db: testPool}
				for _, user := range []string{creator, assignee, testUserID} {
					status := http.StatusForbidden
					if wanted[user] == 1 {
						status = http.StatusOK
					}
					testutil.Call(t, h.GetIssueAccessControl, issueAccessHTTPRequest(user, ws, http.MethodGet, issue, "", nil)).Want(status)
					allowed, err := repo.IssuePermission(ctx, issue, user, projectauth.IssueManage)
					if err != nil || allowed != (status == http.StatusOK) {
						t.Fatalf("repository manage for %s=%v, err=%v, want status=%d", user, allowed, err, status)
					}
				}
				reviewer := assignee
				if wanted[reviewer] == 0 {
					reviewer = creator
				}
				review := withURLParams(issueAccessHTTPRequest(reviewer, ws, http.MethodPost, issue, "", map[string]string{"action": "approve"}), "requestID", request.ID)
				testutil.Call(t, h.ReviewIssueAccessRequest, review).Want(http.StatusOK)
				testutil.Call(t, h.GetIssue, issueAccessHTTPRequest(requester, ws, http.MethodGet, issue, "", nil)).Want(http.StatusOK)
				if kind == "human" || kind == "agent" || kind == "shared-agent" || kind == "agent-creator" {
					if kind == "agent" || kind == "shared-agent" {
						fx.Exec(t, `UPDATE agent SET owner_id=$1 WHERE id=$2`, requester, cols["assignee_id"])
					} else {
						fx.Exec(t, `UPDATE issue SET assignee_id=$1 WHERE id=$2`, requester, issue)
					}
					testutil.Call(t, h.GetIssueAccessControl, issueAccessHTTPRequest(assignee, ws, http.MethodGet, issue, "", nil)).Want(http.StatusForbidden)
					for _, user := range []string{creator, requester} {
						testutil.Call(t, h.GetIssueAccessControl, issueAccessHTTPRequest(user, ws, http.MethodGet, issue, "", nil)).Want(http.StatusOK)
					}
					for _, user := range []string{assignee, requester} {
						allowed, err := repo.IssuePermission(ctx, issue, user, projectauth.IssueManage)
						if err != nil || allowed != (user == requester) {
							t.Fatalf("after transfer repository manage for %s=%v err=%v", user, allowed, err)
						}
					}
					received = map[string]int{}
					testutil.Call(t, h.CreateIssueAccessRequest, issueAccessHTTPRequest(testUserID, ws, http.MethodPost, issue, "", map[string]string{
						"requested_role": "viewer", "idempotency_key": "after-transfer",
					})).Want(http.StatusCreated)
					if !reflect.DeepEqual(received, map[string]int{creator: 1, requester: 1}) {
						t.Fatalf("after transfer notifications=%v; want creator and current assignee", received)
					}
				}
			})
		}
	}
}
