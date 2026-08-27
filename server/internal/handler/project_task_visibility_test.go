package handler

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestTaskVisibleByProjectPermission(t *testing.T) {
	issueID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	projectChatID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	projectlessChatID := pgtype.UUID{Bytes: [16]byte{5}, Valid: true}
	visibleIssues := map[pgtype.UUID]struct{}{issueID: {}}
	visibleChats := map[pgtype.UUID]struct{}{projectChatID: {}, projectlessChatID: {}}

	tests := []struct {
		name string
		task db.AgentTaskQueue
		want bool
	}{
		{name: "visible issue task", task: db.AgentTaskQueue{IssueID: issueID}, want: true},
		{name: "hidden issue task", task: db.AgentTaskQueue{IssueID: pgtype.UUID{Bytes: [16]byte{3}, Valid: true}}, want: false},
		{name: "visible project chat task", task: db.AgentTaskQueue{ChatSessionID: projectChatID}, want: true},
		{name: "hidden project chat task", task: db.AgentTaskQueue{ChatSessionID: pgtype.UUID{Bytes: [16]byte{4}, Valid: true}}, want: false},
		{name: "projectless chat task", task: db.AgentTaskQueue{ChatSessionID: projectlessChatID}, want: true},
		{name: "unscoped task", task: db.AgentTaskQueue{}, want: true},
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
