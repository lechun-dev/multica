CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS directory_reporting_lines_identity ON directory_reporting_lines (workspace_id, user_id, provider);
