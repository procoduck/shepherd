-- name: CreateADOCredential :one
INSERT INTO ado_credentials (org_id, name, ado_org_url, entra_tenant_id, client_id, client_secret_enc)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetADOCredentialByID :one
SELECT * FROM ado_credentials WHERE id = $1;

-- name: ListADOCredentialsByOrg :many
SELECT * FROM ado_credentials WHERE org_id = $1 ORDER BY name;

-- name: UpdateADOCredential :one
UPDATE ado_credentials
SET name              = $2,
    ado_org_url       = $3,
    entra_tenant_id   = $4,
    client_id         = $5,
    client_secret_enc = $6,
    updated_at        = now()
WHERE id = $1
RETURNING *;

-- name: DeleteADOCredential :exec
DELETE FROM ado_credentials WHERE id = $1;
