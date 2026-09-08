CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS dingtalk_personal_message_idempotency_uniq
    ON dingtalk_personal_message (idempotency_key);
