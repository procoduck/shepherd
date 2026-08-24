-- name: InsertAuditLog :exec
-- on_behalf_of is G13's second attribution half (docs/gateway-tier-plan.md):
-- the human a machine actor's write is performed for. NULL for every human
-- session's own action; a 'service_account' actor_type row always carries
-- one, because internal/mgmtapi.requireWriteAuthorized rejects a machine
-- write with no on-behalf-of before any InsertAuditLog call is reached — see
-- that function's doc comment for the reject-vs-record-as-unattributed
-- decision.
INSERT INTO audit_log (actor, actor_type, org_id, action, resource_type, resource_id, detail, on_behalf_of)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ListAuditLog :many
-- include_global ($6) widens an org-scoped query to also return rows with a
-- NULL org_id: platform-level events that belong to no org. Single sign-on
-- configuration is the first of these (internal/mgmtapi/rpc_admin_oidc.go),
-- and without this they were WRITTEN but unreachable — every caller of this
-- query passes an org, so `org_id = $1` silently excluded them and the audit
-- trail for repointing the identity provider could only be read with psql.
--
-- It is a parameter rather than an unconditional OR because these rows are
-- app-admin business: an org admin looking at their own org's trail should not
-- start seeing platform events. The handler passes the caller's app-admin
-- status, so the gate is an authorization decision, not a UI preference.
SELECT * FROM audit_log
WHERE (($1::uuid IS NULL OR org_id = $1) OR ($6::bool AND org_id IS NULL))
  AND (NULLIF($2::text, '') IS NULL OR actor ILIKE '%' || $2 || '%')
  AND (NULLIF($3::text, '') IS NULL OR action = $3)
ORDER BY at DESC
LIMIT $4 OFFSET $5;

-- name: CountAuditLog :one
-- Predicate kept identical to ListAuditLog, include_global included: a total
-- that counted a different set than the page it labels is a paginator that
-- lies.
SELECT COUNT(*)::int AS total FROM audit_log
WHERE (($1::uuid IS NULL OR org_id = $1) OR ($4::bool AND org_id IS NULL))
  AND (NULLIF($2::text, '') IS NULL OR actor ILIKE '%' || $2 || '%')
  AND (NULLIF($3::text, '') IS NULL OR action = $3);
