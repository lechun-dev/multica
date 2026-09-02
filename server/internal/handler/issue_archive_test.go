package handler

import (
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
