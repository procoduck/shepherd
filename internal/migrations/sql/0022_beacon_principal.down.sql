-- Reverse of 0022. A row whose principal is not a UUID (an OIDC collector's
-- "oidc:...") cannot be represented once token_id is a uuid FK again, so drop
-- those rows first; they are ephemeral self-monitoring inventory that a
-- collector re-reports within a scrape interval.
DELETE FROM beacon_inventory WHERE principal !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$';
ALTER TABLE beacon_inventory ADD COLUMN token_id uuid REFERENCES agent_tokens(id);
UPDATE beacon_inventory SET token_id = principal::uuid WHERE token_id IS NULL;
ALTER TABLE beacon_inventory ALTER COLUMN token_id SET NOT NULL;

DROP INDEX idx_beacon_inventory_principal;
CREATE INDEX idx_beacon_inventory_token ON beacon_inventory (token_id);
ALTER TABLE beacon_inventory DROP CONSTRAINT beacon_inventory_principal_instance_component_key;
ALTER TABLE beacon_inventory
    ADD CONSTRAINT beacon_inventory_token_id_instance_label_component_name_key
    UNIQUE (token_id, instance_label, component_name);

ALTER TABLE beacon_inventory DROP CONSTRAINT beacon_inventory_principal_len;
ALTER TABLE beacon_inventory DROP COLUMN principal;
