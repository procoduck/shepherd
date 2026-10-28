-- 0052_session_id_hash.up.sql
-- sessions.id now holds the SHA-256 hex digest of the shepherd_session cookie
-- instead of the cookie value itself. Rows written before this migration are
-- keyed by the raw value, which no server will look up any more, so they are
-- removed rather than rehashed: everyone signs in once after the upgrade.
DELETE FROM sessions;
