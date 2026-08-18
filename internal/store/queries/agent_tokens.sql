-- name: CreateAgentToken :one
INSERT INTO agent_tokens (name, token_hash, created_by)
VALUES ($1, $2, $3)
RETURNING *;

-- name: CreateAgentTokenWithID :one
INSERT INTO agent_tokens (id, name, token_hash, created_by)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetAgentTokenByID :one
SELECT * FROM agent_tokens WHERE id = $1 AND revoked_at IS NULL;

-- name: ListAgentTokens :many
SELECT * FROM agent_tokens ORDER BY created_at DESC;

-- name: RevokeAgentToken :exec
UPDATE agent_tokens SET revoked_at = now(), updated_at = now() WHERE id = $1;
