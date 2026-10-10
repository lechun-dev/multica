package handler

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 2026-10-10 coder(lq): Permission filtering precedes LIMIT so newer hidden
// tasks cannot consume a page or truncate the visible history's cursor chain.
func TestAgentTasksPaginationFiltersPrivateACLBeforeLimit(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fixture := newIssueTableVisibilityFixture(t)
	h := permissionCacheHandler(t)
	runtime := fixture.fx.Runtime(t, "Private pagination runtime")
	agent := fixture.fx.Agent(t, "Private pagination agent", runtime)
	visibleIssue := fixture.fx.Issue(t, "Reader history", testutil.Cols{"creator_type": "member", "creator_id": fixture.reader})
	hiddenIssue := fixture.fx.Issue(t, "Restricted history")
	at := time.Now().UTC().Truncate(time.Microsecond)
	visible := make([]string, 3)
	for i := range visible {
		visible[i] = fixture.fx.Task(t, agent, testutil.Cols{"issue_id": visibleIssue, "runtime_id": runtime, "status": "completed", "created_at": at.Add(-time.Duration(2*i+1) * time.Minute)})
		fixture.fx.Task(t, agent, testutil.Cols{"issue_id": hiddenIssue, "runtime_id": runtime, "status": "completed", "created_at": at.Add(-time.Duration(2*i) * time.Minute)})
	}
	params := db.ListAgentTasksParams{AgentID: parseUUID(agent), BeforeCreatedAt: pgtype.Timestamptz{InfinityModifier: pgtype.Infinity, Valid: true}, BeforeID: parseUUID(agent), PageLimit: 2}
	first, err := h.listAgentTasksPageWithProjectPermission(context.Background(), fixture.ws, fixture.reader, params, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || uuidToString(first[0].ID) != visible[0] || uuidToString(first[1].ID) != visible[1] {
		t.Fatalf("hidden tasks displaced visible first page: %+v", first)
	}
	params.BeforeCreatedAt, params.BeforeID = first[1].CreatedAt, first[1].ID
	second, err := h.listAgentTasksPageWithProjectPermission(context.Background(), fixture.ws, fixture.reader, params, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || uuidToString(second[0].ID) != visible[2] {
		t.Fatalf("hidden tasks displaced final page: %+v", second)
	}
}
