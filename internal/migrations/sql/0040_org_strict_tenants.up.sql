-- Strict tenant mode, per organisation.
--
-- With strict_tenants on, every destination, destination binding and tenant
-- route of the org must use a tenant the org owns (org_tenants: one of its
-- exact tenants, or a tenant under one of its prefixes), and pipeline text
-- that sets a literal tenant the org does not own is refused by the
-- validation gate. Only an app admin turns it on or off
-- (AdminService.SetOrgStrictTenants), and turning it on is refused while
-- anything of the org would break the rule. Off by default: an existing org
-- behaves exactly as before.
ALTER TABLE orgs ADD COLUMN strict_tenants boolean NOT NULL DEFAULT false;

-- What each ENABLED pipeline's text literally writes to (X-Scope-OrgID
-- headers, tenant_id attributes, the __tenant_id__ label;
-- validate.TenantLiterals), recorded by every pipeline write under the shared
-- ownership lock and cleared when the pipeline is disabled. AddOrgTenant
-- refuses a rule covering a tenant another org's enabled pipeline writes to
-- with one indexed query instead of parsing every pipeline. SQL cannot parse
-- Alloy, so the backfill is the server's: every boot rebuilds the table
-- under the exclusive ownership lock (tenancy.RebuildPipelineTenants).
CREATE TABLE pipeline_tenants (
    pipeline_id uuid NOT NULL REFERENCES pipelines (id) ON DELETE CASCADE,
    org_id uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    tenant text COLLATE "C" NOT NULL,
    PRIMARY KEY (pipeline_id, tenant)
);
CREATE INDEX pipeline_tenants_tenant_idx ON pipeline_tenants (tenant);

-- Which contents pipeline_tenants describes for each enabled pipeline
-- (contents_sha256, hex sha256 of pipelines.contents), and whether the lint
-- could read every tenant in it (complete). AddOrgTenant re-records a
-- pipeline whose row is missing or whose sha no longer matches — written by a
-- pod that predates the index, during a rolling upgrade — before it checks.
CREATE TABLE pipeline_tenant_index (
    pipeline_id uuid PRIMARY KEY REFERENCES pipelines (id) ON DELETE CASCADE,
    org_id uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    contents_sha256 text NOT NULL,
    complete boolean NOT NULL
);
