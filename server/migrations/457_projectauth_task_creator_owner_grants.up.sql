-- 2026-09-04 coder(lq): Backfill the task-scoped Owner grant for creators of
-- existing project-bound tasks. Runtime issue hooks cover new and updated
-- tasks; this migration keeps historical tasks visible after workspace-owner
-- bypass is disabled.
INSERT INTO projectauth_access_grants
    (workspace_id, project_id, issue_id, subject_type, subject_id,
     role_key, permission, source, granted_by)
SELECT i.workspace_id, i.project_id, i.id, 'user', i.creator_id::text,
       'owner', NULL, 'migration', NULL
FROM issue i
JOIN member m
  ON m.workspace_id = i.workspace_id
 AND m.user_id = i.creator_id
WHERE i.project_id IS NOT NULL
  AND i.creator_type = 'member'
ON CONFLICT DO NOTHING;

-- 2026-09-04 coder(lq): Agent-created tasks grant Owner to the owning human,
-- matching the runtime adapter and keeping external Agent identities out of
-- the authorization subject column.
INSERT INTO projectauth_access_grants
    (workspace_id, project_id, issue_id, subject_type, subject_id,
     role_key, permission, source, granted_by)
SELECT i.workspace_id, i.project_id, i.id, 'user', a.owner_id::text,
       'owner', NULL, 'migration', NULL
FROM issue i
JOIN agent a
  ON a.id = i.creator_id
 AND a.workspace_id = i.workspace_id
 AND a.kind = 'user'
JOIN member m
  ON m.workspace_id = i.workspace_id
 AND m.user_id = a.owner_id
WHERE i.project_id IS NOT NULL
  AND i.creator_type = 'agent'
  AND a.owner_id IS NOT NULL
ON CONFLICT DO NOTHING;
