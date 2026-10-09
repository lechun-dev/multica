package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

type failingAgentUseResolver struct {
	projectauth.EffectiveAccessResolver
	err error
}

func (r failingAgentUseResolver) CanIssue(context.Context, projectauth.Subject, string, projectauth.Permission) error {
	return r.err
}

func (r failingAgentUseResolver) ExplainIssue(context.Context, projectauth.Subject, string, projectauth.Permission) (projectauth.PermissionExplanation, error) {
	return projectauth.PermissionExplanation{}, r.err
}

func TestTaskPermissionStorageFailureResponseDoesNotExposeCause(t *testing.T) {
	enableProjectAuthForTest(t)
	issueID := dbfx.Issue(t, "Safe unavailable response")
	issue, err := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(issueID))
	if err != nil {
		t.Fatal(err)
	}
	const privateDetail = "private repository diagnostic"
	h := *testHandler
	h.EffectiveIssueAccess = failingAgentUseResolver{err: fmt.Errorf("%w: %s", projectauth.ErrStorageUnavailable, privateDetail)}
	w := httptest.NewRecorder()
	if h.requireIssueProjectPermission(w, newRequest("GET", "/api/issues/"+issueID, nil), issue, projectauth.View) {
		t.Fatal("unavailable authorization must fail closed")
	}
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "project_permission_unavailable") || strings.Contains(w.Body.String(), privateDetail) {
		t.Fatalf("unsafe or incorrectly classified unavailable response: %d %s", w.Code, w.Body.String())
	}
}

func TestAgentUseStorageFailureIsNotAuditedAsDenied(t *testing.T) {
	enableProjectAuthForTest(t)
	ctx := context.Background()
	issueID := dbfx.Issue(t, "Unavailable permission check")
	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("repository query failed")
	h := *testHandler
	h.EffectiveIssueAccess = failingAgentUseResolver{err: fmt.Errorf("%w: %w", projectauth.ErrStorageUnavailable, cause)}
	err = h.authorizeIssueAgentUse(ctx, issue, util.MustParseUUID(testUserID), "enqueue")
	if !errors.Is(err, projectauth.ErrStorageUnavailable) || !errors.Is(err, cause) {
		t.Fatalf("storage failure must stay blocked and diagnosable: %v", err)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM activity_log WHERE issue_id=$1 AND action='task_agent_use_check_failed' AND details->>'result'='error'`, issueID); n != 1 {
		t.Fatalf("check-failed audit count = %d, want 1", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM activity_log WHERE issue_id=$1 AND action='task_agent_use_denied'`, issueID); n != 0 {
		t.Fatalf("storage outage must not be reported as a permission denial: %d", n)
	}
}

func TestAgentInvocationUsesHumanVisibility(t *testing.T) {
	agentID, ownerID, memberID := privateAgentTestFixture(t)
	adminID := dbfx.User(t, "Visible agent administrator", "visible-agent-admin@multica.test")
	dbfx.Member(t, testWorkspaceID, adminID, "admin")
	agent, err := testHandler.Queries.GetAgent(context.Background(), util.MustParseUUID(agentID))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, userID, actorType, actorID string
		want                             bool
	}{
		{"owner can invoke own private agent", ownerID, "member", ownerID, true},
		{"workspace owner can invoke visible private agent", testUserID, "member", testUserID, true},
		{"workspace admin can invoke visible private agent", adminID, "member", adminID, true},
		{"member cannot invoke invisible private agent", memberID, "member", memberID, false},
		{"delegation uses visible human principal", testUserID, "agent", agentID, true},
		{"delegation cannot borrow agent actor visibility", memberID, "agent", agentID, false},
		{"unattributed agent cannot invoke private agent", "", "agent", agentID, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.userID != "" {
				if got := service.CanMemberInvokeAgent(context.Background(), testHandler.Queries, agent, util.MustParseUUID(tc.userID), util.MustParseUUID(testWorkspaceID)); got != tc.want {
					t.Fatalf("durable invoke = %v, want %v", got, tc.want)
				}
			}
			if got := testHandler.canInvokeAgent(context.Background(), agent, tc.actorType, tc.actorID, tc.userID, testWorkspaceID); got != tc.want {
				t.Fatalf("invoke = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTaskMemberCommentQueuesOwnVisibleAgent(t *testing.T) {
	enableProjectAuthForTest(t)
	agentID, ownerID, _ := privateAgentTestFixture(t)
	for _, projectless := range []bool{false, true} {
		t.Run(fmt.Sprintf("projectless=%v", projectless), func(t *testing.T) {
			projectID := ""
			cols := testutil.Cols{}
			if !projectless {
				projectID = dbfx.Project(t, "Member comment project")
				cols["project_id"] = projectID
			}
			issueID := dbfx.Issue(t, "Member comment task", cols)
			var grantErr error
			if projectless {
				grantErr = upsertProjectlessIssueAccessGrant(context.Background(), testPool, issueID, ownerID, projectauth.TaskMember)
			} else {
				grantErr = upsertIssueAccessGrant(context.Background(), testPool, issueID, projectID, ownerID, projectauth.TaskMember)
			}
			if grantErr != nil {
				t.Fatal(grantErr)
			}
			r := withURLParam(newRequestAs(ownerID, "POST", "/api/issues/"+issueID+"/comments", map[string]any{
				"content": fmt.Sprintf("[@My agent](mention://agent/%s) hello", agentID),
			}), "id", issueID)
			var response CommentResponse
			testutil.Call(t, testHandler.CreateComment, r).Want(http.StatusCreated).JSON(&response)
			if len(response.TriggerOutcomes) != 1 || response.TriggerOutcomes[0].Status != DispatchQueued {
				t.Fatalf("own visible agent must queue for task Member: %+v", response.TriggerOutcomes)
			}
			var principal string
			dbfx.QueryRow(t, `SELECT originator_user_id::text FROM agent_task_queue WHERE issue_id=$1 AND agent_id=$2`, issueID, agentID).Scan(&principal)
			if principal != ownerID {
				t.Fatalf("run principal = %s, want invoking task member %s", principal, ownerID)
			}
		})
	}
}

func TestTaskMemberCanInvokeOwnAgentAndViewerDenialIsNotStorageFailure(t *testing.T) {
	enableProjectAuthForTest(t)
	ctx := context.Background()
	member := dbfx.User(t, "Task member invoker", "task-member-invoker@multica.test")
	dbfx.Member(t, testWorkspaceID, member, "member")
	project := dbfx.Project(t, "Member invocation project")
	issueID := dbfx.Issue(t, "Member invocation task", testutil.Cols{"project_id": project})
	if err := upsertIssueAccessGrant(ctx, testPool, issueID, project, member, projectauth.TaskMember); err != nil {
		t.Fatal(err)
	}
	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatal(err)
	}
	if err := testHandler.authorizeIssueAgentUse(ctx, issue, util.MustParseUUID(member), "enqueue"); err != nil {
		t.Fatalf("task member must be allowed to invoke: %v", err)
	}
	dbfx.Exec(t, `UPDATE projectauth_access_grants SET role_key='viewer' WHERE issue_id=$1 AND subject_id=$2`, issueID, member)
	if err := testHandler.authorizeIssueAgentUse(ctx, issue, util.MustParseUUID(member), "enqueue"); !errors.Is(err, projectauth.ErrForbidden) {
		t.Fatalf("viewer denial = %v, want forbidden, not unavailable", err)
	}
}
