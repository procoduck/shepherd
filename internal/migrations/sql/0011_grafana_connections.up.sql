-- 0011_grafana_connections.up.sql
-- D7 (docs/gateway-tier-plan.md §2, §5): storage for the optional Grafana
-- service-account token that lets Shepherd query a destination
-- (/api/ds/query) and answer "did the data actually arrive?".
--
-- Secret-storage pattern: this repo already has two, and they differ on
-- purpose. destinations (0001_init.up.sql) stores secret_name/secret_namespace
-- -- a REFERENCE to a Kubernetes Secret that Alloy itself reads at scrape/push
-- time; Shepherd never needs the plaintext because Shepherd never calls the
-- destination. git_credentials (0006_git_providers.up.sql) stores
-- client_secret_enc -- the ACTUAL secret, AES-256-GCM-encrypted at rest via
-- internal/crypto, because Shepherd itself is the caller (git ls-remote,
-- token exchange) and must hold the plaintext in memory at call time.
--
-- The Grafana token is the second shape, not the first: Shepherd is the one
-- calling /api/ds/query, not Alloy, so a name+namespace indirection would
-- just relocate the problem -- Shepherd would still need to resolve and read
-- that Secret's plaintext itself. token_enc therefore follows
-- git_credentials.client_secret_enc's exact precedent: bytea, encrypted with
-- the same internal/crypto.Encryptor, decrypted only in-process at query
-- time, and never logged or returned (internal/grafana.ConnectionStore has
-- no method that returns the plaintext to a caller -- see connection.go).
--
-- Scope: one Grafana connection per org, matching destinations' org_id
-- scoping -- UNIQUE(org_id) means "configure Grafana for this org" is a
-- single upsert, not a growing list. base_url is validated at the HTTP-scheme
-- CHECK below as defense in depth (mirrors 0009/0010's documented precedent
-- of a CHECK mirroring a Go-level bound), independent of whatever validation
-- internal/grafana.ConnectionStore.Set performs before this INSERT runs.
CREATE TABLE grafana_connections (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     uuid        NOT NULL REFERENCES orgs(id),
    base_url   text        NOT NULL CHECK (base_url ~ '^https?://[^[:space:]]+$' AND length(base_url) <= 2048),
    token_enc  bytea       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id)
);
