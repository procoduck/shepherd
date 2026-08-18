-- 0005_visual_source.up.sql
-- Adds 'visual' as a valid pipeline source for VB-1 (visual pipeline builder).
-- The pipelines table has a CHECK constraint on source; ALTER TABLE + DROP CONSTRAINT
-- + ADD CONSTRAINT is the portable approach for PostgreSQL 16.

ALTER TABLE pipelines
  DROP CONSTRAINT IF EXISTS pipelines_source_check;

ALTER TABLE pipelines
  ADD CONSTRAINT pipelines_source_check
    CHECK (source IN ('ui', 'wizard', 'git', 'visual'));
