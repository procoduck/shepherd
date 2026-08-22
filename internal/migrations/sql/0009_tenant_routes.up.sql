-- 0009_tenant_routes.up.sql
-- W4 (docs/gateway-tier-plan.md §4, D8, D9): tenant route records — the
-- storage layer above internal/gateway's HTTPRoute renderer.
-- internal/gateway.RenderHTTPRoute (route.go) already treats
-- RouteSpec.RouteSegment as an opaque value; internal/gateway.GenerateSegment
-- (segment.go) is where a segment is minted; this table is where the result
-- is stored, looked up for uniqueness, and rotated.
--
-- Numbered 0009, not 0008: 0008_destination_bindings landed on main (W2)
-- after this branch (W3/W4) forked from it, so 0008 is reserved for that
-- migration when the branches merge -- this avoids two different
-- migrations both claiming version 0008 on the same history.
--
-- Rotation is modeled in the row, not bolted on later (an explicit W4
-- requirement): issuing a new segment inserts a new 'active' row
-- (rotated_from_id pointing at the row it replaces) and demotes the
-- previous row to 'deprecated' with a valid_until deadline, so both
-- segments keep routing traffic during the overlap window (D9: "issue new,
-- run both briefly, revoke old"). A later slice's janitor sweep (not built
-- here) flips expired 'deprecated' rows to 'revoked'; RevokeTenantRoute
-- also does it immediately, on demand. status is a plain TEXT + CHECK
-- rather than a Postgres ENUM, matching this repo's documented "enum-like
-- fields stay strings" contract rule (see 0007_simulate_runs.up.sql's
-- comment for the precedent) -- a new status value is then an ordinary
-- migration, not an ENUM ADD VALUE dance.
--
-- gateway_mode captures D8's two ownership modes without a later migration:
-- 'managed' (Shepherd creates/owns the Gateway object) or 'operator'
-- (Shepherd only creates HTTPRoutes with a parentRef at a Gateway the
-- operator runs out of band). Neither the Gateway apply nor the HTTPRoute
-- apply happens in this slice -- that is explicitly the next one (plan §5's
-- W4 boundary); this migration only makes the model able to express both
-- modes so that slice does not need a schema change to add the second one.
--
-- The segment CHECK constraint mirrors internal/gateway.segmentCharsetRE
-- exactly (lowercase alnum, single internal hyphens only) as a second,
-- independent enforcement point: even a bug that bypassed Go-level
-- validation could not get a malformed segment past the database. This is
-- deliberate defense in depth, not redundancy to be trimmed -- see the CI
-- red-run notes in this migration's PR description.
CREATE TABLE tenant_routes (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            uuid        NOT NULL REFERENCES orgs(id),
    tenant_id         text        NOT NULL,
    kind              text        NOT NULL CHECK (kind IN ('otlp', 'faro')),
    segment           text        NOT NULL
                          CHECK (segment ~ '^[a-z0-9]+(-[a-z0-9]+)*$'
                                 AND length(segment) BETWEEN 3 AND 63),
    status            text        NOT NULL DEFAULT 'active'
                          CHECK (status IN ('active', 'deprecated', 'revoked')),
    valid_until       timestamptz,
    rotated_from_id   uuid        REFERENCES tenant_routes(id),
    gateway_mode      text        NOT NULL CHECK (gateway_mode IN ('managed', 'operator')),
    gateway_name      text        NOT NULL,
    gateway_namespace text        NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    revoked_at        timestamptz,
    UNIQUE (kind, segment)
);

-- Backs ListTenantRoutesByOrg and the by-tenant/kind lookups
-- GetActiveTenantRoute needs (find the current active row before rotating).
CREATE INDEX idx_tenant_routes_org_tenant ON tenant_routes (org_id, tenant_id);

-- Enforces "at most one active segment per org+tenant+kind" at the schema
-- level: rotation is a state transition (active -> deprecated on the old
-- row, then a new active row referencing it), never two concurrently-active
-- rows for the same tenant+kind. A partial index so deprecated/revoked
-- history rows for the same tenant+kind stay unconstrained -- rotation
-- history is meant to accumulate.
CREATE UNIQUE INDEX idx_tenant_routes_one_active
    ON tenant_routes (org_id, tenant_id, kind)
    WHERE status = 'active';
