-- name: ListWorkspaceRuntimeModels :many
SELECT *
FROM workspace_runtime_model
WHERE workspace_id = $1
ORDER BY runtime_provider, sort_order, model_id;

-- name: ListEnabledWorkspaceRuntimeModelsByProvider :many
SELECT *
FROM workspace_runtime_model
WHERE workspace_id = $1
  AND runtime_provider = $2
  AND enabled = TRUE
ORDER BY sort_order, model_id;

-- name: GetWorkspaceRuntimeModel :one
SELECT *
FROM workspace_runtime_model
WHERE id = $1
  AND workspace_id = $2;

-- name: GetEnabledWorkspaceRuntimeModelByKey :one
SELECT *
FROM workspace_runtime_model
WHERE workspace_id = $1
  AND runtime_provider = $2
  AND model_id = $3
  AND enabled = TRUE;

-- name: CreateWorkspaceRuntimeModel :one
INSERT INTO workspace_runtime_model (
    workspace_id,
    runtime_provider,
    model_id,
    display_name,
    model_provider,
    description,
    thinking_levels,
    default_thinking_level,
    service_tiers,
    supports_explicit_standard_service_tier,
    enabled,
    sort_order
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
)
RETURNING *;

-- name: UpdateWorkspaceRuntimeModel :one
UPDATE workspace_runtime_model
SET display_name = $3,
    model_provider = $4,
    description = $5,
    thinking_levels = $6,
    default_thinking_level = $7,
    service_tiers = $8,
    supports_explicit_standard_service_tier = $9,
    enabled = $10,
    sort_order = $11,
    updated_at = now()
WHERE id = $1
  AND workspace_id = $2
RETURNING *;

-- name: DeleteWorkspaceRuntimeModel :execrows
DELETE FROM workspace_runtime_model
WHERE id = $1
  AND workspace_id = $2;

-- name: CountAgentsUsingWorkspaceRuntimeModel :one
SELECT count(*)
FROM agent a
JOIN agent_runtime ar
  ON ar.id = a.runtime_id
 AND ar.workspace_id = a.workspace_id
WHERE a.workspace_id = $1
  AND ar.provider = $2
  AND a.model = $3;

-- name: DeleteWorkspaceRuntimeModelsByWorkspace :exec
DELETE FROM workspace_runtime_model
WHERE workspace_id = $1;
