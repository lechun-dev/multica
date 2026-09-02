package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/projectauth"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// 2026-09-02 coder(lq): Keep archive mutations in their own handler so the
// retention feature remains easy to carry across upstream upgrades.

// rejectArchivedIssueMutation blocks operations that change task progress or
// launch new work. Read-only views, comments, reactions, and attachments are
// intentionally unaffected by archive state.
func rejectArchivedIssueMutation(w http.ResponseWriter, issue db.Issue) bool {
	if !issue.ArchivedAt.Valid {
		return false
	}
	writeError(w, http.StatusConflict, "archived task cannot be modified; restore it first")
	return true
}

// 2026-09-02 coder(lq): Archive keeps the timeline readable, but it must not
// create new execution work from comments or trigger previews.
func issueArchiveSuppressesAgentTriggers(issue db.Issue) bool {
	return issue.ArchivedAt.Valid
}

// 2026-09-02 coder(lq): Retrying an idempotent archive must also retry queue
// cleanup. The archive row may have committed before a transient cancellation
// failure, and returning success without this step could leave a running task
// alive indefinitely.
func (h *Handler) cancelArchivedIssueTasks(w http.ResponseWriter, r *http.Request, issue db.Issue) bool {
	if err := h.TaskService.CancelTasksForIssue(r.Context(), issue.ID); err != nil {
		slog.Warn("archive issue task cleanup failed", "issue_id", uuidToString(issue.ID), "error", err)
		writeError(w, http.StatusInternalServerError, "task archived but active work could not be stopped; retry the archive")
		return false
	}
	return true
}

// ArchiveIssue marks an issue as archived without changing its status. The
// endpoint is idempotent so a retry after a successful request is harmless.
func (h *Handler) ArchiveIssue(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if !h.requireIssueProjectPermission(w, r, issue, projectauth.IssueArchive) {
		return
	}
	if issue.ArchivedAt.Valid {
		if !h.cancelArchivedIssueTasks(w, r, issue) {
			return
		}
		writeJSON(w, http.StatusOK, issueToResponse(issue, h.getIssuePrefix(r.Context(), issue.WorkspaceID)))
		return
	}

	archived, err := h.Queries.ArchiveIssue(r.Context(), db.ArchiveIssueParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent archive won the race. Return the now-archived row rather
		// than surfacing a misleading 500 to an otherwise valid retry. Re-run
		// task cleanup as well: this request may be the first one able to recover
		// from a transient cleanup failure in the winning request.
		if current, reloadErr := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID}); reloadErr == nil && current.ArchivedAt.Valid {
			if !h.cancelArchivedIssueTasks(w, r, current) {
				return
			}
			writeJSON(w, http.StatusOK, issueToResponse(current, h.getIssuePrefix(r.Context(), current.WorkspaceID)))
			return
		}
		writeError(w, http.StatusNotFound, "issue not found")
		return
	}
	if err != nil {
		slog.Warn("archive issue failed", "issue_id", uuidToString(issue.ID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to archive issue")
		return
	}
	// 2026-09-02 coder(lq): Stop work that was admitted before the archive
	// commit. Queue-level archive fences below remain the race-safe fallback
	// for an enqueue that overlaps this cleanup transaction.
	if !h.cancelArchivedIssueTasks(w, r, archived) {
		return
	}

	resp := issueToResponse(archived, h.getIssuePrefix(r.Context(), archived.WorkspaceID))
	actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(archived.WorkspaceID))
	h.publish(protocol.EventIssueUpdated, uuidToString(archived.WorkspaceID), actorType, actorID, map[string]any{
		"issue": resp, "archived_changed": true,
	})
	writeJSON(w, http.StatusOK, resp)
}

// RestoreIssue clears archived_at and makes the task active again. Like
// ArchiveIssue, restore is idempotent for clients retrying after a timeout.
func (h *Handler) RestoreIssue(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if !h.requireIssueProjectPermission(w, r, issue, projectauth.IssueArchive) {
		return
	}
	if !issue.ArchivedAt.Valid {
		writeJSON(w, http.StatusOK, issueToResponse(issue, h.getIssuePrefix(r.Context(), issue.WorkspaceID)))
		return
	}

	restored, err := h.Queries.RestoreIssue(r.Context(), db.RestoreIssueParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		if current, reloadErr := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID}); reloadErr == nil && !current.ArchivedAt.Valid {
			writeJSON(w, http.StatusOK, issueToResponse(current, h.getIssuePrefix(r.Context(), current.WorkspaceID)))
			return
		}
		writeError(w, http.StatusNotFound, "issue not found")
		return
	}
	if err != nil {
		slog.Warn("restore issue failed", "issue_id", uuidToString(issue.ID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to restore issue")
		return
	}

	resp := issueToResponse(restored, h.getIssuePrefix(r.Context(), restored.WorkspaceID))
	actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(restored.WorkspaceID))
	h.publish(protocol.EventIssueUpdated, uuidToString(restored.WorkspaceID), actorType, actorID, map[string]any{
		"issue": resp, "archived_changed": true,
	})
	writeJSON(w, http.StatusOK, resp)
}
