-- Keep failed enrichment ineligibility even if its reporting-line diagnostic
-- write failed. A later successful refresh clears this latch.
ALTER TABLE member_handover_automation ADD COLUMN IF NOT EXISTS manager_sync_failed BOOLEAN NOT NULL DEFAULT false;
