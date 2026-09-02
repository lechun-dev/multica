-- 2026-09-02 coder(lq): Remove the temporary comment-draft attachment marker.
DROP INDEX IF EXISTS idx_attachment_pending_comment;
DROP INDEX IF EXISTS idx_attachment_pending_comment_created;
ALTER TABLE attachment DROP COLUMN IF EXISTS pending_comment;
