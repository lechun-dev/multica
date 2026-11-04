DROP TABLE IF EXISTS member_handover_notifications;
DROP TABLE IF EXISTS member_handover_resources;
DROP TABLE IF EXISTS member_handover_automation;
ALTER TABLE IF EXISTS member_handovers DROP COLUMN IF EXISTS automatic;
