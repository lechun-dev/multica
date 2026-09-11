CREATE TABLE IF NOT EXISTS dingtalk_dws_credential (
    multica_user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    corp_id TEXT NOT NULL,
    ding_user_id TEXT NOT NULL,
    union_id TEXT,
    client_id TEXT NOT NULL,
    access_token_ciphertext BYTEA NOT NULL,
    refresh_token_ciphertext BYTEA,
    access_expires_at TIMESTAMPTZ NOT NULL,
    refresh_expires_at TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN (
        'active',
        'reauthorization_required',
        'revoked'
    )),
    last_error_code TEXT,
    last_error_message TEXT,
    last_refreshed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (corp_id, multica_user_id),
    UNIQUE (corp_id, ding_user_id)
);

CREATE TABLE IF NOT EXISTS dingtalk_dws_oauth_state (
    state TEXT PRIMARY KEY,
    multica_user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_dingtalk_dws_oauth_state_expiry
    ON dingtalk_dws_oauth_state (expires_at);

ALTER TABLE dingtalk_personal_message
    ALTER COLUMN expires_at SET DEFAULT (now() + interval '7 days');

-- Stop leases held by the retired desktop sender and let the server worker
-- safely reclaim all still-valid messages after deployment.
UPDATE dingtalk_personal_message
SET status = CASE
        WHEN expires_at <= now() THEN 'expired'
        ELSE 'pending'
    END,
    lease_owner = NULL,
    leased_until = NULL,
    available_at = now(),
    updated_at = now()
WHERE status IN ('pending', 'leased', 'waiting_for_dws_login', 'waiting_for_identity');

-- Keep the sender-scoped index for per-user status reads and old data tools;
-- add a separate global index for the server worker's cross-user claim query.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_dingtalk_personal_message_server_claim
    ON dingtalk_personal_message (status, available_at, created_at)
    WHERE status IN ('pending', 'leased', 'waiting_for_dws_login', 'waiting_for_identity');
