-- 0012_teams_service_accounts.down.sql
-- Reverts 0012_teams_service_accounts.up.sql.
ALTER TABLE audit_log DROP COLUMN on_behalf_of;
DROP TABLE service_accounts;
ALTER TABLE pipelines DROP COLUMN owner_team_id;
DROP TABLE teams;
