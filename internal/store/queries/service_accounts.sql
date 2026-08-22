-- name: CreateServiceAccount :one
INSERT INTO service_accounts (org_id, name, capability, token_hash, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetServiceAccountByID :one
-- Only an unrevoked row authenticates — a revoked service account's id
-- still exists (for audit/history) but GetServiceAccountByID (the lookup
-- the auth interceptor uses) treats it as gone, the same "revoked is
-- absent, not merely flagged, at the authentication boundary" shape
-- agent_tokens' GetAgentTokenByID does not currently enforce at the query
-- layer but service_accounts does, deliberately, since this is new code
-- with no legacy behavior to preserve.
SELECT * FROM service_accounts WHERE id = $1 AND revoked_at IS NULL;

-- name: ListServiceAccountsByOrg :many
SELECT * FROM service_accounts WHERE org_id = $1 ORDER BY name;

-- name: RevokeServiceAccount :exec
UPDATE service_accounts SET revoked_at = now(), updated_at = now() WHERE id = $1;
