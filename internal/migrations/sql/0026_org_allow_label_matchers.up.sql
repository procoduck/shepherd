-- 0026_org_allow_label_matchers.up.sql
--
-- #139: opt an org into using admin-set collector labels (collectors.labels) as
-- pipeline matcher keys, alongside the built-in {cluster, role}. Off by default,
-- so no org's served config changes until an app admin turns it on — the same
-- rollout pattern as orgs.allow_experimental_components (0024). This migration
-- only adds the flag; the matcher wiring that reads it lands in a follow-up.
ALTER TABLE orgs
    ADD COLUMN allow_label_matchers boolean NOT NULL DEFAULT false;
