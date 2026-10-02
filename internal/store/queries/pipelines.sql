-- name: CreatePipeline :one
-- wizard_render_sha256 ($14): sha256 of contents when a wizard wrote them
-- (CommitWizard), NULL from every other creator — see 0030's comment.
INSERT INTO pipelines (org_id, name, contents, matchers, enabled, source, wizard_kind, wizard_state, created_by, updated_by, repo_link_id, git_path, owner_team_id, wizard_render_sha256)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
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
--
-- wizard_render_sha256 ($7) is set, never COALESCE'd: every caller decides
-- it. A wizard write passes the sha256 of the contents it rendered; any
-- other writer passes the stored value only when the contents it writes
-- still hash to it, NULL otherwise (mgmtapi.fingerprintAfterEdit) — so a
-- hand edit can never inherit a wizard's fingerprint (0030).
UPDATE pipelines
SET name                 = $2,
    contents             = $3,
    matchers             = $4,
    wizard_state         = COALESCE($5, wizard_state),
    updated_by           = $6,
    wizard_render_sha256 = $7,
    updated_at           = now()
WHERE id = $1
RETURNING *;

-- name: DetachPipelineFromWizard :one
-- "Detach from wizard" (#262 follow-up): the pipeline becomes an ordinary
-- editor pipeline in place — same id, revisions, owner, matchers, enabled —
-- with no wizard kind, state or render fingerprint, so a destination update
-- no longer finds it (ListWizardPipelinesReferencingDestination keys on
-- wizard_kind) and its text is the operator's from now on.
UPDATE pipelines
SET source               = 'ui',
    wizard_kind          = NULL,
    wizard_state         = NULL,
    wizard_render_sha256 = NULL,
    updated_by           = $2,
    updated_at           = now()
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

-- name: ListWizardPipelinesReferencingDestination :many
-- The wizard pipelines in one org that name a destination (#262). A wizard
-- stores the destination by NAME, under a `<signal>_dest_name` key of its
-- wizard_state object (metrics_dest_name, logs_dest_name — every wizard in
-- internal/wizard), never by id: matching any `*_dest_name` key rather than a
-- fixed list keeps a future wizard's field in scope without a query change.
-- Backs DeleteDestination's in-use check and UpdateDestination's re-render.
-- FOR UPDATE: UpdateDestination re-renders these rows in the same
-- transaction that updates the destination, so a concurrent pipeline edit
-- waits instead of being overwritten by a render of the state it replaced.
-- The CASE keeps jsonb_each from ever seeing a non-object wizard_state.
SELECT * FROM pipelines
WHERE org_id = sqlc.arg(org_id)
AND wizard_kind IS NOT NULL
AND CASE WHEN jsonb_typeof(wizard_state) = 'object' THEN EXISTS (
    SELECT 1 FROM jsonb_each(wizard_state) AS kv
    WHERE kv.key LIKE '%\_dest\_name'
    AND kv.value = to_jsonb(sqlc.arg(destination_name)::text)
) ELSE false END
ORDER BY name
FOR UPDATE;

-- name: ListWizardPipelinesByOrgForUpdate :many
-- Every wizard pipeline in one org, row-locked for a re-render in the same
-- transaction (`shepherd admin rerender-destinations`, #262).
SELECT * FROM pipelines
WHERE org_id = $1 AND wizard_kind IS NOT NULL
ORDER BY name
FOR UPDATE;

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
