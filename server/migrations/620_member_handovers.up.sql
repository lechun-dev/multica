-- A durable preview is also the audit and idempotency record. No business
-- changes occur until the requesting owner confirms this exact snapshot.
CREATE TABLE IF NOT EXISTS member_handovers (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    source_member_id UUID NOT NULL,
    source_user_id UUID NOT NULL,
    recipient_user_id UUID NOT NULL,
    actor_id UUID NOT NULL,
    preview JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    CHECK (source_user_id <> recipient_user_id)
);
