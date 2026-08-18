-- name: CreateRepoLink :one
INSERT INTO repo_links (org_id, collector_id, credential_id, project, repository, branch, path, poll_interval_seconds)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetRepoLinkByID :one
SELECT * FROM repo_links WHERE id = $1;

-- name: ListRepoLinksByOrg :many
SELECT * FROM repo_links WHERE org_id = $1 ORDER BY created_at;

-- name: UpdateRepoLink :one
UPDATE repo_links
SET credential_id         = $2,
    project               = $3,
    repository            = $4,
    branch                = $5,
    path                  = $6,
    poll_interval_seconds = $7,
    updated_at            = now()
WHERE id = $1
RETURNING *;

-- name: UpdateRepoLinkSync :exec
UPDATE repo_links
SET last_synced_at = now(),
    last_commit    = $2,
    sync_status    = $3,
    sync_error     = $4,
    updated_at     = now()
WHERE id = $1;

-- name: DeleteRepoLink :exec
DELETE FROM repo_links WHERE id = $1;

-- name: ListDueRepoLinks :many
SELECT * FROM repo_links
WHERE last_synced_at IS NULL
   OR last_synced_at + (poll_interval_seconds * interval '1 second') < now()
ORDER BY last_synced_at ASC NULLS FIRST;
