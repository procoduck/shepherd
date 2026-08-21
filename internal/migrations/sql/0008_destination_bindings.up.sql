-- 0008_destination_bindings.up.sql
-- W2 (docs/gateway-tier-plan.md §4): destination templates + tenant bindings.
--
-- A "template" is not a new concept bolted onto `destinations` -- it IS a
-- destination row, unchanged. This migration does not ALTER destinations at
-- all: every row that exists today (dev seed, e2e fixtures) keeps working
-- standalone, exactly as before. Any destination can act as a template
-- simply by having one or more bindings point at it; nothing marks a row as
-- "is a template" because nothing needs to.
--
-- A *binding* is a new child table, not a nullable-parent flag on
-- `destinations`. This is the load-bearing design decision: a binding row
-- has NO url / type / secret_name / secret_namespace / auth_mode / extra
-- columns at all -- the credential-bearing fields simply do not exist in
-- this table's shape. "a binding cannot override the credential" is
-- therefore enforced by the schema itself (there is no column to write it
-- into), not merely by application code choosing to ignore extra request
-- fields. The service layer (internal/mgmtapi) additionally refuses a
-- request that *tries* to set one of those fields on a binding, so the
-- rejection is loud rather than a silent drop -- see
-- rpc_destination.go:rejectCredentialOverride. The only column a binding
-- contributes on top of its template is tenant_id.
--
-- destination_id has no ON DELETE CASCADE: deleting a template out from
-- under bindings still in use by teams would silently break them. The
-- default (RESTRICT-equivalent, "NO ACTION") FK behavior makes
-- DeleteDestination fail with a foreign_key_violation (SQLSTATE 23503),
-- which the service layer maps to the same already-exists/"in use" shape as
-- the existing wizard-pipeline reference check (see isFKViolation,
-- internal/mgmtapi/helpers.go).
CREATE TABLE destination_bindings (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    destination_id uuid        NOT NULL REFERENCES destinations(id),
    org_id         uuid        NOT NULL REFERENCES orgs(id),
    name           text        NOT NULL,
    tenant_id      text        NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, name)
);

-- Backs both ListDestinationBindingsByDestination and the FK-violation path
-- above (Postgres does not automatically index the referencing column of a
-- FK, and DeleteDestination's existence check benefits from it same as any
-- other lookup by destination_id).
CREATE INDEX idx_destination_bindings_destination_id ON destination_bindings (destination_id);
