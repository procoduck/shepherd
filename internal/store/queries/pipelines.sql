-- name: CreatePipeline :one
INSERT INTO pipelines (org_id, name, contents, matchers, enabled, source, wizard_kind, wizard_state, created_by, updated_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetPipelineByID :one
SELECT * FROM pipelines WHERE id = $1;

-- name: GetPipelineByOrgAndName :one
SELECT * FROM pipelines WHERE org_id = $1 AND name = $2;

-- name: ListPipelinesByOrg :many
SELECT * FROM pipelines WHERE org_id = $1 ORDER BY name;

-- name: UpdatePipeline :one
UPDATE pipelines
SET name       = $2,
    contents   = $3,
    matchers   = $4,
    updated_by = $5,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetPipelineEnabled :one
UPDATE pipelines
SET enabled    = $2,
    updated_by = $3,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeletePipeline :exec
DELETE FROM pipelines WHERE id = $1;

-- name: ListEnabledPipelinesByOrg :many
SELECT * FROM pipelines WHERE org_id = $1 AND enabled = true ORDER BY name;
