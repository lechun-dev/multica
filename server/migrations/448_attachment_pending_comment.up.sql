-- 2026-09-02 coder(lq): Keep comment-composer uploads distinguishable from
-- task-owned attachments until the comment is created and linked.
ALTER TABLE attachment
    ADD COLUMN pending_comment BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX idx_attachment_pending_comment
    ON attachment(issue_id, uploader_id)
    WHERE pending_comment = TRUE;

CREATE INDEX idx_attachment_pending_comment_created
    ON attachment(created_at, id)
    WHERE pending_comment = TRUE
      AND comment_id IS NULL
      AND source_context_id IS NULL;
