-- 2026-08-31 coder(lq): Introduce a provider-neutral authorization fact
-- table. Legacy project_members and issue_permissions remain intact during
-- rollout; the data backfill is applied by migration 453 after indexes exist.
CREATE TABLE projectauth_access_grants (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    project_id UUID NOT NULL,
    issue_id UUID,
    subject_type TEXT NOT NULL CHECK (subject_type IN ('user', 'role', 'organization', 'everyone')),
    subject_id TEXT NOT NULL DEFAULT '',
    role_key TEXT,
    permission TEXT,
    source TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'organization', 'everyone', 'migration', 'system')),
    granted_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((role_key IS NULL) <> (permission IS NULL))
);

-- 2026-08-31 coder(lq): Keep organization membership provider-neutral. A
-- provider sync writes stable external IDs here; authorization never calls an
-- OA API during a request.
CREATE TABLE projectauth_organizations (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    provider TEXT NOT NULL,
    external_id TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    parent_id UUID,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE projectauth_organization_members (
    organization_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    user_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
