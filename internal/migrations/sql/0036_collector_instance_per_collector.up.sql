-- An instance is identified per collector: (collector_id, id), not the wire id
-- alone (review step 5, R9/R10, B-AGENT-INSTANCE). With a globally unique id
-- the first collector to report an id owned it, so an instance could be
-- rebound into another org, or squatted so its real collector was refused.
-- Now an instance never moves: a poll under a different cluster/role writes a
-- new row, and the old one ages out through agent.delete_after.
--
-- Existing rows are already one per id, so this is a constraint swap. Nothing
-- references collector_instances(id) (beacon_inventory keys on its own
-- identity, see 0010), so no foreign key needs to follow.
ALTER TABLE collector_instances DROP CONSTRAINT collector_instances_pkey;
ALTER TABLE collector_instances ADD CONSTRAINT collector_instances_pkey PRIMARY KEY (collector_id, id);

-- UnregisterCollector and GetCollectorInstanceByID still look an id up
-- across collectors.
CREATE INDEX idx_collector_instances_id ON collector_instances (id);
