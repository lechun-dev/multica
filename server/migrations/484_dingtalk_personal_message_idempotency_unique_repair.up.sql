-- Repair environments whose migration ledger contains 482 but whose original
-- index is absent or was left invalid by an interrupted concurrent build.
-- A distinct name is required because IF NOT EXISTS does not repair an invalid
-- index with the old name. ON CONFLICT (idempotency_key) can infer either one.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS dingtalk_personal_message_idempotency_repair_uniq
    ON dingtalk_personal_message (idempotency_key);
