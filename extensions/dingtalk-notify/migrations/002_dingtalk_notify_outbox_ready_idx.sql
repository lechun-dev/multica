CREATE INDEX CONCURRENTLY IF NOT EXISTS dingtalk_notify_outbox_ready_idx
  ON dingtalk_notify_outbox (status, next_attempt_at);
