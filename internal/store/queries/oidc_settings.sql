-- name: GetOIDCSettings :one
SELECT * FROM oidc_settings WHERE id = true;

-- name: UpsertOIDCSettings :one
INSERT INTO oidc_settings (
    id, enabled, provider, display_name, issuer, client_id, client_secret_enc,
    redirect_url, scopes, subject_claim, email_claim, name_claim,
    groups_claim, app_admin_groups, use_graph_groups, graph_base_url, updated_by
) VALUES (
    true, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16
)
ON CONFLICT (id) DO UPDATE SET
    enabled           = EXCLUDED.enabled,
    provider          = EXCLUDED.provider,
    display_name      = EXCLUDED.display_name,
    issuer            = EXCLUDED.issuer,
    client_id         = EXCLUDED.client_id,
    client_secret_enc = EXCLUDED.client_secret_enc,
    redirect_url      = EXCLUDED.redirect_url,
    scopes            = EXCLUDED.scopes,
    subject_claim     = EXCLUDED.subject_claim,
    email_claim       = EXCLUDED.email_claim,
    name_claim        = EXCLUDED.name_claim,
    groups_claim      = EXCLUDED.groups_claim,
    app_admin_groups  = EXCLUDED.app_admin_groups,
    use_graph_groups  = EXCLUDED.use_graph_groups,
    graph_base_url    = EXCLUDED.graph_base_url,
    updated_by        = EXCLUDED.updated_by,
    updated_at        = now()
RETURNING *;

-- name: DeleteOIDCSettings :exec
DELETE FROM oidc_settings WHERE id = true;
