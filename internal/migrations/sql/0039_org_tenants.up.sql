-- An organisation can own several tenants and tenant prefixes.
--
-- Until now an org had one tenant identity (orgs.tenant_id, 0013), set once
-- by an app admin. org_tenants replaces it: each row is either an EXACT
-- tenant the org owns, or a PREFIX under which the org owns every valid
-- tenant. Only an app admin writes this table (AdminService.AddOrgTenant /
-- RemoveOrgTenant / SetDefaultOrgTenant).
--
-- D11's property still holds — a tenant belongs to at most one org:
--   * an exact tenant is unique across ALL orgs (org_tenants_exact_unique);
--   * a prefix may not overlap another org's prefix (neither a prefix of the
--     other), and may not cover another org's exact tenant, nor may an exact
--     tenant fall under another org's prefix.
-- The second rule is a prefix relation SQL cannot index, so the API enforces
-- it under one transaction-scoped advisory lock (advisorylock.TenantID) that
-- every ownership-relevant write takes; the indexes below are what SQL can
-- enforce on its own.
--
-- The default tenant is what a tenant route uses when none is named, what
-- chart values carry and what the destination form pre-fills. At most one per
-- org, and always an exact tenant.
CREATE TABLE org_tenants (
    org_id     uuid        NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    tenant     text        NOT NULL,
    kind       text        NOT NULL CHECK (kind IN ('exact', 'prefix')),
    is_default boolean     NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by text        NOT NULL DEFAULT '',
    PRIMARY KEY (org_id, kind, tenant),
    -- Mirrors internal/gateway.ValidateTenantID (Grafana Mimir's documented
    -- rule, the same CHECK 0013 put on orgs.tenant_id) for an exact tenant,
    -- and gateway.ValidateTenantPrefix for a prefix: the same charset, and
    -- short enough that at least one more character fits under the limit.
    CHECK (tenant ~ '^[0-9a-zA-Z!\-_.*''()]+$'),
    CHECK (
        (kind = 'exact' AND length(tenant) <= 150 AND tenant NOT IN ('.', '..', '__mimir_cluster'))
        OR (kind = 'prefix' AND length(tenant) <= 149)
    ),
    CHECK (NOT is_default OR kind = 'exact')
);

-- An exact tenant belongs to one org.
CREATE UNIQUE INDEX org_tenants_exact_unique ON org_tenants (tenant) WHERE kind = 'exact';
-- Two orgs holding the very same prefix is the simplest overlap.
CREATE UNIQUE INDEX org_tenants_prefix_unique ON org_tenants (tenant) WHERE kind = 'prefix';
-- At most one default per org.
CREATE UNIQUE INDEX org_tenants_one_default ON org_tenants (org_id) WHERE is_default;

-- Every org that had a tenant keeps it, as its default exact tenant.
INSERT INTO org_tenants (org_id, tenant, kind, is_default, created_at, created_by)
SELECT id, tenant_id, 'exact', true, updated_at, 'migration:0039'
FROM orgs
WHERE tenant_id IS NOT NULL;

DROP INDEX IF EXISTS idx_orgs_tenant_id;
ALTER TABLE orgs DROP COLUMN tenant_id;
