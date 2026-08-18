-- 0005_visual_source.down.sql
-- Reverts the 'visual' source addition.
--
-- WARNING: This rollback WILL FAIL if any pipelines with source='visual' exist.
-- Before running this migration down, ensure all visual pipelines are removed or
-- converted:
--   DELETE FROM pipelines WHERE source='visual';
--   -- OR --
--   UPDATE pipelines SET source='ui' WHERE source='visual';
--
-- This is a deliberate release constraint: once visual pipelines exist in
-- production, downgrading this migration requires a manual data cleanup step.

ALTER TABLE pipelines
  DROP CONSTRAINT IF EXISTS pipelines_source_check;

ALTER TABLE pipelines
  ADD CONSTRAINT pipelines_source_check
    CHECK (source IN ('ui', 'wizard', 'git'));
