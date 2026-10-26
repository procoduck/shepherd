-- name: CreateAgentToken :one
-- org_id is required for every new token (enforced in Go: the API and CLI
-- refuse a create without one); may_claim is true only when an app admin
-- (or the operator CLI) mints it; clusters/roles are jsonb string-array
-- allowlists, empty = any within the org (0038_agent_token_org_binding); a
-- NULL allowlist is stored as the empty one.
INSERT INTO agent_tokens (name, token_hash, created_by, org_id, clusters, roles, may_claim)
VALUES (
    sqlc.arg(name), sqlc.arg(token_hash), sqlc.arg(created_by), sqlc.narg(org_id),
    COALESCE(sqlc.narg(clusters)::jsonb, '[]'::jsonb), COALESCE(sqlc.narg(roles)::jsonb, '[]'::jsonb),
    sqlc.arg(may_claim)
)
RETURNING *;

-- name: CreateAgentTokenWithID :one
INSERT INTO agent_tokens (id, name, token_hash, created_by, org_id, clusters, roles, may_claim)
VALUES (
    sqlc.arg(id), sqlc.arg(name), sqlc.arg(token_hash), sqlc.arg(created_by), sqlc.narg(org_id),
    COALESCE(sqlc.narg(clusters)::jsonb, '[]'::jsonb), COALESCE(sqlc.narg(roles)::jsonb, '[]'::jsonb),
    sqlc.arg(may_claim)
)
RETURNING *;

-- name: GetAgentTokenByID :one
SELECT * FROM agent_tokens WHERE id = $1 AND revoked_at IS NULL;

-- name: ListAgentTokens :many
-- Every token when org_id is NULL (an app admin's view), else only the
-- tokens bound to that org. Unbound tokens sort first: they need an action.
SELECT sqlc.embed(t), o.name AS org_name
FROM agent_tokens t
LEFT JOIN orgs o ON o.id = t.org_id
WHERE sqlc.narg(org_id)::uuid IS NULL OR t.org_id = sqlc.narg(org_id)::uuid
ORDER BY (t.org_id IS NULL) DESC, t.created_at DESC;

-- name: RevokeAgentToken :one
-- Scoped like ListAgentTokens: with org_id set only a token bound to that org
-- matches, so a caller scoped to one org can never revoke another org's (or
-- an unbound) token. No row → pgx.ErrNoRows → NotFound.
UPDATE agent_tokens SET revoked_at = now(), updated_at = now()
WHERE id = sqlc.arg(id)
  AND (sqlc.narg(org_id)::uuid IS NULL OR org_id = sqlc.narg(org_id)::uuid)
RETURNING *;

-- name: BindAgentToken :one
-- Sets (or replaces) a live token's org binding in place. The secret hash is
-- untouched, so a collector keeps its credentials across a bind. may_claim is
-- untouched too: a bind never grants (or removes) the right to claim clusters
-- — that is fixed when the token is minted, so an app admin rebinding an org
-- admin's token does not turn it into a claiming one (#389).
UPDATE agent_tokens
SET org_id = sqlc.arg(org_id), clusters = sqlc.arg(clusters), roles = sqlc.arg(roles),
    updated_at = now()
WHERE id = sqlc.arg(id) AND revoked_at IS NULL
RETURNING *;
