-- 0017_team_members.up.sql
-- Teams gain explicit members, so a team is usable without an identity
-- provider. This EXTENDS 0012's model rather than replacing it, the same way
-- 0015 extended identity: a team may be backed by an IdP group, by a list of
-- local users, or by both, and membership is the union of the two. Nothing
-- about the group path changes.
--
-- idp_group_id therefore stops being mandatory: a team whose members are all
-- local has no group to name. NULL means "no group backs this team" and is
-- distinct from every other NULL under the existing UNIQUE (org_id,
-- idp_group_id), so any number of group-less teams can coexist in one org --
-- which is exactly the shape an org with no IdP has.
ALTER TABLE teams ALTER COLUMN idp_group_id DROP NOT NULL;

-- The column was NOT NULL, so '' was the only way to express "no group".
-- Normalise those to NULL now that the honest value exists, or they would
-- read as a real group ID that no session can ever match.
UPDATE teams SET idp_group_id = NULL WHERE idp_group_id = '';

CREATE TABLE team_members (
    team_id    uuid        NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, user_id)
);

-- CASCADE on both sides is right here and differs from 0012's ON DELETE SET
-- NULL for pipelines.owner_team_id, because the row means something
-- different: a membership has no value once either end is gone, whereas a
-- pipeline outlives the team that owned it and merely becomes unowned.

-- Answers "which teams is this user in" during authorization, which is the
-- hot direction; the primary key already covers the other one.
CREATE INDEX idx_team_members_user ON team_members (user_id);
