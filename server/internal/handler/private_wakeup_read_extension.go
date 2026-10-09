package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

// 2026-10-10 coder(lq): Authorize wakeup candidates before SQL ranks, pages,
// counts or exposes filter choices. Delegated reads use the initiating human
// and the live source-task AgentUse check, never the runtime owner's rights.
func (h *Handler) wakeupReadIssueScope(r *http.Request, workspaceID string) ([]pgtype.UUID, bool, error) {
	if h.ProjectAuth == nil || !h.ProjectAuth.Enabled() {
		return nil, false, nil
	}
	ids := make([]pgtype.UUID, 0)
	subject, reason := h.issuePermissionSubject(r, db.Issue{WorkspaceID: parseUUID(workspaceID)})
	if reason != "" {
		if reason == "unavailable" || reason == "migration" || reason == "internal" {
			return nil, true, fmt.Errorf("wakeup authorization: %s", reason)
		}
		return ids, true, nil
	}
	candidates, err := h.Queries.ListWorkspaceWakeupIssueIDs(r.Context(), parseUUID(workspaceID))
	if err != nil {
		return nil, true, err
	}
	visible, err := h.visibleIssueIDsByProjectPermissionWithWorkspaceScope(r.Context(), parseUUID(workspaceID), parseUUID(subject.UserID), candidates, includeWorkspaceOwnedFromRequest(r))
	if err != nil {
		return nil, true, err
	}
	for id := range visible {
		ids = append(ids, id)
	}
	return ids, true, nil
}

// 2026-10-10 coder(lq): Management is live and bounded to the returned page.
// Ordinary rules retain their author/admin gate; system rules use task Manage.
func (h *Handler) wakeupPageManagement(r *http.Request, result []byte) ([]byte, error) {
	var page map[string]json.RawMessage
	if err := json.Unmarshal(result, &page); err != nil {
		return nil, err
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(page["items"], &items); err != nil {
		return nil, err
	}
	for _, item := range items {
		var source, issueID string
		if err := json.Unmarshal(item["source"], &source); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(item["issue_id"], &issueID); err != nil {
			return nil, err
		}
		if source != "system" {
			continue
		}
		issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: parseUUID(issueID), WorkspaceID: parseUUID(h.resolveWorkspaceID(r))})
		if err != nil {
			return nil, err
		}
		allowed, reason := h.issueProjectAllowedWithWorkspaceScope(r, issue, projectauth.IssueManage, includeWorkspaceOwnedFromRequest(r))
		if reason == "unavailable" || reason == "migration" || reason == "internal" {
			return nil, fmt.Errorf("wakeup management authorization: %s", reason)
		}
		item["can_manage"], err = json.Marshal(allowed)
		if err != nil {
			return nil, err
		}
	}
	var err error
	page["items"], err = json.Marshal(items)
	if err != nil {
		return nil, err
	}
	return json.Marshal(page)
}
