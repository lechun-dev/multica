-- Explicit departure evidence, per-resource completion, and notification intent
-- are durable. This migration does not execute any handover.
ALTER TABLE member_handovers ADD COLUMN IF NOT EXISTS automatic BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS member_handover_automation (
 workspace_id UUID NOT NULL, source_user_id UUID NOT NULL,
 eligible BOOLEAN NOT NULL DEFAULT true,
 verified_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 status TEXT NOT NULL DEFAULT 'pending', reason TEXT NOT NULL DEFAULT '',
 recipient_user_id UUID, handover_id UUID, attempted_at TIMESTAMPTZ
);
CREATE TABLE IF NOT EXISTS member_handover_resources (
 workspace_id UUID NOT NULL, source_user_id UUID NOT NULL,
 resource_type TEXT NOT NULL CHECK(resource_type IN ('project','issue')),
 resource_id UUID NOT NULL, recipient_user_id UUID NOT NULL,
 handover_id UUID NOT NULL, completed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS member_handover_notifications (
 event_id TEXT NOT NULL, workspace_id UUID NOT NULL, recipient_user_id UUID NOT NULL,
 issue_id UUID, handover_id UUID, payload JSONB NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending', last_error TEXT NOT NULL DEFAULT '',
 attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), staged_at TIMESTAMPTZ
);
