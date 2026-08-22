-- name: CreatePipeline :one
INSERT INTO pipelines (org_id, name, contents, matchers, enabled, source, wizard_kind, wizard_state, created_by, updated_by, repo_link_id, git_path, owner_team_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: GetPipelineByID :one
SELECT * FROM pipelines WHERE id = $1;

-- name: GetPipelineByOrgAndName :one
SELECT * FROM pipelines WHERE org_id = $1 AND name = $2;

-- name: ListPipelinesByOrg :many
SELECT * FROM pipelines WHERE org_id = $1 ORDER BY name;

-- name: SetPipelineOwnerTeam :one
-- Reassigns (or, with a NULL owner_team_id, clears — demotes to
-- unowned/admin-only) a pipeline's owning team. Deliberately its own query
-- rather than folded into UpdatePipeline: content edits and ownership
-- transfer are different operations with different authorization
-- requirements (see internal/mgmtapi's PipelineService — a team member may
-- edit their team's pipeline content, but reassigning OWNERSHIP away from
-- their team is an org-admin action), and merging them would let a content
-- edit request accidentally carry an ownership change along for the ride.
UPDATE pipelines
SET owner_team_id = $2,
    updated_at     = now()
WHERE id = $1
RETURNING *;

-- name: UpdatePipeline :one
-- wizard_state is COALESCE'd against the existing column value: the caller
-- passes NULL (an absent/unset field in the request) to preserve whatever
-- graph/wizard-state is already stored, or a non-NULL JSON payload (even
-- "{}") to replace it. This lets a text-only edit of a visual pipeline's
-- contents leave its wizard_state graph intact instead of silently
-- discarding it. wizard_kind is intentionally left out of the SET list: it
-- is fixed at creation by the wizard registry (CommitWizard) and there is no
-- request field that legitimately changes it on update.
UPDATE pipelines
SET name         = $2,
    contents     = $3,
    matchers     = $4,
    wizard_state = COALESCE($5, wizard_state),
    updated_by   = $6,
    updated_at   = now()
WHERE id = $1
RETURNING *;

-- name: SetPipelineEnabled :one
UPDATE pipelines
SET enabled    = $2,
    updated_by = $3,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeletePipeline :exec
DELETE FROM pipelines WHERE id = $1;

-- name: ListEnabledPipelinesByOrg :many
SELECT * FROM pipelines WHERE org_id = $1 AND enabled = true ORDER BY name;

-- ListEnabledPipelinesForMerge returns the org's enabled pipelines together with the
-- collector each git-sourced pipeline targets. The merge engine matches source='git'
-- pipelines by that collector id rather than by matchers, so it must be selected here;
-- without it every git pipeline compares against an empty id and is silently never served.
-- name: ListEnabledPipelinesForMerge :many
SELECT p.*, rl.collector_id AS repo_link_collector_id
FROM pipelines p
LEFT JOIN repo_links rl ON rl.id = p.repo_link_id
WHERE p.org_id = $1 AND p.enabled = true
ORDER BY p.name;
