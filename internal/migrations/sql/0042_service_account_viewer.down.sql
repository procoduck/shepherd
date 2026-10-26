-- 0042_service_account_viewer.down.sql
-- The pre-0042 schema cannot hold a viewer service account: role admitted
-- only editor and admin, and capability was required. Converting a viewer
-- to either tier would widen what the credential reaches, so viewer rows are
-- deleted instead — their credentials stop authenticating, which is the safe
-- direction. Nothing references service_accounts by foreign key; audit rows
-- name the account by text ("svcacct:<name>") and are kept.
DELETE FROM service_accounts WHERE role = 'viewer';

ALTER TABLE service_accounts
    DROP CONSTRAINT service_accounts_role_capability,
    ALTER COLUMN capability SET NOT NULL,
    DROP CONSTRAINT service_accounts_role_check,
    ADD CONSTRAINT service_accounts_role_check CHECK (role IN ('editor', 'admin'));
