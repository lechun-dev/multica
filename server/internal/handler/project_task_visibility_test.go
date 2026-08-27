package handler

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

// 2026-08-27 coder(lq): Projectless issues are intentionally narrower than
// project-bound issues: only the creator, member assignee, or workspace owner
// may view them, and non-owners do not gain mutation permissions implicitly.
func TestProjectlessIssuePermission(t *testing.T) {
	creatorID := pgtype.UUID{Bytes: [16]byte{11}, Valid: true}
	assigneeID := pgtype.UUID{Bytes: [16]byte{12}, Valid: true}
	otherID := pgtype.UUID{Bytes: [16]byte{13}, Valid: true}
	issue := db.Issue{
		CreatorType:  "member",
		CreatorID:    creatorID,
		AssigneeType: pgtype.Text{String: "member", Valid: true},
		AssigneeID:   assigneeID,
	}

	cases := []struct {
		name          string
		userID        pgtype.UUID
		workspaceRole projectauth.WorkspaceRole
		permission    projectauth.Permission
		want          bool
	}{
		{name: "workspace owner can view", userID: otherID, workspaceRole: projectauth.WorkspaceOwner, permission: projectauth.View, want: true},
		{name: "workspace owner can edit", userID: otherID, workspaceRole: projectauth.WorkspaceOwner, permission: projectauth.Edit, want: true},
		{name: "creator can view", userID: creatorID, workspaceRole: projectauth.WorkspaceMember, permission: projectauth.View, want: true},
		{name: "creator cannot edit", userID: creatorID, workspaceRole: projectauth.WorkspaceMember, permission: projectauth.Edit, want: false},
		{name: "member assignee can view", userID: assigneeID, workspaceRole: projectauth.WorkspaceMember, permission: projectauth.View, want: true},
		{name: "member assignee cannot edit", userID: assigneeID, workspaceRole: projectauth.WorkspaceMember, permission: projectauth.Edit, want: false},
		{name: "unrelated member cannot view", userID: otherID, workspaceRole: projectauth.WorkspaceMember, permission: projectauth.View, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectlessIssuePermissionAllowed(issue, tc.userID, tc.workspaceRole, tc.permission); got != tc.want {
				t.Fatalf("projectlessIssuePermissionAllowed() = %v, want %v", got, tc.want)
			}
		})
	}

	agentIssue := issue
	agentIssue.CreatorType = "agent"
	if projectlessIssuePermissionAllowed(agentIssue, creatorID, projectauth.WorkspaceMember, projectauth.View) {
		t.Fatal("agent creator must not match a member identity")
	}
	agentAssignee := issue
	agentAssignee.AssigneeType = pgtype.Text{String: "agent", Valid: true}
	if projectlessIssuePermissionAllowed(agentAssignee, assigneeID, projectauth.WorkspaceMember, projectauth.View) {
		t.Fatal("agent assignee must not match a member identity")
	}
}

func TestTaskVisibleByProjectPermission(t *testing.T) {
	issueID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	projectChatID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	projectlessChatID := pgtype.UUID{Bytes: [16]byte{5}, Valid: true}
	visibleIssues := map[pgtype.UUID]struct{}{issueID: {}}
	visibleChats := map[pgtype.UUID]struct{}{projectChatID: {}}

	tests := []struct {
		name string
		task db.AgentTaskQueue
		want bool
	}{
		{name: "visible issue task", task: db.AgentTaskQueue{IssueID: issueID}, want: true},
		{name: "hidden issue task", task: db.AgentTaskQueue{IssueID: pgtype.UUID{Bytes: [16]byte{3}, Valid: true}}, want: false},
		{name: "visible project chat task", task: db.AgentTaskQueue{ChatSessionID: projectChatID}, want: true},
		{name: "hidden project chat task", task: db.AgentTaskQueue{ChatSessionID: pgtype.UUID{Bytes: [16]byte{4}, Valid: true}}, want: false},
		{name: "projectless chat task", task: db.AgentTaskQueue{ChatSessionID: projectlessChatID}, want: false},
		{name: "unscoped task", task: db.AgentTaskQueue{}, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskVisibleByProjectPermission(tc.task, visibleIssues, visibleChats); got != tc.want {
				t.Fatalf("taskVisibleByProjectPermission() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIssueIDsVisibleByProjectPermission(t *testing.T) {
	visibleID := pgtype.UUID{Bytes: [16]byte{6}, Valid: true}
	hiddenID := pgtype.UUID{Bytes: [16]byte{7}, Valid: true}
	visible := map[pgtype.UUID]struct{}{visibleID: {}}

	tests := []struct {
		name     string
		issueIDs []pgtype.UUID
		want     bool
	}{
		{name: "no issue context remains visible", issueIDs: nil, want: true},
		{name: "all issues visible", issueIDs: []pgtype.UUID{visibleID}, want: true},
		{name: "mixed visibility hides aggregate", issueIDs: []pgtype.UUID{visibleID, hiddenID}, want: false},
		{name: "invalid issue id fails closed", issueIDs: []pgtype.UUID{{Valid: false}}, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := issueIDsVisibleByProjectPermission(tc.issueIDs, visible); got != tc.want {
				t.Fatalf("issueIDsVisibleByProjectPermission() = %v, want %v", got, tc.want)
			}
		})
	}
}
