CREATE UNIQUE INDEX CONCURRENTLY projectauth_organization_members_pkey
    ON projectauth_organization_members (organization_id, user_id);
