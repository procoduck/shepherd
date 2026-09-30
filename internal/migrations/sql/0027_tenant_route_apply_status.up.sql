-- 0027_tenant_route_apply_status.up.sql
--
-- Tenant-route apply (docs/plans/2026-09-29-tenant-route-apply.md): a
-- background reconciler puts each route's HTTPRoute in the cluster, or
-- removes it, and records the outcome here so the UI can say whether a
-- route actually routes. Additive; every existing route starts `pending`
-- until the reconciler has looked at it.
--
--   pending        not yet reconciled, or changed since
--   applied        HTTPRoute present and attachment verified (applied_at)
--   refused        the gateway refused attachment (reason in apply_message)
--   error          anything else went wrong (apply_message); retried
--   removed        revoked/expired route whose HTTPRoute has been deleted
--   not_applicable nothing to apply: a Faro route, or apply disabled
ALTER TABLE tenant_routes
    ADD COLUMN apply_status  text NOT NULL DEFAULT 'pending'
        CHECK (apply_status IN ('pending', 'applied', 'refused', 'error', 'removed', 'not_applicable')),
    ADD COLUMN apply_message text NOT NULL DEFAULT '',
    ADD COLUMN applied_at    timestamptz;
