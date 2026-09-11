-- 2026-09-12 coder(lq): Replayed upstream migration 448 under 489 after the private migration stream reused that version; IF NOT EXISTS keeps prior deployments safe.
ALTER TABLE autopilot_quota_period
    ADD COLUMN IF NOT EXISTS rejection_notified_at TIMESTAMPTZ;
