-- name: CreateAttachment :one
WITH inserted AS (
  INSERT INTO attachment (
    id, workspace_id, issue_id, comment_id, chat_session_id, task_id, source_context_id, pending_comment,
    uploader_type, uploader_id, filename, url, content_type, size_bytes
  )
  SELECT
    $1, $2, sqlc.narg(issue_id), sqlc.narg(comment_id), sqlc.narg(chat_session_id), sqlc.narg(task_id), sqlc.narg(source_context_id), sqlc.arg(pending_comment)::boolean,
    $3, $4, $5, $6, $7, $8
  WHERE sqlc.narg(comment_id)::uuid IS NOT NULL
  OR (sqlc.arg(pending_comment)::boolean AND sqlc.narg(comment_id)::uuid IS NULL)
  OR sqlc.narg(issue_id)::uuid IS NULL
     OR EXISTS (
          SELECT 1 FROM issue
          WHERE issue.id = sqlc.narg(issue_id)::uuid
            AND issue.workspace_id = $2
            AND issue.archived_at IS NULL
        )
  RETURNING id, workspace_id, issue_id, comment_id, uploader_type, uploader_id,
            filename, url, content_type, size_bytes, created_at, chat_session_id,
            chat_message_id, task_id, source_context_id
), bumped_issue AS (
  UPDATE issue
  SET revision = revision + 1
  WHERE id IN (SELECT issue_id FROM inserted WHERE issue_id IS NOT NULL)
  RETURNING revision
), bumped_comment AS (
  UPDATE comment
  SET revision = revision + 1
  WHERE id IN (SELECT comment_id FROM inserted WHERE comment_id IS NOT NULL)
  RETURNING revision
)
SELECT inserted.id, inserted.workspace_id, inserted.issue_id, inserted.comment_id,
       inserted.uploader_type, inserted.uploader_id, inserted.filename, inserted.url,
       inserted.content_type, inserted.size_bytes, inserted.created_at,
       inserted.chat_session_id, inserted.chat_message_id, inserted.task_id,
       inserted.source_context_id,
       COALESCE((SELECT revision FROM bumped_issue), 0)::bigint AS issue_revision,
       COALESCE((SELECT revision FROM bumped_comment), 0)::bigint AS comment_revision
FROM inserted;

-- name: ListAttachmentsByIssue :many
-- 2026-09-05 coder(lq): Keep pending_comment in the generated value so sqlc's
-- Attachment model stays complete; the predicate below still hides drafts.
SELECT id, workspace_id, issue_id, comment_id, uploader_type, uploader_id, filename, url, content_type, size_bytes, created_at, chat_session_id, chat_message_id, task_id, source_context_id, pending_comment FROM attachment
WHERE issue_id = $1 AND workspace_id = $2 AND pending_comment = FALSE
ORDER BY created_at ASC;

-- name: ListAttachmentsByComment :many
SELECT id, workspace_id, issue_id, comment_id, uploader_type, uploader_id, filename, url, content_type, size_bytes, created_at, chat_session_id, chat_message_id, task_id, source_context_id, pending_comment FROM attachment
WHERE comment_id = $1 AND workspace_id = $2
ORDER BY created_at ASC;

-- name: GetAttachment :one
SELECT id, workspace_id, issue_id, comment_id, uploader_type, uploader_id, filename, url, content_type, size_bytes, created_at, chat_session_id, chat_message_id, task_id, source_context_id, pending_comment FROM attachment
WHERE id = $1 AND workspace_id = $2;

-- name: IsAttachmentPendingComment :one
SELECT pending_comment FROM attachment
WHERE id = $1 AND workspace_id = $2;

-- name: GetAttachmentByIDOnly :one
-- Used by the download endpoint, which derives workspace context from the
-- attachment row itself rather than from request headers/query params. The
-- caller still has to verify the requester is a member of the returned
-- workspace_id before serving the bytes — this query is access-neutral on
-- purpose so a self-contained URL like /api/attachments/{id}/download can
-- work as a native <img>/<video> resource load (no header attachment).
SELECT id, workspace_id, issue_id, comment_id, uploader_type, uploader_id, filename, url, content_type, size_bytes, created_at, chat_session_id, chat_message_id, task_id, source_context_id, pending_comment FROM attachment
WHERE id = $1;

-- name: ListAttachmentsByCommentIDs :many
SELECT id, workspace_id, issue_id, comment_id, uploader_type, uploader_id, filename, url, content_type, size_bytes, created_at, chat_session_id, chat_message_id, task_id, source_context_id, pending_comment FROM attachment
WHERE comment_id = ANY($1::uuid[]) AND workspace_id = $2
ORDER BY created_at ASC;

