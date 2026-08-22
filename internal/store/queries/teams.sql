-- name: CreateTeam :one
INSERT INTO teams (org_id, name, idp_group_id)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetTeamByID :one
SELECT * FROM teams WHERE id = $1;

-- name: ListTeamsByOrg :many
SELECT * FROM teams WHERE org_id = $1 ORDER BY name;

-- name: DeleteTeam :exec
DELETE FROM teams WHERE id = $1;

-- name: ListTeamsByOrgAndGroups :many
-- Used by internal/auth.authorizeOrgAccess's team-membership fallback: is
-- this session a member (via IdP group) of any team in this org? A
-- non-empty result grants the same reader-equivalent baseline
-- group_assignments already grants for collector access — see that
-- function's doc comment.
SELECT * FROM teams WHERE org_id = $1 AND idp_group_id = ANY($2::text[]);
