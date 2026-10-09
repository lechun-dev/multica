-- 2026-10-09 coder(lq): Task Member may invoke visible agents. Update existing
-- system Member catalogs as well as the defaults for newly created workspaces;
-- custom roles and task/project grants are unchanged.
INSERT INTO projectauth_task_role_permissions (role_id, permission)
SELECT id, 'project.agent.use'
FROM projectauth_task_roles
WHERE role_key = 'member' AND is_system = true
ON CONFLICT DO NOTHING;
