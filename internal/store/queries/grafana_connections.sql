-- name: UpsertGrafanaConnection :one
INSERT INTO grafana_connections (org_id, base_url, token_enc)
VALUES ($1, $2, $3)
ON CONFLICT (org_id) DO UPDATE SET
    base_url   = EXCLUDED.base_url,
    token_enc  = EXCLUDED.token_enc,
    updated_at = now()
RETURNING *;

-- name: GetGrafanaConnectionByOrgID :one
SELECT * FROM grafana_connections WHERE org_id = $1;

-- name: DeleteGrafanaConnection :exec
DELETE FROM grafana_connections WHERE org_id = $1;
