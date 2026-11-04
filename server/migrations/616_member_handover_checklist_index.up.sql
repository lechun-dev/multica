CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_member_handover_checklist ON member_handovers (workspace_id,checklist_issue_id) WHERE checklist_issue_id IS NOT NULL;
