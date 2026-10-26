-- name: ListOrgImportRepositories :many
-- One org's allowed import.git repositories (0043), by normal form.
SELECT * FROM org_import_repositories WHERE org_id = $1 ORDER BY repository_key;

-- name: AddOrgImportRepository :one
INSERT INTO org_import_repositories (org_id, repository, repository_key, credential_id, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetOrgImportRepository :one
SELECT * FROM org_import_repositories WHERE id = $1;

-- name: DeleteOrgImportRepository :execrows
DELETE FROM org_import_repositories WHERE id = $1 AND org_id = $2;

-- name: GetPinnedImportModule :one
-- A fetched module (0043), if it is cached for the org.
SELECT * FROM pinned_import_modules
WHERE org_id = $1 AND repository_key = $2 AND revision = $3 AND path = $4;

-- name: TouchPinnedImportModule :exec
UPDATE pinned_import_modules SET used_at = now()
WHERE org_id = $1 AND repository_key = $2 AND revision = $3 AND path = $4;

-- name: PutPinnedImportModule :exec
-- A commit cannot change, so a row already there keeps its content.
INSERT INTO pinned_import_modules (org_id, repository_key, revision, path, content)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (org_id, repository_key, revision, path) DO UPDATE SET used_at = now();

-- name: EvictPinnedImportModules :exec
-- Keeps the org's keep most recently used modules, removing the rest.
DELETE FROM pinned_import_modules m
WHERE m.org_id = sqlc.arg(org_id) AND (m.repository_key, m.revision, m.path) IN (
    SELECT c.repository_key, c.revision, c.path FROM pinned_import_modules c
    WHERE c.org_id = sqlc.arg(org_id)
    ORDER BY c.used_at DESC, c.repository_key, c.revision, c.path
    OFFSET sqlc.arg(keep)::int
);
