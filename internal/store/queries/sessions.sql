-- name: CreateSession :one
INSERT INTO sessions (id, user_oid, email, display_name, group_ids, is_app_admin, id_token_expires, expires_at, source)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetSessionByID :one
SELECT * FROM sessions WHERE id = $1 AND expires_at > now();

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at < now();

-- name: UpdateSession :exec
UPDATE sessions
SET group_ids        = $2,
    is_app_admin     = $3,
    id_token_expires = $4,
    expires_at       = $5,
    updated_at       = now()
WHERE id = $1;
