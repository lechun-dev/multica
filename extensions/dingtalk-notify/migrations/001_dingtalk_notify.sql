-- Isolated schema proposal. Apply only after the host chooses a production DB.
-- All identifiers are module-prefixed to avoid collisions with Multica tables.
CREATE TABLE IF NOT EXISTS dingtalk_notify_member_bindings (
  workspace_id TEXT NOT NULL,
  member_id TEXT NOT NULL,
  union_id TEXT,
  open_id TEXT,
  active BOOLEAN NOT NULL DEFAULT TRUE,
  updated_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (workspace_id, member_id),
  CHECK (union_id IS NOT NULL OR open_id IS NOT NULL)
);

CREATE TABLE IF NOT EXISTS dingtalk_notify_agent_channels (
  workspace_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  channel_id TEXT NOT NULL,
  channel_name TEXT NOT NULL,
  robot_code TEXT,
  active BOOLEAN NOT NULL DEFAULT TRUE,
  updated_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (workspace_id, agent_id, channel_id)
);

CREATE TABLE IF NOT EXISTS dingtalk_notify_outbox (
  id BIGSERIAL PRIMARY KEY,
  idempotency_key TEXT NOT NULL UNIQUE,
  workspace_id TEXT NOT NULL,
  event_id TEXT NOT NULL,
  target_id TEXT NOT NULL,
  target_kind TEXT NOT NULL,
  channel_id TEXT,
  ding_user_id TEXT,
  message_text TEXT NOT NULL,
  status TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ NOT NULL,
  lease_until TIMESTAMPTZ,
  last_error TEXT,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS dingtalk_notify_outbox_ready_idx ON dingtalk_notify_outbox (status, next_attempt_at);

CREATE TABLE IF NOT EXISTS dingtalk_notify_delivery_attempts (
  id BIGSERIAL PRIMARY KEY,
  outbox_id BIGINT NOT NULL REFERENCES dingtalk_notify_outbox(id) ON DELETE CASCADE,
  attempt_no INTEGER NOT NULL,
  status TEXT NOT NULL,
  error_class TEXT,
  error_message TEXT,
  duration_ms BIGINT,
  created_at TIMESTAMPTZ NOT NULL
);
