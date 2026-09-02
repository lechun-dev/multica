-- 2026-09-02 coder(lq): Keep archive state on the issue row so every query
-- can apply the same visibility predicate without joining an optional table.
ALTER TABLE issue ADD COLUMN archived_at TIMESTAMPTZ DEFAULT NULL;

CREATE INDEX issue_workspace_archived_position_idx
    ON issue (workspace_id, archived_at, position, created_at DESC);
