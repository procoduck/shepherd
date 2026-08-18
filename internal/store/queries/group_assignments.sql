-- name: CreateGroupAssignment :one
INSERT INTO group_assignments (collector_id, group_id, group_display_name)
VALUES ($1, $2, $3)
ON CONFLICT (collector_id, group_id) DO UPDATE SET group_display_name = EXCLUDED.group_display_name
RETURNING *;

-- name: DeleteGroupAssignment :exec
DELETE FROM group_assignments WHERE collector_id = $1 AND group_id = $2;

-- name: ListGroupAssignmentsByCollector :many
SELECT * FROM group_assignments WHERE collector_id = $1 ORDER BY group_display_name;

-- name: ListCollectorIDsByGroupMembership :many
SELECT DISTINCT collector_id
FROM group_assignments
WHERE group_id = ANY($1::text[]);
