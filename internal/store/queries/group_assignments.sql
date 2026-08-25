-- name: CreateGroupAssignment :one
INSERT INTO group_assignments (collector_id, group_id, group_display_name)
VALUES ($1, $2, $3)
ON CONFLICT (collector_id, group_id) DO UPDATE SET group_display_name = EXCLUDED.group_display_name
RETURNING *;

-- name: DeleteGroupAssignment :exec
DELETE FROM group_assignments WHERE collector_id = $1 AND group_id = $2;

-- name: ListGroupAssignmentsByCollector :many
SELECT * FROM group_assignments WHERE collector_id = $1 ORDER BY group_display_name;

-- name: ListCollectorIDsByGroupMembershipInOrg :many
-- Used by internal/auth.authorizeOrgAccess's collector-assignment fallback.
--
-- The org filter is load-bearing, not decoration. Without it this asks "does
-- this session hold an assignment on ANY collector anywhere", and a viewer with
-- one assignment in one org clears the reader floor for EVERY org -- silently,
-- because GetMe computes its org list from group matches and never shows the
-- extra orgs. The sibling ListTeamsByOrgAndGroups has always been org-scoped;
-- this one was not.
SELECT DISTINCT ga.collector_id
FROM group_assignments ga
JOIN collectors c ON c.id = ga.collector_id
JOIN clusters cl ON cl.id = c.cluster_id
WHERE ga.group_id = ANY($1::text[])
  AND cl.org_id = $2;
