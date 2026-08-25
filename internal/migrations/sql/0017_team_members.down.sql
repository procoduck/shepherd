DROP TABLE IF EXISTS team_members;

-- Restoring NOT NULL needs a value for every group-less team, and '' would
-- collide under UNIQUE (org_id, idp_group_id) the moment an org has two of
-- them. A synthetic per-row value keeps the rollback non-destructive: the
-- teams and their pipeline ownership survive, and the placeholder matches no
-- real IdP group, so it grants nobody anything. It is visible in the UI,
-- which is the honest outcome -- the information that these teams had no
-- group is not recoverable from a schema that cannot express it.
UPDATE teams SET idp_group_id = 'no-group:' || id::text WHERE idp_group_id IS NULL;
ALTER TABLE teams ALTER COLUMN idp_group_id SET NOT NULL;
