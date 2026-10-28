-- 0053_group_assignment_org.down.sql
-- Reverts 0053_group_assignment_org.up.sql by dropping the org binding. The
-- grants 0053 deleted are NOT restored (their audit_log entries, actor
-- 'migration:0053', remain the record of them); re-create any that are still
-- wanted from the collector's Access tab.
DROP INDEX IF EXISTS idx_group_assignments_org_group;
ALTER TABLE group_assignments DROP COLUMN org_id;
