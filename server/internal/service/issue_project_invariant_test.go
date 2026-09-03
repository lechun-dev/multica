package service

import (
	"context"
	"errors"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 2026-09-01 coder(lq): Child issues must remain in their parent's project;
// this acceptance test protects the invariant at the shared service seam.
func TestIssueServiceCreateRejectsParentProjectMismatch(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	q := db.New(pool)
	workspaceID, userID, _, _ := seedAttributionFixture(t, pool)
	workspaceUUID := util.MustParseUUID(workspaceID)
	userUUID := util.MustParseUUID(userID)

	var parentProjectID, otherProjectID, parentIssueID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, 'Parent project invariant') RETURNING id
	`, workspaceUUID).Scan(&parentProjectID); err != nil {
		t.Fatalf("create parent project: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, 'Other project invariant') RETURNING id
	`, workspaceUUID).Scan(&otherProjectID); err != nil {
		t.Fatalf("create other project: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, project_id, title, status, priority, creator_type, creator_id)
		VALUES ($1, $2, 'Parent issue invariant', 'todo', 'medium', 'member', $3)
		RETURNING id
	`, workspaceUUID, parentProjectID, userUUID).Scan(&parentIssueID); err != nil {
		t.Fatalf("create parent issue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM project WHERE id IN ($1, $2)`, parentProjectID, otherProjectID)
	})

	svc := NewIssueService(q, pool, events.New(), nil, nil)
	_, err := svc.Create(ctx, IssueCreateParams{
		WorkspaceID:    workspaceUUID,
		Title:          "Mismatched child",
		Status:         "todo",
		Priority:       "medium",
		CreatorType:    "member",
		CreatorID:      userUUID,
		ParentIssueID:  util.MustParseUUID(parentIssueID),
		ProjectID:      util.MustParseUUID(otherProjectID),
		AllowDuplicate: true,
	}, IssueCreateOpts{})
	if !errors.Is(err, ErrParentProjectMismatch) {
		t.Fatalf("Create error = %v, want ErrParentProjectMismatch", err)
	}
}
