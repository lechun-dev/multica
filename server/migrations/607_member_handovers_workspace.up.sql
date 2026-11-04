CREATE INDEX CONCURRENTLY IF NOT EXISTS member_handovers_workspace ON member_handovers (workspace_id, source_user_id, created_at);
