-- 2026-09-07 coder(lq): Keep retry-policy names unique per workspace without
-- blocking policy reads while the index is built.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS task_retry_policy_workspace_name_uniq
    ON task_retry_policy (workspace_id, name);
