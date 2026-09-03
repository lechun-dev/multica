-- 2026-08-31 coder(lq): Backfill the provider-neutral grant table only after
-- its idempotency indexes exist. Legacy tables remain the compatibility source.
INSERT INTO projectauth_access_grants
    (workspace_id, project_id, subject_type, subject_id, role_key, source)
SELECT p.workspace_id, pm.project_id, 'user', pm.user_id::text,
       COALESCE(role_def.role_key, pm.role), 'migration'
FROM project_members pm
JOIN project p ON p.id = pm.project_id
LEFT JOIN project_permission_roles role_def ON role_def.id = pm.custom_role_id
    -- 2026-09-04 coder(lq): Keep custom-role backfill inside the project's
    -- workspace boundary even if legacy data contains a stale role reference.
    AND role_def.workspace_id = p.workspace_id
ON CONFLICT DO NOTHING;

INSERT INTO projectauth_access_grants
    (workspace_id, project_id, issue_id, subject_type, subject_id, permission, source, granted_by)
SELECT p.workspace_id, ip.project_id, ip.issue_id, 'user', ip.user_id::text, ip.permission, 'migration', ip.granted_by
FROM issue_permissions ip
JOIN project p ON p.id = ip.project_id
ON CONFLICT DO NOTHING;
