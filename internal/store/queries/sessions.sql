-- name: CreateSession :one
INSERT INTO sessions (id, user_oid, email, display_name, group_ids, is_app_admin, id_token_expires, expires_at, source)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetSessionByID :one
SELECT * FROM sessions WHERE id = $1 AND expires_at > now();

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at < now();
