-- Generalise the beacon inventory's identity from an agent-token UUID to a
-- free-form principal, so an OIDC-authenticated collector (which has no
-- agent_tokens row) can report self-monitoring too
-- (docs/plans/2026-09-16-agent-oidc-auth.md, Phase 2). The identity was always
-- "the credential that reported" (0010's header); it is now a text principal:
-- the agent token's UUID as before, or "oidc:<issuer>|<sub>" for an OIDC
-- collector. The agent_tokens FK goes with the old column — the handler only
-- ever writes a principal it just authenticated, so app-layer integrity is
-- unchanged, and the FK could not have referenced an OIDC identity anyway.
ALTER TABLE beacon_inventory ADD COLUMN principal text;
UPDATE beacon_inventory SET principal = token_id::text WHERE principal IS NULL;
ALTER TABLE beacon_inventory ALTER COLUMN principal SET NOT NULL;
ALTER TABLE beacon_inventory
    ADD CONSTRAINT beacon_inventory_principal_len CHECK (length(principal) BETWEEN 1 AND 512);

-- Swap the identity's unique constraint and index onto the new column.
ALTER TABLE beacon_inventory
    DROP CONSTRAINT beacon_inventory_token_id_instance_label_component_name_key;
ALTER TABLE beacon_inventory
    ADD CONSTRAINT beacon_inventory_principal_instance_component_key
    UNIQUE (principal, instance_label, component_name);
DROP INDEX idx_beacon_inventory_token;
CREATE INDEX idx_beacon_inventory_principal ON beacon_inventory (principal);

-- Dropping token_id drops its FK to agent_tokens with it.
ALTER TABLE beacon_inventory DROP COLUMN token_id;
