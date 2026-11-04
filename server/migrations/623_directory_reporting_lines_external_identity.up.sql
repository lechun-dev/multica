CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS directory_reporting_lines_external_identity_idx ON directory_reporting_lines (workspace_id, provider, external_user_id);
