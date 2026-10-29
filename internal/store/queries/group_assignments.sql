-- name: CreateGroupAssignment :one
-- A grant belongs to the org that made it (0053). The insert happens only
-- while the collector's cluster is in that org, so a cluster that moved
-- between the caller's ownership check and this statement gets no row (the
-- caller sees pgx.ErrNoRows) rather than a grant bound to an org that no
-- longer owns it. FOR SHARE OF cl makes a claim that is already updating the
-- cluster finish first, so the org check sees its result.
INSERT INTO group_assignments (collector_id, group_id, group_display_name, org_id)
SELECT c.id, sqlc.arg('group_id')::text, sqlc.arg('group_display_name')::text, cl.org_id
FROM collectors c
JOIN clusters cl ON cl.id = c.cluster_id
WHERE c.id = sqlc.arg('collector_id') AND cl.org_id = sqlc.arg('org_id')
FOR SHARE OF cl
ON CONFLICT (collector_id, group_id) DO UPDATE
SET group_display_name = EXCLUDED.group_display_name,
    org_id = EXCLUDED.org_id,
    updated_at = now()
RETURNING *;

-- name: DeleteGroupAssignment :exec
DELETE FROM group_assignments WHERE collector_id = $1 AND group_id = $2 AND org_id = $3;

-- name: ListGroupAssignmentsByCollector :many
-- Org-scoped: the Access tab lists only the grants the viewing org made.
SELECT * FROM group_assignments WHERE collector_id = $1 AND org_id = $2 ORDER BY group_display_name;

-- name: ListCollectorIDsByGroupMembershipInOrg :many
-- Used by internal/auth.authorizeOrgAccess's collector-assignment fallback.
--
-- The org filter is load-bearing, not decoration. Without it this asks "does
-- this session hold an assignment on ANY collector anywhere", and a viewer with
-- one assignment in one org clears the viewer floor for EVERY org -- silently,
-- because GetMe computes its org list from group matches and never shows the
-- extra orgs. The sibling ListTeamsByOrgAndGroups has always been org-scoped;
-- this one was not.
--
-- Both halves are required: the grant must have been made by this org
-- (ga.org_id, 0053) AND the collector must still be in it. Checking only the
-- collector's current org let a grant made by a cluster's previous owner keep
-- working after the cluster moved.
SELECT DISTINCT ga.collector_id
FROM group_assignments ga
JOIN collectors c ON c.id = ga.collector_id
JOIN clusters cl ON cl.id = c.cluster_id
WHERE ga.group_id = ANY(sqlc.arg('group_ids')::text[])
  AND ga.org_id = sqlc.arg('org_id')
  AND cl.org_id = sqlc.arg('org_id');
