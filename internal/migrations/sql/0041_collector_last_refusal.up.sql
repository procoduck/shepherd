-- The latest identity refusal of a poll for this collector (cluster + role).
--
-- A refused poll (token_unbound, claim_denied, role_denied) upserts the
-- cluster and collector rows before the org check and deliberately writes no
-- instance row (#370), so the instance's last_seen and remote_config_status
-- keep reading the last successful poll — the Collectors page showed APPLIED
-- for a fleet whose every poll was refused. The agent API records the refusal
-- here instead, at most once per refusal-log window per collector and reason,
-- and clears it on the next poll that passes the org check. Read-only for the
-- management API, which presents it in place of the instance status while it
-- is newer than the collector's last successful poll.
ALTER TABLE collectors
    ADD COLUMN last_refused_at timestamptz,
    ADD COLUMN last_refused_reason text
        CHECK (last_refused_reason IN ('token_unbound', 'claim_denied', 'role_denied')),
    ADD CONSTRAINT collectors_last_refusal_pair
        CHECK ((last_refused_at IS NULL) = (last_refused_reason IS NULL));
