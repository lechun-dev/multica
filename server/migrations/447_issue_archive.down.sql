-- 2026-09-02 coder(lq): Roll back the issue archive column and its list index.
DROP INDEX IF EXISTS issue_workspace_archived_position_idx;
ALTER TABLE issue DROP COLUMN IF EXISTS archived_at;
