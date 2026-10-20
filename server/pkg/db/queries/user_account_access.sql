-- name: GetUserAccountAccess :one
SELECT account_disabled_at, session_version FROM "user" WHERE id = $1;

-- name: SetUserAccountAccess :one
UPDATE "user" SET
    account_disabled_at = CASE WHEN sqlc.arg(disabled)::boolean THEN now() ELSE NULL END,
    session_version = session_version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING account_disabled_at, session_version;

-- name: ListManageableAccounts :many
-- 2026-10-19 coder(lq): A workspace role cannot suspend an account in another
-- tenant. Require ownership of every workspace and never suspend an owner.
SELECT candidate.user_id FROM member candidate
WHERE candidate.workspace_id = sqlc.arg(workspace_id)
  AND candidate.user_id <> sqlc.arg(actor_id)
  AND EXISTS (SELECT 1 FROM member actor_here WHERE actor_here.workspace_id = candidate.workspace_id
              AND actor_here.user_id = sqlc.arg(actor_id) AND actor_here.role = 'owner')
  AND NOT EXISTS (
    SELECT 1 FROM member scope_member
    LEFT JOIN member actor ON actor.workspace_id = scope_member.workspace_id
                         AND actor.user_id = sqlc.arg(actor_id)
    WHERE scope_member.user_id = candidate.user_id
      AND (scope_member.role = 'owner' OR COALESCE(actor.role, '') <> 'owner')
  );
