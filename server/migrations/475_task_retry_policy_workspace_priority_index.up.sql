-- 2026-09-07 coder(lq): Accelerate enabled retry-policy lookup by workspace
-- and priority.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_task_retry_policy_workspace_priority
    ON task_retry_policy (workspace_id, enabled, priority, created_at);
