-- name: CountUsers :one
-- Drives the first-run bootstrap: an empty table means nobody can sign in yet.
SELECT COUNT(*)::bigint AS total FROM users;

-- name: GetUserByLogin :one
-- Case-insensitive, matching idx_users_login_lower. Disabled users are returned
-- so the caller can distinguish "no such account" from "account disabled" in
-- logs while still refusing the login.
SELECT * FROM users WHERE lower(login) = lower($1);

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: ListUsers :many
SELECT * FROM users ORDER BY lower(login);

-- name: CreateUser :one
INSERT INTO users (login, email, display_name, password_hash, is_app_admin, must_change_password)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: UpdateUser :one
-- Profile and flags only. Passwords go through SetUserPassword so the hash is
-- never carried alongside fields a general-purpose update might touch.
UPDATE users
SET email = $2, display_name = $3, is_app_admin = $4, disabled = $5, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetUserPassword :exec
UPDATE users
SET password_hash = $2, must_change_password = $3, updated_at = now()
WHERE id = $1;

-- name: TouchUserLogin :exec
UPDATE users SET last_login_at = now(), updated_at = now() WHERE id = $1;

-- name: DeleteUser :exec
-- sessions.user_id and org_members cascade, so this ends the user's sessions
-- and removes their org roles in the same statement.
DELETE FROM users WHERE id = $1;

-- name: ListOrgMembers :many
SELECT om.org_id, om.role, om.created_at, om.updated_at,
       u.id AS user_id, u.login, u.email, u.display_name, u.is_app_admin, u.disabled
FROM org_members om
JOIN users u ON u.id = om.user_id
WHERE om.org_id = $1
ORDER BY lower(u.login);

-- name: ListOrgMembershipsForUser :many
SELECT om.org_id, om.role, o.name AS org_name, o.display_name AS org_display_name
FROM org_members om
JOIN orgs o ON o.id = om.org_id
WHERE om.user_id = $1
ORDER BY lower(o.name);

-- name: GetOrgMemberRole :one
-- The authorization lookup for a local session.
SELECT role FROM org_members WHERE org_id = $1 AND user_id = $2;

-- name: UpsertOrgMember :one
INSERT INTO org_members (org_id, user_id, role)
VALUES ($1, $2, $3)
ON CONFLICT (org_id, user_id) DO UPDATE SET role = EXCLUDED.role, updated_at = now()
RETURNING *;

-- name: DeleteOrgMember :exec
DELETE FROM org_members WHERE org_id = $1 AND user_id = $2;
