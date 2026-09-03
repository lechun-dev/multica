-- 2026-08-31 coder(lq): Enforce idempotent project-level grants without a
-- table-level unique constraint, keeping index creation non-blocking.
CREATE UNIQUE INDEX CONCURRENTLY projectauth_access_grants_project_uniq
    ON projectauth_access_grants (project_id, subject_type, subject_id, COALESCE(role_key, ''), COALESCE(permission, ''))
    WHERE issue_id IS NULL;
