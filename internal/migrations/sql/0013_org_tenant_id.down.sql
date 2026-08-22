DROP INDEX IF EXISTS idx_orgs_tenant_id;
ALTER TABLE orgs DROP COLUMN IF EXISTS tenant_id;
