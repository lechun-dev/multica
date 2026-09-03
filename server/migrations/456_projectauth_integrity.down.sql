-- 2026-09-04 coder(lq): Reverse the integrity constraints while retaining
-- the canonical grant and directory data for a safe migration rollback.
ALTER TABLE projectauth_organization_members
    DROP CONSTRAINT IF EXISTS projectauth_organization_members_user_id_fkey,
    DROP CONSTRAINT IF EXISTS projectauth_organization_members_workspace_id_fkey,
    DROP CONSTRAINT IF EXISTS projectauth_organization_members_organization_id_fkey;

ALTER TABLE projectauth_organizations
    DROP CONSTRAINT IF EXISTS projectauth_organizations_parent_id_fkey,
    DROP CONSTRAINT IF EXISTS projectauth_organizations_workspace_id_fkey;

ALTER TABLE projectauth_access_grants
    DROP CONSTRAINT IF EXISTS projectauth_access_grants_issue_id_fkey,
    DROP CONSTRAINT IF EXISTS projectauth_access_grants_project_id_fkey,
    DROP CONSTRAINT IF EXISTS projectauth_access_grants_workspace_id_fkey;
