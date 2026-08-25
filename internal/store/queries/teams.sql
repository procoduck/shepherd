-- name: CreateTeam :one
-- idp_group_id is nullable since 0017: a team whose members are all local
-- users has no group backing it. sqlc.narg keeps '' out of the column, where
-- it would read as a real group ID that no session can match.
INSERT INTO teams (org_id, name, idp_group_id)
VALUES ($1, $2, sqlc.narg('idp_group_id'))
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

-- name: ListTeamMembers :many
-- The explicit (local user) half of a team's membership. The IdP-group half
-- has no rows anywhere -- it is resolved from the session's groups claim at
-- authorization time -- so this returns only what is actually stored.
SELECT u.id, u.login, u.email, u.display_name, u.disabled, tm.created_at AS added_at
FROM team_members tm
JOIN users u ON u.id = tm.user_id
WHERE tm.team_id = $1
ORDER BY lower(u.login);

-- name: AddTeamMember :exec
INSERT INTO team_members (team_id, user_id)
VALUES ($1, $2)
ON CONFLICT (team_id, user_id) DO NOTHING;

-- name: RemoveTeamMember :execrows
DELETE FROM team_members WHERE team_id = $1 AND user_id = $2;

-- name: IsTeamMember :one
-- Used by internal/auth.AuthorizeOwnership for a local session, which has no
-- groups claim to match against teams.idp_group_id.
SELECT EXISTS (
    SELECT 1 FROM team_members WHERE team_id = $1 AND user_id = $2
);

-- name: CountTeamMembersByTeam :many
-- Member counts for the teams list, so the page can show membership source
-- per team without a query per row.
SELECT team_id, count(*)::bigint AS member_count
FROM team_members
WHERE team_id = ANY($1::uuid[])
GROUP BY team_id;
