CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_dingtalk_personal_message_claim
    ON dingtalk_personal_message (sender_user_id, status, available_at, created_at)
    WHERE status IN ('pending', 'leased', 'waiting_for_dws_login', 'waiting_for_identity');
