-- Step 7a (#361): one role vocabulary, viewer/editor/admin. The org's IdP
-- group for the viewer role was the only place still spelled "reader".
ALTER TABLE orgs RENAME COLUMN reader_group_id TO viewer_group_id;
