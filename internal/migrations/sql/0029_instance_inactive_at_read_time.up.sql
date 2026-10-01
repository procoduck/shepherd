-- #237: "inactive" is no longer stored in remote_config_status. It is derived
-- at read time from last_seen and agent.inactive_after, so the agent's
-- reported outcome and the instance's staleness are independent.
--
-- Rows the old lifecycle sweeper overwrote with the 'inactive' sentinel have
-- lost the outcome that was there before; nothing can recover it. NULL ("no
-- outcome known") is the honest replacement, and it is exactly what the old
-- code turned 'inactive' into when the instance reconnected
-- (UpsertCollectorInstance), so a returning instance takes the same path it
-- would have before this migration: its next status, or a silent poll with
-- the served hash, sets it. While such a row stays stale it still presents as
-- inactive under the new read-time rule, because its last_seen is unchanged.
-- FAILED rows were never overwritten (since #197), so no failure is erased.
UPDATE collector_instances
SET remote_config_status = NULL,
    remote_config_error  = NULL,
    updated_at           = now()
WHERE remote_config_status = 'inactive';
