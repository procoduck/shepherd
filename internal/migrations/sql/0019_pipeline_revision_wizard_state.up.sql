-- 0019_pipeline_revision_wizard_state.up.sql
-- F-REVISIONS (docs/plans/2026-09-11-f-revisions.md): give each
-- pipeline_revisions row its own copy of the visual-builder graph document,
-- alongside contents/matchers/enabled which the table already carries. The
-- column is nullable with no default — NULL means either "this revision
-- predates 0019" or "the pipeline was never source=visual/wizard and has no
-- graph to store" — both are legitimate, permanent states, not a backfill
-- gap to close.
--
-- Nothing reads this column until the F-REVISIONS backend package lands
-- GetRevision (returns it on the full, single-revision response) and
-- RestoreRevision (restores it onto pipelines.wizard_state via the same
-- COALESCE-on-absent rule UpdatePipeline already uses, so a text-only
-- restore of an old, pre-0019 revision leaves the stored graph untouched
-- rather than clearing it). createRevision (internal/mgmtapi/rpc_pipeline.go)
-- copies pipelines.wizard_state verbatim into this column at write time —
-- so every revision from here forward carries the graph as it stood when
-- that revision was made, not just the pipeline's current graph.
ALTER TABLE pipeline_revisions
    ADD COLUMN wizard_state jsonb;
