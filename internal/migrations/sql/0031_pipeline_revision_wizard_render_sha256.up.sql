-- M4 follow-up: the render fingerprint (0030) per revision.
--
-- pipeline_revisions.wizard_render_sha256 is the pipeline's
-- wizard_render_sha256 as it stood when the revision was written: the hex
-- sha256 of contents a wizard wrote, or NULL when the revision's text is not
-- (known to be) a wizard's. Every revision write copies it from the pipeline
-- row it records. RestoreRevision copies it back, so restoring a revision a
-- wizard wrote re-attaches the pipeline to its wizard even when the wizard
-- renders that state differently today (#289 changed two wizards' output) —
-- without it the hand-edit check fell back to a fresh render and refused the
-- restored text.
ALTER TABLE pipeline_revisions ADD COLUMN wizard_render_sha256 text;

-- Backfill, from server-written facts only (never change_note, which
-- RestoreRevision used to accept verbatim from the caller). A revision is
-- stamped only when its contents actually hash to the value stamped.

-- 1. The current revision of a wizard pipeline that still carries a
--    fingerprint matching its text.
UPDATE pipeline_revisions r
SET wizard_render_sha256 = p.wizard_render_sha256
FROM pipelines p
WHERE r.pipeline_id = p.id
  AND p.wizard_render_sha256 IS NOT NULL
  AND r.revision = (SELECT max(revision) FROM pipeline_revisions WHERE pipeline_id = p.id)
  AND encode(sha256(convert_to(r.contents, 'UTF8')), 'hex') = p.wizard_render_sha256;

-- 2. Revision 1 of a wizard pipeline when CommitWizard wrote it: only
--    CommitWizard sets wizard_kind, and since #67 (v0.7.0) it writes
--    revision 1 itself with the note "created". Before that it wrote no
--    revision, so an older wizard pipeline's revision 1 is its first editor
--    save ("updated") — possibly a hand edit — and is left unstamped. The
--    note is not forgeable here: RestoreRevision always writes max+1, and
--    CreatePipeline's "created" is never on a wizard_kind pipeline.
UPDATE pipeline_revisions r
SET wizard_render_sha256 = encode(sha256(convert_to(r.contents, 'UTF8')), 'hex')
FROM pipelines p
WHERE r.pipeline_id = p.id
  AND p.wizard_kind IS NOT NULL
  AND r.revision = 1
  AND r.change_note = 'created'
  AND r.wizard_render_sha256 IS NULL;

-- 3. Revisions a destination re-render or `shepherd admin
--    rerender-destinations` wrote: each has a pipeline.rerender audit row,
--    written in the same transaction, naming the revision number.
UPDATE pipeline_revisions r
SET wizard_render_sha256 = encode(sha256(convert_to(r.contents, 'UTF8')), 'hex')
FROM audit_log a
WHERE a.action = 'pipeline.rerender'
  AND a.resource_type = 'pipeline'
  AND a.resource_id = r.pipeline_id::text
  AND a.detail ->> 'revision' = r.revision::text
  AND r.wizard_render_sha256 IS NULL;
