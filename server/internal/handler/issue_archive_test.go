package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIssueArchiveSuppressesAgentTriggers(t *testing.T) {
	if issueArchiveSuppressesAgentTriggers(db.Issue{}) {
		t.Fatal("active issue should allow comment trigger evaluation")
	}
	if !issueArchiveSuppressesAgentTriggers(db.Issue{
		ArchivedAt: pgtype.Timestamptz{Valid: true},
	}) {
		t.Fatal("archived issue should suppress comment trigger evaluation")
	}
}

func TestRejectArchivedIssueMutation(t *testing.T) {
	active := httptest.NewRecorder()
	if rejectArchivedIssueMutation(active, db.Issue{}) {
		t.Fatal("active issue should remain mutable")
	}
	if active.Code != http.StatusOK {
		t.Fatalf("active issue changed response status: got %d", active.Code)
	}

	archived := httptest.NewRecorder()
	if !rejectArchivedIssueMutation(archived, db.Issue{
		ArchivedAt: pgtype.Timestamptz{Valid: true},
	}) {
		t.Fatal("archived issue should be rejected")
	}
	if archived.Code != http.StatusConflict {
		t.Fatalf("archived issue status = %d, want %d", archived.Code, http.StatusConflict)
	}
}
