-- 2026-09-23 coder(lq): Repair databases where migration 537 was recorded
-- but issue_status.icon is absent. Existing icon values are preserved.
ALTER TABLE issue_status
    ADD COLUMN IF NOT EXISTS icon text NOT NULL DEFAULT '';
