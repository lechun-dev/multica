-- 2026-09-05 coder(lq): Store task-scoped authorization for issues that do
-- not belong to a project. The project grant table deliberately keeps
-- project_id NOT NULL, so projectless task shares live in this narrow table
-- instead of weakening project-level data integrity.
CREATE TABLE projectauth_issue_access_grants (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    subject_type TEXT NOT NULL CHECK (subject_type IN ('user', 'organization', 'everyone')),
    subject_id TEXT NOT NULL DEFAULT '',
    role_key TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'organization', 'everyone', 'migration', 'system')),
    granted_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, issue_id, subject_type, subject_id, role_key, source)
);

CREATE INDEX projectauth_issue_access_grants_issue_idx
    ON projectauth_issue_access_grants (workspace_id, issue_id);

CREATE INDEX projectauth_issue_access_grants_subject_idx
    ON projectauth_issue_access_grants (workspace_id, subject_type, subject_id);