-- name: ListSourceContextIssueAttachments :many
-- Source snapshots keep issue-owned attachments separate from comment-owned
-- attachments. In this schema a comment attachment retains issue_id, so the
-- generic ListAttachmentsByIssue query is intentionally too broad here.
SELECT id, workspace_id, issue_id, comment_id, uploader_type, uploader_id, filename, url, content_type, size_bytes, created_at, chat_session_id, chat_message_id, task_id, source_context_id, pending_comment FROM attachment
WHERE workspace_id = sqlc.arg(workspace_id)
  AND issue_id = sqlc.arg(issue_id)
  AND comment_id IS NULL
  AND pending_comment = FALSE
  AND source_context_id IS NULL
ORDER BY created_at ASC, id ASC;

-- name: ListSourceContextCommentAttachments :many
-- The issue predicate is a defense-in-depth owner guard for damaged rows; the
-- comment ids were already derived from the guarded thread history.
SELECT id, workspace_id, issue_id, comment_id, uploader_type, uploader_id, filename, url, content_type, size_bytes, created_at, chat_session_id, chat_message_id, task_id, source_context_id, pending_comment FROM attachment
WHERE workspace_id = sqlc.arg(workspace_id)
  AND issue_id = sqlc.arg(issue_id)
  AND comment_id = ANY(sqlc.arg(comment_ids)::uuid[])
  AND pending_comment = FALSE
  AND source_context_id IS NULL
ORDER BY created_at ASC, id ASC;

-- name: ListAttachmentURLsByIssueOrComments :many
SELECT a.url FROM attachment a
WHERE a.pending_comment = FALSE
  AND (a.issue_id = $1
   OR a.comment_id IN (SELECT c.id FROM comment c WHERE c.issue_id = $1));

-- name: ListAttachmentURLsByCommentID :many
SELECT url FROM attachment
WHERE comment_id = $1;

-- name: LinkAttachmentsToComment :exec
UPDATE attachment
SET comment_id = $1,
    pending_comment = FALSE
WHERE issue_id = $2
  AND comment_id IS NULL
  AND workspace_id = sqlc.arg(workspace_id)
  AND uploader_type = sqlc.arg(uploader_type)
  AND uploader_id = sqlc.arg(uploader_id)
  AND source_context_id IS NULL
  AND id = ANY(sqlc.arg(attachment_ids)::uuid[]);

-- name: ReplaceCommentAttachments :execrows
UPDATE attachment
SET comment_id = CASE
  WHEN id = ANY(sqlc.arg(attachment_ids)::uuid[]) THEN $1
  ELSE NULL
END,
    pending_comment = CASE
      WHEN id = ANY(sqlc.arg(attachment_ids)::uuid[]) THEN FALSE
      ELSE pending_comment
    END
WHERE issue_id = $2
  AND workspace_id = sqlc.arg(workspace_id)
  AND source_context_id IS NULL
  AND (
    (comment_id = $1 AND NOT id = ANY(sqlc.arg(attachment_ids)::uuid[]))
    OR (
      comment_id IS NULL
      AND pending_comment = TRUE
      AND uploader_type = sqlc.arg(uploader_type)
      AND uploader_id = sqlc.arg(uploader_id)
      AND id = ANY(sqlc.arg(attachment_ids)::uuid[])
    )
  );

-- name: LinkAttachmentsToChatMessage :many
UPDATE attachment
SET chat_message_id = sqlc.arg(chat_message_id),
    chat_session_id = sqlc.arg(chat_session_id)
WHERE workspace_id = sqlc.arg(workspace_id)
  AND issue_id IS NULL
  AND comment_id IS NULL
  AND chat_message_id IS NULL
  AND source_context_id IS NULL
  AND (
    chat_session_id IS NULL
    OR chat_session_id = sqlc.arg(chat_session_id)
  )
  AND uploader_type = sqlc.arg(uploader_type)
  AND uploader_id = sqlc.arg(uploader_id)
  AND id = ANY(sqlc.arg(attachment_ids)::uuid[])
RETURNING id;

