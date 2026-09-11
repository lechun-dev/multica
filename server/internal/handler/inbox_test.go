package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

func inboxRequest(method, path, workspaceID string) *http.Request {
	return testutil.WithHeaders(
		testutil.JSONRequest(method, path, nil),
		"X-User-ID", testUserID,
		"X-Workspace-ID", workspaceID,
	)
}

func inboxWorkspaceHandler(handler http.HandlerFunc) http.HandlerFunc {
	return middleware.RequireWorkspaceMember(testHandler.Queries)(handler).ServeHTTP
}

func TestListInboxProjectsCurrentIssueStatusAndPriority(t *testing.T) {
	workspaceID := dbfx.Workspace(t, "Inbox filter projections", "inbox-filter-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "owner")
	issueID := dbfx.Issue(t, "Filtered issue", testutil.Cols{
		"workspace_id": workspaceID,
		"status":       "in_review",
		"priority":     "high",
	})
	dbfx.Insert(t, "inbox_item", testutil.Cols{
		"workspace_id":   workspaceID,
		"recipient_type": "member",
		"recipient_id":   testUserID,
		"type":           "status_changed",
		"severity":       "info",
		"issue_id":       issueID,
		"title":          "Projected issue",
	})

	var items []InboxItemResponse
	testutil.Call(t, inboxWorkspaceHandler(testHandler.ListInbox),
		inboxRequest(http.MethodGet, "/api/inbox", workspaceID)).
		Want(http.StatusOK).
		JSON(&items)

	if len(items) != 1 {
		t.Fatalf("inbox items = %d, want 1: %+v", len(items), items)
	}
	if items[0].IssueStatus == nil || *items[0].IssueStatus != "in_review" {
		t.Errorf("issue_status = %v, want in_review", items[0].IssueStatus)
	}
	if items[0].IssuePriority == nil || *items[0].IssuePriority != "high" {
		t.Errorf("issue_priority = %v, want high", items[0].IssuePriority)
	}
}

func TestListInboxShowsDirectMentionOutsideProjectMembership(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	recipientID := createSecondWorkspaceMember(t)
	var projectID string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO project (workspace_id, title) VALUES ($1, $2) RETURNING id`,
		testWorkspaceID, "Inbox direct mention project",
	).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	mentionedIssueID := dbfx.Issue(t, "Directly mentioned issue", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"project_id":   projectID,
	})
	filteredIssueID := dbfx.Issue(t, "Unrelated inbox issue", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"project_id":   projectID,
	})
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id = ANY($1::uuid[])`, []string{mentionedIssueID, filteredIssueID})
		_, _ = testPool.Exec(ctx, `DELETE FROM project WHERE id = $1`, projectID)
	})
	dbfx.Insert(t, "inbox_item", testutil.Cols{
		"workspace_id":   testWorkspaceID,
		"recipient_type": "member",
		"recipient_id":   recipientID,
		"type":           "mentioned",
		"severity":       "info",
		"issue_id":       mentionedIssueID,
		"title":          "You were mentioned",
	})
	// 2026-09-11 coder(lq): Model the production @mention rule with the
	// canonical task-scoped Member grant. This must not create project membership.
	if err := upsertIssueAccessGrant(ctx, testPool, mentionedIssueID, projectID, recipientID, projectauth.ProjectMember); err != nil {
		t.Fatalf("grant mentioned recipient task membership: %v", err)
	}
	var projectMemberCount int
	if err := testPool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM project_members
		WHERE project_id = $1::uuid AND user_id = $2::uuid`, projectID, recipientID).Scan(&projectMemberCount); err != nil {
		t.Fatalf("check project membership: %v", err)
	}
	if projectMemberCount != 0 {
		t.Fatalf("task mention created %d project membership rows, want 0", projectMemberCount)
	}
	dbfx.Insert(t, "inbox_item", testutil.Cols{
		"workspace_id":   testWorkspaceID,
		"recipient_type": "member",
		"recipient_id":   recipientID,
		"type":           "status_changed",
		"severity":       "info",
		"issue_id":       filteredIssueID,
		"title":          "Hidden project update",
	})

	previous := testHandler.ProjectAuth
	t.Setenv("PROJECT_OWNER_BYPASS_ENABLED", "false")
	testHandler.ProjectAuth = projectauth.New(newProjectAuthRepository(testPool), true)
	t.Cleanup(func() { testHandler.ProjectAuth = previous })

	request := newRequestAs(recipientID, http.MethodGet, "/api/inbox", nil)
	request.Header.Set("X-Workspace-ID", testWorkspaceID)
	var items []InboxItemResponse
	testutil.Call(t, inboxWorkspaceHandler(testHandler.ListInbox), request).
		Want(http.StatusOK).
		JSON(&items)
	if len(items) != 1 || items[0].IssueID == nil || *items[0].IssueID != mentionedIssueID {
		t.Fatalf("recipient inbox = %+v, want only direct mention for %s", items, mentionedIssueID)
	}

	request = newRequestAs(testUserID, http.MethodGet, "/api/inbox", nil)
	request.Header.Set("X-Workspace-ID", testWorkspaceID)
	items = nil
	testutil.Call(t, inboxWorkspaceHandler(testHandler.ListInbox), request).
		Want(http.StatusOK).
		JSON(&items)
	for _, item := range items {
		if item.IssueID != nil && *item.IssueID == mentionedIssueID {
			t.Fatalf("unmentioned member saw direct mention inbox row: %+v", item)
		}
	}

	// 2026-09-05 coder(lq): The notification is only the inbox surface; task
	// access comes from the explicit task-member grant above.
	view := httptest.NewRecorder()
	get := newRequestAs(recipientID, http.MethodGet, "/api/issues/"+mentionedIssueID, nil)
	get.Header.Set("X-Workspace-ID", testWorkspaceID)
	get = withURLParam(get, "id", mentionedIssueID)
	testHandler.GetIssue(view, get)
	if view.Code != http.StatusOK {
		t.Fatalf("mentioned recipient GetIssue: expected 200, got %d: %s", view.Code, view.Body.String())
	}

	reply := httptest.NewRecorder()
	comment := newRequestAs(recipientID, http.MethodPost, "/api/issues/"+mentionedIssueID+"/comments", map[string]any{"content": "I will review this."})
	comment.Header.Set("X-Workspace-ID", testWorkspaceID)
	comment = withURLParam(comment, "id", mentionedIssueID)
	testHandler.CreateComment(reply, comment)
	if reply.Code != http.StatusCreated {
		t.Fatalf("mentioned recipient CreateComment: expected 201, got %d: %s", reply.Code, reply.Body.String())
	}
}

func TestUnreadInboxCountsExcludeArchivedIssues(t *testing.T) {
	workspaceID := dbfx.Workspace(t, "Unread archived issue", "inbox-unread-archived-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "owner")
	archivedIssueID := dbfx.Issue(t, "Archived issue", testutil.Cols{
		"workspace_id": workspaceID,
		"archived_at":  "now()",
	})
	dbfx.Insert(t, "inbox_item", testutil.Cols{
		"workspace_id":   workspaceID,
		"recipient_type": "member",
		"recipient_id":   testUserID,
		"type":           "status_changed",
		"severity":       "info",
		"issue_id":       archivedIssueID,
		"title":          "Archived issue notification",
		"read":           false,
		"archived":       false,
	})

	count, err := testHandler.Queries.CountUnreadInbox(t.Context(), db.CountUnreadInboxParams{
		WorkspaceID:   parseUUID(workspaceID),
		RecipientType: "member",
		RecipientID:   parseUUID(testUserID),
	})
	if err != nil {
		t.Fatalf("count unread inbox: %v", err)
	}
	if count != 0 {
		t.Fatalf("count unread inbox = %d, want archived issue excluded", count)
	}
}

func TestListArchivedInboxLimitsIssueGroupsNotRows(t *testing.T) {
	workspaceID := dbfx.Workspace(t, "Archived inbox groups", "archived-groups-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "owner")
	noisyIssueID := dbfx.Issue(t, "Noisy archived issue", testutil.Cols{"workspace_id": workspaceID})
	olderIssueID := dbfx.Issue(t, "Older archived issue", testutil.Cols{"workspace_id": workspaceID})

	base := time.Now().UTC().Add(-time.Minute)
	for i := 0; i < 200; i++ {
		cols := testutil.Cols{
			"workspace_id":   workspaceID,
			"recipient_type": "member",
			"recipient_id":   testUserID,
			"type":           "status_changed",
			"severity":       "info",
			"issue_id":       noisyIssueID,
			"title":          fmt.Sprintf("noisy-%03d", i),
			"archived":       true,
			"created_at":     base.Add(-time.Duration(i) * time.Millisecond),
		}
		if i == 199 {
			// The bounded response keeps this row as the group's comment anchor,
			// even though the newest status row is the one the UI renders.
			cols["details"] = testutil.Raw(`'{"comment_id":"comment-1"}'::jsonb`)
		}
		dbfx.Insert(t, "inbox_item", cols)
	}
	dbfx.Insert(t, "inbox_item", testutil.Cols{
		"workspace_id":   workspaceID,
		"recipient_type": "member",
		"recipient_id":   testUserID,
		"type":           "new_comment",
		"severity":       "info",
		"issue_id":       olderIssueID,
		"title":          "older-group",
		"archived":       true,
		"created_at":     base.Add(-time.Hour),
	})

	var items []InboxItemResponse
	testutil.Call(t, inboxWorkspaceHandler(testHandler.ListArchivedInbox),
		inboxRequest(http.MethodGet, "/api/inbox/archived", workspaceID)).
		Want(http.StatusOK).
		JSON(&items)

	var noisyRows int
	var sawNoisyNewest, sawCommentAnchor, sawOlderGroup bool
	for _, item := range items {
		switch {
		case item.IssueID != nil && *item.IssueID == noisyIssueID:
			noisyRows++
			sawNoisyNewest = sawNoisyNewest || item.Title == "noisy-000"
			sawCommentAnchor = sawCommentAnchor || strings.Contains(string(item.Details), `"comment_id":"comment-1"`)
		case item.IssueID != nil && *item.IssueID == olderIssueID:
			sawOlderGroup = true
		}
	}
	if noisyRows != 2 || !sawNoisyNewest || !sawCommentAnchor {
		t.Fatalf("noisy group rows = %d, newest=%v anchor=%v; items=%+v",
			noisyRows, sawNoisyNewest, sawCommentAnchor, items)
	}
	if !sawOlderGroup {
		t.Fatal("raw-row limit let one issue hide another archived issue group")
	}
}

func TestArchiveAllReadInboxUsesNewestIssueRow(t *testing.T) {
	workspaceID := dbfx.Workspace(t, "Archive read groups", "archive-read-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "owner")
	readIssueID := dbfx.Issue(t, "Newest row is read", testutil.Cols{"workspace_id": workspaceID})
	unreadIssueID := dbfx.Issue(t, "Newest row is unread", testutil.Cols{"workspace_id": workspaceID})

	insert := func(issueID, title string, read bool, createdAt testutil.Raw) {
		t.Helper()
		dbfx.Insert(t, "inbox_item", testutil.Cols{
			"workspace_id":   workspaceID,
			"recipient_type": "member",
			"recipient_id":   testUserID,
			"type":           "status_changed",
			"severity":       "info",
			"issue_id":       issueID,
			"title":          title,
			"read":           read,
			"archived":       false,
			"created_at":     createdAt,
		})
	}
	insert(readIssueID, "older unread", false, "now() - interval '2 minutes'")
	insert(readIssueID, "newest read", true, "now() - interval '1 minute'")
	insert(unreadIssueID, "older read", true, "now() - interval '2 minutes'")
	insert(unreadIssueID, "newest unread", false, "now() - interval '1 minute'")

	testutil.Call(t, inboxWorkspaceHandler(testHandler.ArchiveAllReadInbox),
		inboxRequest(http.MethodPost, "/api/inbox/archive-all-read", workspaceID)).
		Want(http.StatusOK)

	if got := dbfx.Count(t,
		"SELECT count(*) FROM inbox_item WHERE issue_id = $1 AND archived = true", readIssueID); got != 2 {
		t.Fatalf("archived rows in read issue = %d, want the whole two-row group", got)
	}
	if got := dbfx.Count(t,
		"SELECT count(*) FROM inbox_item WHERE issue_id = $1 AND archived = true", unreadIssueID); got != 0 {
		t.Fatalf("archived rows in unread issue = %d, want the whole group untouched", got)
	}
}

func TestArchiveCompletedInboxExpandsCustomTerminalStatuses(t *testing.T) {
	workspaceID := dbfx.Workspace(t, "Archive custom completed", "archive-custom-completed-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "owner")
	dbfx.Insert(t, "issue_status", testutil.Cols{
		"workspace_id": workspaceID,
		"key":          "verified_complete",
		"name":         "Verified complete",
		"category":     "done",
		"color":        "#22c55e",
		"is_system":    false,
		"position":     1,
	})
	completedIssueID := dbfx.Issue(t, "Custom completed issue", testutil.Cols{
		"workspace_id": workspaceID,
		"status":       "verified_complete",
	})
	openIssueID := dbfx.Issue(t, "Open issue", testutil.Cols{
		"workspace_id": workspaceID,
		"status":       "todo",
	})
	for _, issueID := range []string{completedIssueID, openIssueID} {
		dbfx.Insert(t, "inbox_item", testutil.Cols{
			"workspace_id":   workspaceID,
			"recipient_type": "member",
			"recipient_id":   testUserID,
			"type":           "status_changed",
			"severity":       "info",
			"issue_id":       issueID,
			"title":          "Status changed",
			"archived":       false,
		})
	}

	testutil.Call(t, inboxWorkspaceHandler(testHandler.ArchiveCompletedInbox),
		inboxRequest(http.MethodPost, "/api/inbox/archive-completed", workspaceID)).
		Want(http.StatusOK)

	if got := dbfx.Count(t,
		"SELECT count(*) FROM inbox_item WHERE issue_id = $1 AND archived = true", completedIssueID); got != 1 {
		t.Fatalf("archived rows for custom completed issue = %d, want 1", got)
	}
	if got := dbfx.Count(t,
		"SELECT count(*) FROM inbox_item WHERE issue_id = $1 AND archived = true", openIssueID); got != 0 {
		t.Fatalf("archived rows for open issue = %d, want 0", got)
	}
}

func TestInboxListBodyPreview(t *testing.T) {
	issue := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	text := func(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }
	long := strings.Repeat("a", 5000)
	// Every CJK character is three bytes in UTF-8: a byte-based cut would land
	// mid-character and produce invalid UTF-8.
	longCJK := strings.Repeat("评论内容", 500)

	cases := []struct {
		name      string
		notifType string
		issueID   pgtype.UUID
		body      pgtype.Text
		want      *string
	}{
		{"null body stays null", "new_comment", issue, pgtype.Text{}, nil},
		{"short comment is untouched", "new_comment", issue, text("looks good"), ptr("looks good")},
		{"exactly at the limit is untouched", "new_comment", issue,
			text(strings.Repeat("a", inboxListBodyPreviewLimit)),
			ptr(strings.Repeat("a", inboxListBodyPreviewLimit))},
		{"one past the limit is cut, ellipsis included", "new_comment", issue,
			text(strings.Repeat("a", inboxListBodyPreviewLimit+1)),
			ptr(strings.Repeat("a", inboxListBodyPreviewLimit-1) + "…")},
		{"long comment is cut to the limit", "new_comment", issue, text(long),
			ptr(strings.Repeat("a", inboxListBodyPreviewLimit-1) + "…")},
		// Issue-less notifications render their body in the detail pane from
		// the list cache, so shortening them would lose content.
		{"comment without an issue keeps its full body", "new_comment", pgtype.UUID{}, text(long), ptr(long)},
		// Other types are out of scope even when issue-backed: their body is
		// not merely a preview of something the issue page shows.
		{"other types keep their full body", "task_failed", issue, text(long), ptr(long)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := inboxListBody(tc.notifType, tc.issueID, tc.body)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("body = %q, want nil", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("body = nil, want %d characters", utf8.RuneCountInString(*tc.want))
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("body = %d characters %q…, want %d characters",
					utf8.RuneCountInString(*got), truncateForLog(*got),
					utf8.RuneCountInString(*tc.want))
			}
		})
	}

	t.Run("multi-byte text is cut on a character boundary", func(t *testing.T) {
		got := inboxListBody("new_comment", issue, text(longCJK))
		if got == nil {
			t.Fatal("body = nil")
		}
		if !utf8.ValidString(*got) {
			t.Fatalf("preview is not valid UTF-8: %q", truncateForLog(*got))
		}
		if n := utf8.RuneCountInString(*got); n != inboxListBodyPreviewLimit {
			t.Fatalf("preview = %d characters, want %d", n, inboxListBodyPreviewLimit)
		}
		if !strings.HasSuffix(*got, "…") {
			t.Fatalf("preview does not end with an ellipsis: %q", truncateForLog(*got))
		}
	})
}

// Both inbox lists ship the preview, the stored row keeps the whole comment,
// and a notification that needs its full body still gets it.
func TestInboxListsShipCommentPreviewNotFullComment(t *testing.T) {
	workspaceID := dbfx.Workspace(t, "Inbox body preview", "inbox-preview-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "owner")
	activeIssue := dbfx.Issue(t, "Active comment issue", testutil.Cols{"workspace_id": workspaceID})
	archivedIssue := dbfx.Issue(t, "Archived comment issue", testutil.Cols{"workspace_id": workspaceID})
	fullComment := strings.Repeat("A long agent reply. ", 300)
	issueLessBody := strings.Repeat("An issue-less notice. ", 300)

	insert := func(cols testutil.Cols) {
		base := testutil.Cols{
			"workspace_id":   workspaceID,
			"recipient_type": "member",
			"recipient_id":   testUserID,
			"severity":       "info",
			"title":          "Notification",
		}
		for k, v := range cols {
			base[k] = v
		}
		dbfx.Insert(t, "inbox_item", base)
	}
	insert(testutil.Cols{"type": "new_comment", "issue_id": activeIssue, "body": fullComment})
	insert(testutil.Cols{"type": "new_comment", "issue_id": archivedIssue, "body": fullComment, "archived": true})
	insert(testutil.Cols{"type": "autopilot_paused", "body": issueLessBody})

	wantPreview := string([]rune(fullComment)[:inboxListBodyPreviewLimit-1]) + "…"
	bodyOf := func(items []InboxItemResponse, issueID string) string {
		t.Helper()
		for _, item := range items {
			if item.IssueID != nil && *item.IssueID == issueID {
				if item.Body == nil {
					t.Fatalf("item for issue %s has no body", issueID)
				}
				return *item.Body
			}
		}
		t.Fatalf("no item for issue %s in %d items", issueID, len(items))
		return ""
	}

	var active []InboxItemResponse
	testutil.Call(t, inboxWorkspaceHandler(testHandler.ListInbox),
		inboxRequest(http.MethodGet, "/api/inbox", workspaceID)).
		Want(http.StatusOK).
		JSON(&active)
	if got := bodyOf(active, activeIssue); got != wantPreview {
		t.Errorf("main list body = %d characters, want the %d-character preview",
			utf8.RuneCountInString(got), inboxListBodyPreviewLimit)
	}
	var issueLess *string
	for _, item := range active {
		if item.IssueID == nil {
			issueLess = item.Body
		}
	}
	if issueLess == nil || *issueLess != issueLessBody {
		t.Errorf("issue-less notification lost its full body (its detail pane renders it)")
	}

	var archived []InboxItemResponse
	testutil.Call(t, inboxWorkspaceHandler(testHandler.ListArchivedInbox),
		inboxRequest(http.MethodGet, "/api/inbox/archived", workspaceID)).
		Want(http.StatusOK).
		JSON(&archived)
	if got := bodyOf(archived, archivedIssue); got != wantPreview {
		t.Errorf("archived list body = %d characters, want the %d-character preview",
			utf8.RuneCountInString(got), inboxListBodyPreviewLimit)
	}

	// Only the response is shortened; the stored comment is whole.
	if got := dbfx.Count(t,
		`SELECT count(*) FROM inbox_item WHERE workspace_id = $1 AND type = 'new_comment' AND body = $2`,
		workspaceID, fullComment); got != 2 {
		t.Errorf("stored full comments = %d, want 2", got)
	}
}

func truncateForLog(s string) string {
	if r := []rune(s); len(r) > 40 {
		return string(r[:40])
	}
	return s
}
