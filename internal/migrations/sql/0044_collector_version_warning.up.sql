-- 0044_collector_version_warning.up.sql
-- #375: what the served config's version check found for this collector.
--
-- Stage 3 refuses a save whose merged config uses a component, block or
-- attribute missing from the Alloy version of any of a collector's ACTIVE
-- instances (internal/validate.CheckAlloyVersions). A collector's instances
-- change after the save — an older pod joins — and serving continues then
-- (the config was validated), so the agent API re-runs the check when it
-- hands the config out and records the outcome here:
--
--   version_warning     the findings, one per line; NULL when there are none
--   version_warning_at  when version_warning last changed (set with it)
--   version_mismatch    true when a finding is a missing item — an instance
--                       will most likely reject the config — rather than a
--                       note (a version checked against the pinned schema
--                       only, a component below general availability)
--
-- version_checked_hash / version_checked_versions / version_checked_at are
-- the fingerprint of the last check (the served hash and the versions the
-- active instances reported), so a steady fleet's polls skip it: a poll
-- re-runs the check only when the served hash changes, the polling instance
-- reports a version the last check did not see (at most once a minute per
-- collector, however a version flaps), a recorded mismatch is due a
-- re-check, or version_checked_until has passed. version_checked_until is
-- set only while a warning stands: when the first of the checked versions
-- can drop out of the active set (its newest instance's last_seen plus
-- agent.inactive_after), the moment a re-check can clear it. Written by
-- the agent API only; read-only for the management API. Audited once per
-- change of version_warning (the update is a compare-and-swap on it), never
-- per poll.
--
-- Numbered 0044: #408 takes 0043.
ALTER TABLE collectors
    ADD COLUMN version_warning text,
    ADD COLUMN version_warning_at timestamptz,
    ADD COLUMN version_mismatch boolean NOT NULL DEFAULT false,
    ADD COLUMN version_checked_hash text,
    ADD COLUMN version_checked_versions text[] NOT NULL DEFAULT '{}',
    ADD COLUMN version_checked_at timestamptz,
    ADD COLUMN version_checked_until timestamptz,
    ADD CONSTRAINT collectors_version_warning_pair
        CHECK ((version_warning IS NULL) = (version_warning_at IS NULL)),
    ADD CONSTRAINT collectors_version_mismatch_has_warning
        CHECK (NOT version_mismatch OR version_warning IS NOT NULL);
