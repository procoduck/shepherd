-- name: CreateSession :one
-- user_id is set only for local sessions (source = 'local'); an OIDC session
-- has no local users row and leaves it NULL. Authorization branches on it —
-- see internal/auth.authorizeOrgAccess.
INSERT INTO sessions (id, user_id, user_oid, email, display_name, group_ids, is_app_admin, id_token_expires, expires_at, source)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetSessionByID :one
SELECT * FROM sessions WHERE id = $1 AND expires_at > now();

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at < now();

-- name: DeleteSessionsForUser :execrows
-- Revocation for a local user: end every session they currently hold.
--
-- Needed because a session row is a SNAPSHOT. is_app_admin is copied into it at
-- login and read from there on every request, and `disabled` is checked only at
-- login -- so demoting or disabling an account changes nothing about the
-- sessions it already has, and the account keeps whatever it held until the
-- cookie expires (session_ttl, 8h by default). Org roles are the exception:
-- those are re-read per request from org_members.
DELETE FROM sessions WHERE user_id = $1;
