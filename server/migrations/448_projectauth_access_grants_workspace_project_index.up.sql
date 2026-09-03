CREATE INDEX CONCURRENTLY projectauth_access_grants_workspace_project_idx
    ON projectauth_access_grants (workspace_id, project_id, issue_id);
