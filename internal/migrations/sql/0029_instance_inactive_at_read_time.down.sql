-- Irreversible by design: the up migration only replaced the old sweeper's
-- 'inactive' sentinel with NULL, and the previous code reads NULL the same way
-- it read a reconnected 'inactive' (cleared to NULL). A down migration has
-- nothing to restore; the old sweeper re-marks stale rows on its next tick.
SELECT 1;
