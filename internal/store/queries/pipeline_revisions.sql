-- name: CreatePipelineRevision :one
INSERT INTO pipeline_revisions (pipeline_id, revision, contents, matchers, enabled, changed_by, change_note)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListPipelineRevisions :many
SELECT * FROM pipeline_revisions WHERE pipeline_id = $1 ORDER BY revision DESC;

-- name: GetMaxPipelineRevision :one
SELECT COALESCE(MAX(revision), 0)::int AS max_rev FROM pipeline_revisions WHERE pipeline_id = $1;
