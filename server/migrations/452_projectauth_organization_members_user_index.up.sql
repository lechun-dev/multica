CREATE INDEX CONCURRENTLY projectauth_organization_members_user_idx
    ON projectauth_organization_members (workspace_id, user_id);
