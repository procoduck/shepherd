-- name: CreateAgentIdentity :one
-- clusters and roles are jsonb string arrays marshaled in Go; '[]' means "any".
INSERT INTO agent_identities (issuer, app_id, org_id, clusters, roles, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetAgentIdentityByAppID :one
-- The lookup the collector auth path makes: resolve a token's (issuer, app_id)
-- to its org and allowlists.
SELECT * FROM agent_identities WHERE issuer = $1 AND app_id = $2;

-- name: ListAgentIdentities :many
-- Admin/CLI listing, with the org's slug so a human can read it.
SELECT ai.*, o.name AS org_name
FROM agent_identities ai
JOIN orgs o ON o.id = ai.org_id
ORDER BY ai.created_at DESC;

-- name: DeleteAgentIdentity :execrows
DELETE FROM agent_identities WHERE issuer = $1 AND app_id = $2;