-- name: DetachAttachmentsFromUserChatMessageByTask :many
-- When an empty chat task is cancelled, its user message is deleted. The
-- attachment FK is ON DELETE CASCADE, so without this the bound rows would be
-- destroyed and a restored draft could never re-bind them. Detach first
-- (chat_message_id -> NULL, keep chat_session_id) so the rows survive as
-- workspace/session-scoped unattached attachments and re-send can re-link them.
UPDATE attachment
SET chat_message_id = NULL
WHERE chat_message_id IN (
  SELECT id FROM chat_message WHERE chat_message.task_id = $1 AND role = 'user'
)
RETURNING id, workspace_id, issue_id, comment_id, uploader_type, uploader_id, filename, url, content_type, size_bytes, created_at, chat_session_id, chat_message_id, task_id, source_context_id, pending_comment;

-- name: CountUnboundChatAttachmentsForTask :one
-- How many attachments the agent produced for this chat task that are still
-- unbound to any owner. Lets CompleteTask create an assistant message (and
-- bind them) even when the agent's text output was empty but it uploaded files.
SELECT COUNT(*) FROM attachment
WHERE workspace_id = sqlc.arg(workspace_id)
  AND task_id = sqlc.arg(task_id)
  AND issue_id IS NULL
  AND comment_id IS NULL
  AND chat_message_id IS NULL
  AND source_context_id IS NULL;

-- name: BindChatAttachmentsToMessage :many
-- Bind a chat agent's task-scoped attachments to the assistant reply it just
-- produced. Only rows still unclaimed by any owner (issue/comment/chat_message)
-- are eligible, so an attachment already linked elsewhere is never stolen.
-- Returns the bound ids for logging.
UPDATE attachment
SET chat_message_id = sqlc.arg(chat_message_id)
WHERE workspace_id = sqlc.arg(workspace_id)
  AND task_id = sqlc.arg(task_id)
  AND issue_id IS NULL
  AND comment_id IS NULL
  AND chat_message_id IS NULL
  AND source_context_id IS NULL
RETURNING id;

-- name: ListAttachmentsByChatMessage :many
SELECT id, workspace_id, issue_id, comment_id, uploader_type, uploader_id, filename, url, content_type, size_bytes, created_at, chat_session_id, chat_message_id, task_id, source_context_id, pending_comment FROM attachment
WHERE chat_message_id = $1 AND workspace_id = $2
ORDER BY created_at ASC;

-- name: ListAttachmentsByChatMessageIDs :many
SELECT id, workspace_id, issue_id, comment_id, uploader_type, uploader_id, filename, url, content_type, size_bytes, created_at, chat_session_id, chat_message_id, task_id, source_context_id, pending_comment FROM attachment
WHERE chat_message_id = ANY($1::uuid[]) AND workspace_id = $2
ORDER BY created_at ASC;

-- name: LockAttachmentsForIssueLink :many
-- Issue updates bind attachments and then touch the owner row. Lock eligible
-- attachment rows first so every attachment -> issue mutation uses the same
-- lock order as DeleteAttachment and cannot deadlock with it.
SELECT id FROM attachment
WHERE workspace_id = sqlc.arg(workspace_id)
  AND issue_id IS NULL
  AND pending_comment = FALSE
  AND source_context_id IS NULL
  AND id = ANY(sqlc.arg(attachment_ids)::uuid[])
ORDER BY id
FOR UPDATE;

-- name: LinkAttachmentsToIssue :one
WITH linked AS (
  UPDATE attachment
  SET issue_id = sqlc.arg(issue_id)
  WHERE attachment.workspace_id = sqlc.arg(workspace_id)
    AND attachment.issue_id IS NULL
    AND attachment.pending_comment = FALSE
    AND attachment.source_context_id IS NULL
    AND attachment.id = ANY(sqlc.arg(attachment_ids)::uuid[])
    AND EXISTS (
      SELECT 1 FROM issue
      WHERE issue.id = sqlc.arg(issue_id)
        AND issue.workspace_id = sqlc.arg(workspace_id)
        AND issue.archived_at IS NULL
    )
  RETURNING attachment.issue_id
), bumped_issue AS (
  UPDATE issue
  SET revision = revision + 1,
      updated_at = now()
  WHERE id = sqlc.arg(issue_id)
    AND sqlc.arg(bump_revision)::boolean
    AND EXISTS (SELECT 1 FROM linked)
  RETURNING revision
)
SELECT COUNT(*)::bigint AS linked_count,
       COALESCE((SELECT revision FROM bumped_issue), 0)::bigint AS issue_revision
FROM linked;

