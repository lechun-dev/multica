-- 2026-09-17 coder(lq): Use 527 because the independently released project
-- authorization stream already owns migration numbers 509 through 526.
-- Keep this migration idempotent for databases that applied the briefly
-- released 509_workspace_runtime_model migration before it was renumbered.
CREATE TABLE IF NOT EXISTS workspace_runtime_model (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    runtime_provider TEXT NOT NULL,
    model_id TEXT NOT NULL,
    display_name TEXT NOT NULL,
    model_provider TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    thinking_levels JSONB NOT NULL DEFAULT '[]'::jsonb,
    default_thinking_level TEXT NOT NULL DEFAULT '',
    service_tiers JSONB NOT NULL DEFAULT '[]'::jsonb,
    supports_explicit_standard_service_tier BOOLEAN NOT NULL DEFAULT FALSE,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT workspace_runtime_model_key UNIQUE (workspace_id, runtime_provider, model_id),
    CONSTRAINT workspace_runtime_model_runtime_provider_nonempty CHECK (btrim(runtime_provider) <> ''),
    CONSTRAINT workspace_runtime_model_model_id_nonempty CHECK (btrim(model_id) <> ''),
    CONSTRAINT workspace_runtime_model_display_name_nonempty CHECK (btrim(display_name) <> ''),
    CONSTRAINT workspace_runtime_model_thinking_levels_array CHECK (jsonb_typeof(thinking_levels) = 'array'),
    CONSTRAINT workspace_runtime_model_service_tiers_array CHECK (jsonb_typeof(service_tiers) = 'array'),
    CONSTRAINT workspace_runtime_model_sort_order_nonnegative CHECK (sort_order >= 0)
);

CREATE INDEX IF NOT EXISTS workspace_runtime_model_workspace_provider_idx
    ON workspace_runtime_model (workspace_id, runtime_provider, enabled, sort_order, model_id);

-- 2026-09-17 coder(lq): Preserve the two gateway models that were previously
-- hardcoded in every Codex catalog while moving their ownership into workspace data.
INSERT INTO workspace_runtime_model (
    workspace_id,
    runtime_provider,
    model_id,
    display_name,
    model_provider,
    description,
    thinking_levels,
    enabled,
    sort_order
)
SELECT
    w.id,
    'codex',
    seed.model_id,
    seed.display_name,
    'openai',
    seed.description,
    seed.thinking_levels::jsonb,
    TRUE,
    seed.sort_order
FROM workspace w
CROSS JOIN (
    VALUES
        (
            'grok-4.6',
            'Grok 4.6',
            'Grok 4.6 routed through the configured Codex API gateway.',
            '[{"value":"low","label":"Low"},{"value":"medium","label":"Medium"},{"value":"high","label":"High"},{"value":"xhigh","label":"Extra high"}]',
            10
        ),
        (
            'grok-4.5',
            'Grok 4.5',
            'Grok 4.5 routed through the configured Codex API gateway.',
            '[{"value":"low","label":"Low"},{"value":"medium","label":"Medium"},{"value":"high","label":"High"}]',
            20
        )
) AS seed(model_id, display_name, description, thinking_levels, sort_order)
ON CONFLICT (workspace_id, runtime_provider, model_id) DO NOTHING;
