-- 2026-09-03 coder(lq): store workspace-owned, data-driven retry policies independently from task execution.
CREATE TABLE task_retry_policy (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    priority INTEGER NOT NULL DEFAULT 100 CHECK (priority >= 0),
    match_type TEXT NOT NULL CHECK (match_type IN ('failure_reason', 'http_status', 'error_contains')),
    match_value TEXT NOT NULL,
    max_attempts INTEGER NOT NULL DEFAULT 2 CHECK (max_attempts BETWEEN 1 AND 5),
    delay_schedule JSONB NOT NULL DEFAULT '[0]'::jsonb,
    created_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, name)
);

CREATE INDEX idx_task_retry_policy_workspace_priority
    ON task_retry_policy(workspace_id, enabled, priority, created_at);
