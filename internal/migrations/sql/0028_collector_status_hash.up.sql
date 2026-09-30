-- #115 (B1): remote_config_status_hash is the config hash the agent reported
-- its remote_config_status FOR. Alloy (verified on v1.20.1,
-- docs/proofs/applied-status.md) reports FAILED exactly once, with the hash of
-- the config it rejected, and then keeps polling with that same hash and no
-- status while it runs its previous config. Without this column
-- ClearStaleFailedStatus could not tell that silence from recovery and turned
-- the FAILED into APPLIED on the next poll. NULL for rows that predate it.
ALTER TABLE collector_instances ADD COLUMN remote_config_status_hash text;
