-- 2026-09-12 coder(lq): Upstream migration 450 is renumbered to 491 to preserve the released private migration history.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_comment_delegated_failure_pending
ON comment (created_at, id)
WHERE author_type = 'system'
  AND type = 'progress_update'
  AND source_task_id IS NOT NULL;
