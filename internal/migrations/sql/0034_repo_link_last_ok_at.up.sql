-- When a repo link last finished a sync with sync_status 'ok' (B-GITSYNC-PRUNE).
-- gitsync's mass-delete guard measures a sync's deletes against the link's
-- pipelines that existed at that moment: a guarded sync still applies its
-- creates, and counting those would let the next poll compare the same
-- deletes against a larger set and release the guard on its own.
--
-- Stamped by a trigger rather than by the sync's own UPDATE, so a write from
-- any binary — including an older one during a rolling update, whose
-- UpdateRepoLinkSync does not name this column — keeps it current. now() is
-- the transaction's start time, the same instant as the created_at of every
-- pipeline that sync's transaction inserted, so those count as existing.
ALTER TABLE repo_links ADD COLUMN last_ok_at TIMESTAMPTZ;

-- Every pipeline that exists at the upgrade is part of each link's baseline:
-- a backlog of files removed before the upgrade is measured against what the
-- link held then, whatever the link's status.
UPDATE repo_links SET last_ok_at = now();

CREATE FUNCTION repo_links_stamp_last_ok() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.sync_status = 'ok' THEN
        NEW.last_ok_at := now();
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER repo_links_stamp_last_ok
    BEFORE INSERT OR UPDATE ON repo_links
    FOR EACH ROW EXECUTE FUNCTION repo_links_stamp_last_ok();
