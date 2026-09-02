ALTER TABLE dingtalk_notify_identities
  DROP COLUMN IF EXISTS departments_synced_at,
  DROP COLUMN IF EXISTS departments;
