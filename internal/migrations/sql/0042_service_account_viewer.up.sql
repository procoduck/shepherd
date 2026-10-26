-- 0042_service_account_viewer.up.sql
-- #362 (review step 7b, R14): a service account may hold the viewer tier.
-- A viewer service account gets exactly what an org viewer gets: the
-- interceptor (internal/mgmtapi/rpc_interceptor.go,
-- authorizeServiceAccountProcedure) already compares the stored tier with
-- auth.Role.AtLeast, so admitting 'viewer' here is the whole of the reach
-- change.
--
-- capability (0012, propose vs apply: "may this write at all") has no
-- meaning for a tier that never writes, so a viewer carries none: it is NULL
-- for a viewer and required, propose or apply, for editor and admin. A
-- viewer with a capability is refused rather than ignored, so no row can
-- read as "a viewer that may apply". requireWriteAuthorized
-- (internal/mgmtapi/machine_auth.go) refuses every write by a caller whose
-- capability is not apply, so a NULL capability never passes it.
--
-- Existing rows are editor or admin with a capability, so they satisfy every
-- constraint below unchanged; nothing is backfilled.
ALTER TABLE service_accounts
    DROP CONSTRAINT service_accounts_role_check,
    ADD CONSTRAINT service_accounts_role_check CHECK (role IN ('viewer', 'editor', 'admin')),
    ALTER COLUMN capability DROP NOT NULL,
    ADD CONSTRAINT service_accounts_role_capability CHECK (
        (role = 'viewer' AND capability IS NULL)
        OR (role IN ('editor', 'admin') AND capability IS NOT NULL AND capability IN ('propose', 'apply'))
    );
