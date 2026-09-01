-- 2026-08-31 coder(lq): Enforce idempotent task-level grants without a
-- table-level unique constraint, keeping index creation non-blocking.
CREATE UNIQUE INDEX CONCURRENTLY projectauth_access_grants_issue_uniq
    ON projectauth_access_grants (project_id, issue_id, subject_type, subject_id, COALESCE(role_key, ''), COALESCE(permission, ''))
    WHERE issue_id IS NOT NULL;
