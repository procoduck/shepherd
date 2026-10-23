-- name: ListOrgTenants :many
-- One org's tenants and prefixes (0039_org_tenants), default first.
SELECT * FROM org_tenants WHERE org_id = $1 ORDER BY is_default DESC, kind, tenant;

-- name: ListAllOrgTenants :many
-- Every org's tenants, for ListOrgs and the cross-org ownership checks
-- (gateway.OrgOwnsTenant over each other org's rules). The table holds a
-- handful of rows per org.
SELECT * FROM org_tenants ORDER BY org_id, is_default DESC, kind, tenant;

-- name: ListOrgTenantsOfOtherOrgs :many
-- Every tenant and prefix held by an org other than org_id, with the holder's
-- name for the app-admin-facing conflict message. Pass the zero UUID for an
-- org not created yet.
SELECT t.*, o.name AS org_name
FROM org_tenants t JOIN orgs o ON o.id = t.org_id
WHERE t.org_id <> sqlc.arg(org_id)
ORDER BY o.name, t.kind, t.tenant;

-- name: AddOrgTenant :one
INSERT INTO org_tenants (org_id, tenant, kind, is_default, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: DeleteOrgTenant :execrows
DELETE FROM org_tenants WHERE org_id = $1 AND tenant = $2 AND kind = $3;

-- name: ClearOrgDefaultTenant :exec
UPDATE org_tenants SET is_default = false WHERE org_id = $1 AND is_default;

-- name: MarkOrgDefaultTenant :execrows
-- Only an exact tenant can be the default (the table's CHECK says so too).
UPDATE org_tenants SET is_default = true
WHERE org_id = $1 AND tenant = $2 AND kind = 'exact';

-- name: LockOrgTenantsExclusive :exec
-- Serialises, until the transaction ends, a change to who owns which tenant
-- (AddOrgTenant / RemoveOrgTenant / SetDefaultOrgTenant, CreateOrg with an
-- initial tenant) against every write that checks ownership: a prefix is a
-- relation over many tenants, so the lock is one key for the whole table
-- rather than one per tenant. namespace is advisorylock.TenantID.
SELECT pg_advisory_xact_lock(sqlc.arg(namespace)::int, 0);

-- name: LockOrgTenantsShared :exec
-- The reader side of LockOrgTenantsExclusive: a destination, binding or
-- tenant route checking a tenant's owner holds it until it commits, so
-- ownership cannot change between the check and the write. Readers do not
-- block each other.
SELECT pg_advisory_xact_lock_shared(sqlc.arg(namespace)::int, 0);

-- name: ListOtherOrgDestinationTenants :many
-- Every tenant a destination or destination binding of an org other than
-- org_id sends: what an org may not be given (exactly, or under a prefix).
-- Pass the zero UUID for an org not created yet.
SELECT DISTINCT tenant_id::text AS tenant FROM (
    SELECT d.tenant_id FROM destinations d WHERE d.org_id <> sqlc.arg(org_id) AND d.tenant_id <> ''
    UNION
    SELECT b.tenant_id FROM destination_bindings b WHERE b.org_id <> sqlc.arg(org_id) AND b.tenant_id <> ''
) u
ORDER BY tenant;

-- name: ListOrgTenantUses :many
-- Everything of one org that names a tenant: its live (not revoked) tenant
-- routes, its destinations and its destination bindings. RemoveOrgTenant
-- refuses while one of them would be left on a tenant the org no longer owns.
SELECT 'tenant route'::text AS what, (r.kind || ' route ' || r.segment)::text AS name, r.tenant_id::text AS tenant
FROM tenant_routes r WHERE r.org_id = sqlc.arg(org_id) AND r.status <> 'revoked'
UNION ALL
SELECT 'destination'::text, d.name::text, d.tenant_id::text
FROM destinations d WHERE d.org_id = sqlc.arg(org_id) AND d.tenant_id <> ''
UNION ALL
SELECT 'destination binding'::text, b.name::text, b.tenant_id::text
FROM destination_bindings b WHERE b.org_id = sqlc.arg(org_id) AND b.tenant_id <> ''
ORDER BY what, name;
