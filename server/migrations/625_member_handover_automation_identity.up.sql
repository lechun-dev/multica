CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_member_handover_automation_identity ON member_handover_automation (workspace_id,source_user_id);
