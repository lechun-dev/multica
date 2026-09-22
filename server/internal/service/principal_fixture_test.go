package service

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var principalSeq atomic.Int64

// 2026-09-22 coder(lq): Keep the shared principal fixture independent from
// reverted autopilot tests because issue wakeup coverage also relies on it.
type principalFixture struct {
	*testutil.Fixture
	svc *AutopilotService
	q   *db.Queries
}

func newPrincipalFixture(t *testing.T) (principalFixture, string) {
	t.Helper()
	pool := newResolveOriginatorPool(t)
	q := db.New(pool)

	n := principalSeq.Add(1)
	fx := testutil.New(pool, "", "")
	ownerUserID := fx.User(t, "principal owner", fmt.Sprintf("principal-owner-%d@multica.test", n))
	workspaceID := fx.Workspace(t, "principal ws", fmt.Sprintf("principal-ws-%d", n))
	fx.Member(t, workspaceID, ownerUserID, "owner")
	fx.WorkspaceID = workspaceID
	fx.UserID = ownerUserID

	return principalFixture{
		Fixture: fx,
		q:       q,
		svc: &AutopilotService{
			Queries: q, TxStarter: pool, Bus: events.New(),
			TaskSvc: &TaskService{Queries: q, TxStarter: pool, Bus: events.New()},
		},
	}, ownerUserID
}

func (f principalFixture) member(t *testing.T, label string) string {
	t.Helper()
	n := principalSeq.Add(1)
	userID := f.User(t, label, fmt.Sprintf("%s-%d@multica.test", label, n))
	f.Member(t, f.WorkspaceID, userID, "member")
	return userID
}

func (f principalFixture) privateAgentOwnedBy(t *testing.T, ownerID, label string) string {
	t.Helper()
	n := principalSeq.Add(1)
	runtimeID := f.Runtime(t, fmt.Sprintf("rt-%s-%d", label, n), testutil.Cols{"owner_id": ownerID})
	return f.Agent(t, fmt.Sprintf("agent-%s-%d", label, n), runtimeID, testutil.Cols{"owner_id": ownerID})
}
