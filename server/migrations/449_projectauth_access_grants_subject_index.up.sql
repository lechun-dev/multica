CREATE INDEX CONCURRENTLY projectauth_access_grants_subject_idx
    ON projectauth_access_grants (subject_type, subject_id, workspace_id);
