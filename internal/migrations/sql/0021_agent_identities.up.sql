-- Bindings that map a collector's OIDC identity to an organisation, for the
-- recommended "unique client per org, Shepherd maps it" model
-- (docs/plans/2026-09-16-agent-oidc-auth.md, resolution mode 1). A collector
-- authenticates with an OIDC access token; the (issuer, app_id) it carries is
-- looked up here to decide which org it acts for. Org assignment stays in
-- Shepherd — the IdP is never authoritative for org membership in the first
-- cut.
CREATE TABLE agent_identities (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- issuer + app_id is the token's stable identity: the verified `iss` and
    -- the configured app-identity claim (sub / azp / client_id).
    issuer     text        NOT NULL,
    app_id     text        NOT NULL,
    org_id     uuid        NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    -- Optional allowlists narrowing what this identity may act as. Empty array
    -- means "any within the org". Stored as jsonb string arrays, validated in
    -- Go before write; a CHECK keeps a hand-written row honest.
    clusters   jsonb       NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(clusters) = 'array'),
    roles      jsonb       NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(roles) = 'array'),
    created_by text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    -- One binding per (issuer, app_id): a client's identity resolves to exactly
    -- one org.
    UNIQUE (issuer, app_id)
);

CREATE INDEX agent_identities_org_id_idx ON agent_identities (org_id);
