-- Tenant identity belongs to the ORG, and only an app admin may set it.
--
-- Before this migration, CreateTenantRoute accepted tenant_id as free-form
-- request input, so an org admin (or an apply-capability service account in
-- their org) could mint an active route whose injected X-Scope-OrgID named a
-- DIFFERENT org's tenant. Once the apply slice gets a production caller, that
-- row becomes a gateway-blessed endpoint stamping the victim's tenant onto
-- attacker-chosen data — a cross-tenant write authorized by the wrong org.
-- A final review found this while it was still latent; this migration is the
-- fix, and it works by removing the choice rather than by validating it.
--
-- tenant_id is nullable because orgs created before this migration have none.
-- An org without one cannot have tenant routes at all: CreateTenantRoute
-- refuses with a message naming the app-admin action needed. That is
-- deliberately a loud stop rather than a backfilled default — inventing a
-- tenant identity for an existing org would silently decide which tenant its
-- telemetry lands in, which is exactly the decision this column exists to
-- keep in human hands.
ALTER TABLE orgs
    ADD COLUMN tenant_id text
        -- Mirrors internal/gateway.ValidateTenantID, which is Grafana Mimir's
        -- own documented rule (alphanumerics plus ! - _ . * ' ( ), max 150
        -- bytes, no slashes or whitespace). Duplicated here on purpose, the
        -- same defense-in-depth 0009 applies to segments: the Go validator is
        -- what produces a good error message, and this is what makes a bad
        -- value unstorable even if some future write path forgets to call it.
        CHECK (tenant_id IS NULL OR (
            tenant_id ~ '^[0-9a-zA-Z!\-_.*''()]+$'
            AND length(tenant_id) <= 150
            AND tenant_id NOT IN ('.', '..', '__mimir_cluster')
        ));

-- One tenant identity, one org. Without this, two orgs could be given the
-- same tenant_id and the gateway would stamp both orgs' traffic with one
-- identity — re-introducing the cross-tenant merge this change exists to
-- prevent, just by a slower route. Partial so the NULLs of not-yet-configured
-- orgs do not collide with each other.
CREATE UNIQUE INDEX idx_orgs_tenant_id ON orgs (tenant_id) WHERE tenant_id IS NOT NULL;
