-- 0016_org_editor_group.up.sql
-- The editor role on the OIDC path.
--
-- Local users got admin/editor/viewer in 0015 (org_members.role). Without this
-- column an OIDC user could only ever be admin or viewer, so the two sign-in
-- paths would grant different sets of things — a difference that is invisible
-- until someone asks why the same person has different rights depending on how
-- they logged in.
--
-- Nullable, and an org with no editor group behaves exactly as it did before:
-- admins from admin_group_id, viewers from reader_group_id, nobody in between.
-- This is additive to a deployment that never sets it.
ALTER TABLE orgs
    ADD COLUMN editor_group_id text;
