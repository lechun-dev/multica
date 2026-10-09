-- 2026-10-09 coder(lq): Restore the old system Task Member execution policy.
DELETE FROM projectauth_task_role_permissions permission
USING projectauth_task_roles role
WHERE permission.role_id = role.id
  AND role.role_key = 'member' AND role.is_system = true
  AND permission.permission = 'project.agent.use';
