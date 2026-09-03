CREATE UNIQUE INDEX CONCURRENTLY projectauth_organizations_workspace_provider_external_uniq
    ON projectauth_organizations (workspace_id, provider, external_id);
