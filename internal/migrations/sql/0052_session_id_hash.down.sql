-- 0052_session_id_hash.down.sql
-- Rows written after 0052 are keyed by the digest of the cookie, which an
-- older server (looking up the raw cookie value) cannot match, so they are
-- removed: everyone signs in again after the rollback.
DELETE FROM sessions;
