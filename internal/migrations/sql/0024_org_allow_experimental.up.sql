-- 0024_org_allow_experimental.up.sql
--
-- #114: experimental Alloy components become a per-org opt-in. Off by default —
-- an org sees only GA / public-preview components in the visual builder, and the
-- server refuses to render a graph that uses an experimental component, until an
-- app admin turns this on for the org. Additive; existing orgs default to the
-- prior behaviour (experimental gated off).
ALTER TABLE orgs
    ADD COLUMN allow_experimental_components boolean NOT NULL DEFAULT false;
