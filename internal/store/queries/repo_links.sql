-- name: CreateRepoLink :one
INSERT INTO repo_links (org_id, collector_id, credential_id, repo_url, branch, path, poll_interval_seconds)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetRepoLinkByID :one
SELECT * FROM repo_links WHERE id = $1;

-- name: ListRepoLinksByOrg :many
SELECT * FROM repo_links WHERE org_id = $1 ORDER BY created_at;

-- name: UpdateRepoLinkSync :exec
UPDATE repo_links
SET last_synced_at = now(),
    last_commit    = $2,
    sync_status    = $3,
    sync_error     = $4,
    updated_at     = now()
WHERE id = $1;

-- name: GetRepoLinkForUpdate :one
-- Row-locks the link for DeleteRepoLink's transaction: a concurrent gitsync
-- insert of a pipeline referencing it (which takes FOR KEY SHARE on this
-- row) waits for the delete to commit and then fails its foreign key,
-- instead of slipping a pipeline in between the pipeline delete and the
-- link delete.
SELECT * FROM repo_links WHERE id = $1 FOR UPDATE;

-- name: DeleteRepoLink :exec
DELETE FROM repo_links WHERE id = $1;

-- name: ListDueRepoLinks :many
SELECT * FROM repo_links
WHERE last_synced_at IS NULL
   OR last_synced_at + (poll_interval_seconds * interval '1 second') < now()
ORDER BY last_synced_at ASC NULLS FIRST;
