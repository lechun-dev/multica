-- 2026-09-04 coder(lq): Remove stale authorization and directory rows before
-- adding foreign keys. Older private deployments could delete projects,
-- issues, or workspaces while the new tables had no database-level cleanup.
DELETE FROM projectauth_access_grants g
WHERE NOT EXISTS (
    SELECT 1
    FROM project p
    WHERE p.id = g.project_id
      AND p.workspace_id = g.workspace_id
)
   OR (
       g.issue_id IS NOT NULL
       AND NOT EXISTS (
           SELECT 1
           FROM issue i
           WHERE i.id = g.issue_id
             AND i.project_id = g.project_id
             AND i.workspace_id = g.workspace_id
       )
   );

-- 2026-09-04 coder(lq): Keep directory imports self-healing when a parent
-- department was removed outside the import workflow, while removing
-- memberships that cannot resolve to the same workspace's directory/user.
UPDATE projectauth_organizations o
SET parent_id = NULL
WHERE parent_id IS NOT NULL
  AND NOT EXISTS (
      SELECT 1
      FROM projectauth_organizations parent
      WHERE parent.id = o.parent_id
        AND parent.workspace_id = o.workspace_id
  );

DELETE FROM projectauth_organization_members om
WHERE NOT EXISTS (
    SELECT 1
    FROM projectauth_organizations o
    WHERE o.id = om.organization_id
      AND o.workspace_id = om.workspace_id
)
   OR NOT EXISTS (
       SELECT 1
       FROM member m
       WHERE m.workspace_id = om.workspace_id
         AND m.user_id = om.user_id
   );

ALTER TABLE projectauth_access_grants
    ADD CONSTRAINT projectauth_access_grants_workspace_id_fkey
        FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE CASCADE,
    ADD CONSTRAINT projectauth_access_grants_project_id_fkey
        FOREIGN KEY (project_id) REFERENCES project(id) ON DELETE CASCADE,
    ADD CONSTRAINT projectauth_access_grants_issue_id_fkey
        FOREIGN KEY (issue_id) REFERENCES issue(id) ON DELETE CASCADE;

ALTER TABLE projectauth_organizations
    ADD CONSTRAINT projectauth_organizations_workspace_id_fkey
        FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE CASCADE,
    ADD CONSTRAINT projectauth_organizations_parent_id_fkey
        FOREIGN KEY (parent_id) REFERENCES projectauth_organizations(id) ON DELETE SET NULL;

ALTER TABLE projectauth_organization_members
    ADD CONSTRAINT projectauth_organization_members_organization_id_fkey
        FOREIGN KEY (organization_id) REFERENCES projectauth_organizations(id) ON DELETE CASCADE,
    ADD CONSTRAINT projectauth_organization_members_workspace_id_fkey
        FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE CASCADE,
    ADD CONSTRAINT projectauth_organization_members_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES "user"(id) ON DELETE CASCADE;
