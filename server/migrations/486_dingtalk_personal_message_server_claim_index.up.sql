-- Keep this migration to one statement: PostgreSQL does not allow
-- CREATE INDEX CONCURRENTLY inside the implicit transaction created when a
-- migration file contains multiple statements.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_dingtalk_personal_message_server_claim
    ON dingtalk_personal_message (status, available_at, created_at)
    WHERE status IN ('pending', 'leased', 'waiting_for_dws_login', 'waiting_for_identity');
