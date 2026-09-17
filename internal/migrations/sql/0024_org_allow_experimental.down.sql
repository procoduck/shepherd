-- 0024_org_allow_experimental.down.sql
ALTER TABLE orgs DROP COLUMN allow_experimental_components;
