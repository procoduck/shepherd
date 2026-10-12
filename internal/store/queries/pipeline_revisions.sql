-- name: CreatePipelineRevision :one
INSERT INTO pipeline_revisions (pipeline_id, revision, contents, matchers, enabled, changed_by, change_note, wizard_state, wizard_render_sha256)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: ListPipelineRevisions :many
SELECT * FROM pipeline_revisions WHERE pipeline_id = $1 ORDER BY revision DESC;

-- name: GetMaxPipelineRevision :one
SELECT COALESCE(MAX(revision), 0)::int AS max_rev FROM pipeline_revisions WHERE pipeline_id = $1;

-- name: GetPipelineRevision :one
SELECT * FROM pipeline_revisions WHERE pipeline_id = $1 AND revision = $2;

-- name: GetLatestWizardPipelineRevision :one
-- The pipeline's most recent revision a wizard wrote: CommitWizard's
-- "created" and applyWizardRerenders' "re-rendered: …" (mgmtapi's
-- isWizardChangeNote, which RestoreRevision reserves for wizard writes).
-- The pre-#261 hand-edit fallback (review of #277) accepts a tenant-zeroed
-- render only while that revision is still the pipeline's text.
SELECT * FROM pipeline_revisions
WHERE pipeline_id = $1
  AND (btrim(change_note) = 'created' OR btrim(change_note) LIKE 're-rendered:%')
ORDER BY revision DESC
LIMIT 1;
