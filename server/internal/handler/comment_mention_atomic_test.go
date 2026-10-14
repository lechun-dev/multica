package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

type failMentionGrantTxStarter struct{ delegate *pgxpool.Pool }
type failMentionGrantTx struct{ pgx.Tx }

func (s failMentionGrantTxStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.delegate.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return failMentionGrantTx{tx}, nil
}
func (tx failMentionGrantTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(query, "INSERT INTO projectauth_access_grants") {
		return pgconn.CommandTag{}, errors.New("injected mention grant failure")
	}
	return tx.Tx.Exec(ctx, query, args...)
}

// 2026-10-14 coder(lq): Exercise the real create/read APIs, both attachment
// paths and a failed ACL write. A failed grant must leave no visible comment.
func TestCreateCommentMentionGrantIsAtomic(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	for _, withAttachment := range []bool{false, true} {
		for _, failGrant := range []bool{false, true} {
			t.Run(fmt.Sprintf("attachment=%t/fail=%t", withAttachment, failGrant), func(t *testing.T) {
				h := *testHandler
				h.ProjectAuth = projectauth.NewWithRollout(newProjectAuthRepository(testPool), projectauth.RolloutRestricted)
				h.EffectiveIssueAccess = projectauth.NewEffectiveAccessResolver(&projectAuthRepository{db: testPool})
				reader := dbfx.User(t, "Atomic mention reader", "atomic-"+fmt.Sprint(withAttachment, failGrant)+"@example.test")
				dbfx.Member(t, testWorkspaceID, reader, "member")
				project := dbfx.Project(t, "Atomic mention project")
				issue := dbfx.Issue(t, "Atomic mention task", testutil.Cols{"project_id": project})
				read := func() *testutil.Response {
					req := newRequestAs(reader, http.MethodGet, "/api/issues/"+issue, nil)
					req.Header.Set("X-Workspace-ID", testWorkspaceID)
					return testutil.Call(t, h.GetIssue, withURLParam(req, "id", issue))
				}
				read().Want(http.StatusForbidden)
				body := map[string]any{"content": "Please review [@Reader](mention://member/" + reader + ")"}
				var attachment string
				if withAttachment {
					attachment = unlinkedIssueAttachment(t, issue)
					body["attachment_ids"] = []string{attachment}
				}
				if failGrant {
					h.TxStarter = failMentionGrantTxStarter{testPool}
				}
				req := newRequest(http.MethodPost, "/api/issues/"+issue+"/comments", body)
				req.Header.Set("X-Workspace-ID", testWorkspaceID)
				result := testutil.Call(t, h.CreateComment, withURLParam(req, "id", issue))
				if failGrant {
					result.Want(http.StatusInternalServerError)
					if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id=$1`, issue); n != 0 {
						t.Fatalf("failed grant left %d comments", n)
					}
					if n := dbfx.Count(t, `SELECT count(*) FROM projectauth_access_grants WHERE issue_id=$1 AND subject_id=$2`, issue, reader); n != 0 {
						t.Fatal("failed grant survived rollback")
					}
					if withAttachment && dbfx.Count(t, `SELECT count(*) FROM attachment WHERE id=$1 AND comment_id IS NULL`, attachment) != 1 {
						t.Fatal("failed grant left linked attachment")
					}
					read().Want(http.StatusForbidden)
				} else {
					result.Want(http.StatusCreated)
					read().Want(http.StatusOK)
					if n := dbfx.Count(t, `SELECT count(*) FROM projectauth_access_grants WHERE issue_id=$1 AND subject_id=$2 AND role_key='member' AND source='system'`, issue, reader); n != 1 {
						t.Fatalf("mention grants=%d", n)
					}
					if projectRoleForTest(t, project, reader) != "" {
						t.Fatal("mention granted project-wide access")
					}
				}
			})
		}
	}
}
