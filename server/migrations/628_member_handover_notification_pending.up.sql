CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_member_handover_notification_pending ON member_handover_notifications (next_attempt_at) WHERE status='pending';
