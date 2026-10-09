package service

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 2026-10-10 coder(lq): A live status does not reactivate an archived task.
// The shared guard is reached by child-event and scheduled wakeup dispatch.
func TestWakeupIssueActiveRejectsPrivateArchive(t *testing.T) {
	for _, status := range []string{"todo", "in_progress", "in_review"} {
		active, err := wakeupIssueActive(context.Background(), nil, db.Issue{Status: status, ArchivedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}})
		if err != nil || active {
			t.Fatalf("archived %s active=%v error=%v", status, active, err)
		}
	}
}

// 2026-10-10 coder(lq): An archived unfinished child must not hold a stage
// open after every active child is complete.
func TestWakeupSubIssueBarrierExcludesPrivateArchives(t *testing.T) {
	f, _, parent, _ := wakeFixture(t)
	live := f.Issue(t, "Finished active child", testutil.Cols{"parent_issue_id": util.UUIDToString(parent), "status": "done", "stage": int32(1)})
	f.Issue(t, "Archived unfinished child", testutil.Cols{"parent_issue_id": util.UUIDToString(parent), "status": "todo", "stage": int32(1), "archived_at": testutil.Raw("now()")})
	ctx := context.Background()
	tx, err := f.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	children, err := loadSubIssues(ctx, tx, f.q.WithTx(tx), parent, parseTestUUID(t, f.WorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || util.UUIDToString(children[0].ID) != live {
		t.Fatalf("archived child entered barrier: %+v", children)
	}
	met, _, _ := stageProgress(children)
	if !met {
		t.Fatalf("finished active children did not close barrier: %+v", children)
	}
}
