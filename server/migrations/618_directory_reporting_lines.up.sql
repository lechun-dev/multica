-- Reporting lines survive directory removals and failed provider reads.
CREATE TABLE IF NOT EXISTS directory_reporting_lines (
    workspace_id UUID NOT NULL,
    user_id UUID NOT NULL,
    provider TEXT NOT NULL,
    external_user_id TEXT NOT NULL,
    manager_external_id TEXT NOT NULL DEFAULT '',
    sync_status TEXT NOT NULL CHECK (sync_status IN ('complete', 'failed')),
    attempted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    successful_at TIMESTAMPTZ
);
