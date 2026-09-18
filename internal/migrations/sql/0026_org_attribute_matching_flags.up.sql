-- 0026_org_attribute_matching_flags.up.sql
--
-- Attribute-based pipeline matching (procoduck/shepherd#139) becomes two
-- independent per-org opt-ins, mirroring 0024's allow_experimental_components
-- shape: off by default, existing orgs keep today's {cluster, role}-only
-- matching until an app admin turns one or both on. Two flags, not one,
-- because collectors.labels (admin-set) and collector_instances.local_attributes
-- (agent-reported, reachable via a compromised agent token) carry different
-- trust boundaries and ship on different timelines.
--
-- Numbered 0026, not 0025: upstream's own 0025_beacon_collector_id (#110)
-- claimed that number first, discovered as a duplicate-migration-file error
-- while rebasing this fork onto upstream main on 2026-09-18.
ALTER TABLE orgs
    ADD COLUMN allow_label_matching boolean NOT NULL DEFAULT false,
    ADD COLUMN allow_local_attribute_matching boolean NOT NULL DEFAULT false;
