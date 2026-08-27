ALTER TABLE dingtalk_notify_identities
  ADD COLUMN IF NOT EXISTS departments JSONB NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN IF NOT EXISTS departments_synced_at TIMESTAMPTZ;
