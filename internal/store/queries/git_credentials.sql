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

-- name: UpdateGitCredential :one
UPDATE git_credentials
SET name                     = $2,
    kind                     = $3,
    username                 = $4,
    ado_org_url              = $5,
    entra_tenant_id          = $6,
    client_id                = $7,
    client_secret_enc        = $8,
    secret2_enc              = $9,
    provider_config          = $10,
    ssh_known_hosts          = $11,
    ca_cert                  = $12,
    tls_insecure_skip_verify = $13,
    updated_at               = now()
WHERE id = $1
RETURNING *;

-- name: DeleteGitCredential :exec
DELETE FROM git_credentials WHERE id = $1;
