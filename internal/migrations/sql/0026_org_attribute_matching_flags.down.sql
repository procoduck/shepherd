-- 0026_org_attribute_matching_flags.down.sql
ALTER TABLE orgs
    DROP COLUMN allow_label_matching,
    DROP COLUMN allow_local_attribute_matching;
