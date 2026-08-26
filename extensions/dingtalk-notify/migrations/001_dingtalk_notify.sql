-- Isolated schema proposal. Apply only after the host chooses a production DB.
-- All identifiers are module-prefixed to avoid collisions with Multica tables.
CREATE TABLE IF NOT EXISTS dingtalk_notify_member_bindings (
  workspace_id TEXT NOT NULL,
  member_id TEXT NOT NULL,
  ding_user_id TEXT,
  union_id TEXT,
  open_id TEXT,
  active BOOLEAN NOT NULL DEFAULT TRUE,
  updated_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (workspace_id, member_id),
  CHECK (ding_user_id IS NOT NULL OR union_id IS NOT NULL OR open_id IS NOT NULL)
);

-- Login identities are account-level and intentionally independent from the
-- host's member/workspace tables. The host adapter applies its own trusted
-- email/identity matching policy before writing the Multica user id here.
CREATE TABLE IF NOT EXISTS dingtalk_notify_identities (
  ding_user_id TEXT,
  union_id TEXT,
  open_id TEXT,
  email TEXT,
  multica_user_id TEXT NOT NULL,
  active BOOLEAN NOT NULL DEFAULT TRUE,
  login_only BOOLEAN NOT NULL DEFAULT FALSE,
  updated_at TIMESTAMPTZ NOT NULL,
  CHECK (ding_user_id IS NOT NULL OR union_id IS NOT NULL OR open_id IS NOT NULL),
  UNIQUE (ding_user_id),
  UNIQUE (union_id),
  UNIQUE (open_id)
);

CREATE TABLE IF NOT EXISTS dingtalk_notify_oauth_states (
  state TEXT PRIMARY KEY,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS dingtalk_notify_agent_channels (
  workspace_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  channel_id TEXT NOT NULL,
  channel_name TEXT NOT NULL,
  robot_code TEXT,
  owner_id TEXT,
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
  robot_code TEXT,
  ding_user_id TEXT,
  channel_type TEXT NOT NULL,
  message_text TEXT NOT NULL,
  status TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ NOT NULL,
  lease_until TIMESTAMPTZ,
  last_error TEXT,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS dingtalk_notify_delivery_attempts (
  id BIGSERIAL PRIMARY KEY,
  outbox_id BIGINT NOT NULL,
  attempt_no INTEGER NOT NULL,
  status TEXT NOT NULL,
  error_class TEXT,
  error_message TEXT,
  duration_ms BIGINT,
  created_at TIMESTAMPTZ NOT NULL
);
