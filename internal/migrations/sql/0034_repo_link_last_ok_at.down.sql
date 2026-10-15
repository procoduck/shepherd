DROP TRIGGER IF EXISTS repo_links_stamp_last_ok ON repo_links;
DROP FUNCTION IF EXISTS repo_links_stamp_last_ok();
ALTER TABLE repo_links DROP COLUMN IF EXISTS last_ok_at;
