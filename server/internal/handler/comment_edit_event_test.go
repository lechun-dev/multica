package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestCommentEditEventCarriesPrivatePreviousContent(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	for _, strict := range []bool{false, true} {
		name := "legacy"
		if strict {
			name = "revision checked"
		}
		t.Run(name, func(t *testing.T) {
			issueID := dbfx.Issue(t, "comment edit notification snapshot")
			commentID := dbfx.Comment(t, issueID, "private old body")
			h := *testHandler
			h.Bus = events.New()
			var captured events.Event
			h.Bus.Subscribe(protocol.EventCommentUpdated, func(event events.Event) { captured = event })
			body := map[string]any{"content": "new body"}
			if strict {
				body["expected_revision"] = 1
			}
			request := withURLParam(newRequest(http.MethodPut, "/api/comments/"+commentID, body), "commentId", commentID)
			var response CommentResponse
			testutil.Call(t, h.UpdateComment, request).Want(http.StatusOK).JSON(&response)
			if captured.PreviousCommentContent == nil || *captured.PreviousCommentContent != "private old body" {
				t.Fatalf("missing committed edit snapshot: %+v", captured)
			}
			if response.Revision != 2 {
				t.Fatalf("comment revision = %d, want 2", response.Revision)
			}
			encoded, err := json.Marshal(captured)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "private old body") {
				t.Fatalf("internal edit snapshot leaked into serialized event: %s", encoded)
			}
			// A duplicate save must expose the actual current body, not an old
			// caller snapshot that could trigger a second notification.
			delete(body, "expected_revision")
			request = withURLParam(newRequest(http.MethodPut, "/api/comments/"+commentID, body), "commentId", commentID)
			testutil.Call(t, h.UpdateComment, request).Want(http.StatusOK).JSON(&response)
			if captured.PreviousCommentContent == nil || *captured.PreviousCommentContent != "new body" || response.Revision != 2 {
				t.Fatalf("no-op save did not preserve snapshot/revision: %+v, %+v", captured, response)
			}
		})
	}
}
