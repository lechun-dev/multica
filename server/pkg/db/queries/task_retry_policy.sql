-- name: ListTaskRetryPolicies :many
SELECT * FROM task_retry_policy
WHERE workspace_id = sqlc.arg('workspace_id')::uuid
ORDER BY priority ASC, created_at ASC, id ASC;

-- name: GetTaskRetryPolicy :one
SELECT * FROM task_retry_policy
WHERE id = sqlc.arg('id')::uuid AND workspace_id = sqlc.arg('workspace_id')::uuid;

-- name: CreateTaskRetryPolicy :one
INSERT INTO task_retry_policy (
    workspace_id, name, enabled, priority, match_type, match_value,
    max_attempts, delay_schedule, created_by
) VALUES (
    sqlc.arg('workspace_id')::uuid,
    sqlc.arg('name')::text,
    sqlc.arg('enabled')::bool,
    sqlc.arg('priority')::int,
    sqlc.arg('match_type')::text,
    sqlc.arg('match_value')::text,
    sqlc.arg('max_attempts')::int,
    sqlc.arg('delay_schedule')::jsonb,
    sqlc.narg('created_by')::uuid
)
RETURNING *;

-- name: UpdateTaskRetryPolicy :one
UPDATE task_retry_policy SET
    name = COALESCE(sqlc.narg('name'), name),
    enabled = COALESCE(sqlc.narg('enabled'), enabled),
    priority = COALESCE(sqlc.narg('priority'), priority),
    match_type = COALESCE(sqlc.narg('match_type'), match_type),
    match_value = COALESCE(sqlc.narg('match_value'), match_value),
    max_attempts = COALESCE(sqlc.narg('max_attempts'), max_attempts),
    delay_schedule = COALESCE(sqlc.narg('delay_schedule'), delay_schedule),
    updated_at = now()
WHERE id = sqlc.arg('id')::uuid AND workspace_id = sqlc.arg('workspace_id')::uuid
RETURNING *;

-- name: DeleteTaskRetryPolicy :exec
DELETE FROM task_retry_policy
WHERE id = sqlc.arg('id')::uuid AND workspace_id = sqlc.arg('workspace_id')::uuid;

-- name: ListFailedTaskIDsForRetry :many
-- The scanner resolves the owning workspace through agent.workspace_id so it
-- also covers chat and quick-create tasks whose optional source foreign keys
-- are absent. Autopilot runs have their own scheduler and are excluded here.
SELECT atq.id
FROM agent_task_queue AS atq
JOIN agent AS a ON a.id = atq.agent_id
WHERE atq.status = 'failed'
  AND atq.autopilot_run_id IS NULL
ORDER BY atq.created_at ASC, atq.id ASC
LIMIT sqlc.arg('limit')::int;
