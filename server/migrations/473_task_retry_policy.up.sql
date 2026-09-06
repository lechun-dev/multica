-- 2026-09-07 coder(lq): Restore the retry-policy table that was present on
-- the upstream stream but lost when migration numbers were squashed around
-- private migrations. Relationships are validated by the service layer.
CREATE TABLE IF NOT EXISTS task_retry_policy (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    name TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    priority INTEGER NOT NULL DEFAULT 100 CHECK (priority >= 0),
    match_type TEXT NOT NULL CHECK (match_type IN ('failure_reason', 'http_status', 'error_contains')),
    match_value TEXT NOT NULL,
    max_attempts INTEGER NOT NULL DEFAULT 2 CHECK (max_attempts BETWEEN 1 AND 5),
    delay_schedule JSONB NOT NULL DEFAULT '[0]'::jsonb,
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
