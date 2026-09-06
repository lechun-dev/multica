-- name: ListMembers :many
SELECT * FROM member
WHERE workspace_id = $1
ORDER BY created_at ASC;

-- name: ListWorkspaceManagerUserIDs :many
SELECT user_id FROM member
WHERE workspace_id = $1 AND role IN ('owner', 'admin')
ORDER BY created_at ASC;

-- name: GetMember :one
SELECT * FROM member
WHERE id = $1;

-- name: GetMemberByUserAndWorkspace :one
SELECT * FROM member
WHERE user_id = $1 AND workspace_id = $2;

-- name: CreateMember :one
INSERT INTO member (workspace_id, user_id, role)
VALUES ($1, $2, $3)
RETURNING *;

-- name: UpdateMemberRole :one
UPDATE member SET role = $2
WHERE id = $1
RETURNING *;

-- name: DeleteMember :exec
DELETE FROM member WHERE id = $1;

-- name: ListMembersWithUser :many
SELECT m.id, m.workspace_id, m.user_id, m.role, m.created_at,
       u.name as user_name, u.email as user_email, u.avatar_url as user_avatar_url,
       -- 2026-09-06 coder(lq): Owners/admins can only receive their role from
       -- an authenticated workspace action; retain that legacy signal for
       -- accounts created before projectauth_user_logins was introduced.
       (l.user_id IS NOT NULL OR u.onboarded_at IS NOT NULL OR m.role IN ('owner', 'admin')) AS has_logged_in
FROM member m
JOIN "user" u ON u.id = m.user_id
LEFT JOIN projectauth_user_logins l ON l.user_id = m.user_id
WHERE m.workspace_id = $1
ORDER BY m.created_at ASC;
