-- Back to one tenant identity per org (0013's orgs.tenant_id). Each org keeps
-- its default tenant; an org with tenants but no default keeps none, and its
-- other tenants and prefixes are lost.
ALTER TABLE orgs
    ADD COLUMN tenant_id text
        CHECK (tenant_id IS NULL OR (
            tenant_id ~ '^[0-9a-zA-Z!\-_.*''()]+$'
            AND length(tenant_id) <= 150
            AND tenant_id NOT IN ('.', '..', '__mimir_cluster')
        ));

UPDATE orgs o
SET tenant_id = t.tenant
FROM org_tenants t
WHERE t.org_id = o.id AND t.is_default;

CREATE UNIQUE INDEX idx_orgs_tenant_id ON orgs (tenant_id) WHERE tenant_id IS NOT NULL;

DROP TABLE org_tenants;
