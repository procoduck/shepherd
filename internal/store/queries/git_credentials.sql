-- name: CreateGitCredential :one
INSERT INTO git_credentials (
    org_id, name, kind, username,
    ado_org_url, entra_tenant_id, client_id,
    client_secret_enc, secret2_enc,
    provider_config, ssh_known_hosts, ca_cert, tls_insecure_skip_verify
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: GetGitCredentialByID :one
SELECT * FROM git_credentials WHERE id = $1;

-- name: ListGitCredentialsByOrg :many
SELECT * FROM git_credentials WHERE org_id = $1 ORDER BY name;

-- name: DeleteGitCredential :exec
DELETE FROM git_credentials WHERE id = $1;