-- name: DeleteAttachment :one
WITH deleted AS (
  DELETE FROM attachment
  WHERE attachment.id = $1 AND attachment.workspace_id = $2
    AND attachment.source_context_id IS NULL
    AND (
      attachment.comment_id IS NOT NULL
      OR attachment.pending_comment = TRUE
      OR attachment.issue_id IS NULL
      OR EXISTS (
        SELECT 1 FROM issue
        WHERE issue.id = attachment.issue_id
          AND issue.workspace_id = attachment.workspace_id
          AND issue.archived_at IS NULL
      )
    )
  RETURNING issue_id, comment_id
), bumped_issue AS (
  UPDATE issue
  SET revision = revision + 1
  WHERE id IN (SELECT issue_id FROM deleted WHERE issue_id IS NOT NULL)
  RETURNING revision
), bumped_comment AS (
  UPDATE comment
  SET revision = revision + 1
  WHERE id IN (SELECT comment_id FROM deleted WHERE comment_id IS NOT NULL)
  RETURNING revision
)
SELECT EXISTS(SELECT 1 FROM deleted) AS changed,
       COALESCE((SELECT revision FROM bumped_issue), 0)::bigint AS issue_revision,
       COALESCE((SELECT revision FROM bumped_comment), 0)::bigint AS comment_revision;

-- name: ListAttachmentsByIDs :many
SELECT id, workspace_id, issue_id, comment_id, uploader_type, uploader_id, filename, url, content_type, size_bytes, created_at, chat_session_id, chat_message_id, task_id, source_context_id, pending_comment FROM attachment
WHERE id = ANY(sqlc.arg(attachment_ids)::uuid[]) AND workspace_id = sqlc.arg(workspace_id)
ORDER BY created_at ASC;

-- name: ListStaleCommentAttachmentDrafts :many
-- Comment uploads are intentionally retained for a short window so a client
-- can finish creating or editing a comment after the upload request returns.
-- Only unbound drafts are eligible; once linked, the row follows normal
-- comment-attachment lifecycle rules.
SELECT id, workspace_id, issue_id, comment_id, uploader_type, uploader_id, filename, url, content_type, size_bytes, created_at, chat_session_id, chat_message_id, task_id, source_context_id, pending_comment FROM attachment
WHERE pending_comment = TRUE
  AND comment_id IS NULL
  AND source_context_id IS NULL
  AND created_at <= sqlc.arg(created_before)
ORDER BY created_at ASC, id ASC
LIMIT sqlc.arg(row_limit);

-- name: DeleteCommentAttachmentDraft :execrows
-- The lifecycle predicates make cleanup safe if a draft is bound while the
-- object-store delete is in flight.
DELETE FROM attachment
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
  AND pending_comment = TRUE
  AND comment_id IS NULL
  AND source_context_id IS NULL;

-- name: CreateSourceContextAttachment :one
INSERT INTO attachment (
  id, workspace_id, source_context_id, uploader_type, uploader_id,
  filename, url, content_type, size_bytes
) VALUES (
  sqlc.arg(id), sqlc.arg(workspace_id), sqlc.arg(source_context_id),
  sqlc.arg(uploader_type), sqlc.arg(uploader_id), sqlc.arg(filename),
  sqlc.arg(url), sqlc.arg(content_type), sqlc.arg(size_bytes)
)
RETURNING id, workspace_id, issue_id, comment_id, uploader_type, uploader_id, filename, url, content_type, size_bytes, created_at, chat_session_id, chat_message_id, task_id, source_context_id, pending_comment;

-- name: ListAttachmentsBySourceContext :many
SELECT id, workspace_id, issue_id, comment_id, uploader_type, uploader_id, filename, url, content_type, size_bytes, created_at, chat_session_id, chat_message_id, task_id, source_context_id, pending_comment FROM attachment
WHERE workspace_id = sqlc.arg(workspace_id)
  AND source_context_id = sqlc.arg(source_context_id)
ORDER BY created_at ASC, id ASC;

-- name: DeleteAttachmentsBySourceContext :many
DELETE FROM attachment
WHERE workspace_id = sqlc.arg(workspace_id)
  AND source_context_id = sqlc.arg(source_context_id)
RETURNING id, workspace_id, issue_id, comment_id, uploader_type, uploader_id, filename, url, content_type, size_bytes, created_at, chat_session_id, chat_message_id, task_id, source_context_id, pending_comment;

-- name: ListSourceContextAttachmentURLsByWorkspace :many
SELECT url FROM attachment
WHERE workspace_id = sqlc.arg(workspace_id)
  AND source_context_id IS NOT NULL
ORDER BY id;

-- name: DeleteSourceContextAttachmentsByWorkspace :exec
DELETE FROM attachment
WHERE workspace_id = sqlc.arg(workspace_id)
  AND source_context_id IS NOT NULL;
