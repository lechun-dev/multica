-- 2026-10-19 coder(lq): Preserve membership and task history; account access
-- changes invalidate interactive sessions without deleting business records.
ALTER TABLE "user"
    ADD COLUMN account_disabled_at TIMESTAMPTZ,
    ADD COLUMN session_version BIGINT NOT NULL DEFAULT 0;
