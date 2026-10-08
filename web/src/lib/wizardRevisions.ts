/**
 * Which revisions of a wizard pipeline its wizard wrote (M4, 2026-10-08
 * walkthrough). The server stores no author kind on a revision, but every
 * wizard write leaves a fixed change note (internal/mgmtapi):
 *
 * - `created` — CommitWizard's revision 1 (rpc_wizard.go). The only way a
 *   pipeline gets source `wizard` from the UI is a wizard commit.
 * - `re-rendered: …` — a destination update or `shepherd admin
 *   rerender-destinations` regenerating it (applyWizardRerenders).
 *
 * Everything else — an editor save (`updated`), a restore (`Restored from
 * revision N`) — is not the wizard's, even when its text is. RestoreRevision
 * refuses those two notes from a caller, so they stay the wizard's. The
 * server's own record is the revision's render fingerprint (migration 0031),
 * which the API does not expose; it is what makes restoring one re-attach
 * the pipeline to its wizard.
 */
export function isWizardRevision(changeNote: string, pipelineSource: string): boolean {
  return (
    pipelineSource === 'wizard' &&
    (changeNote === 'created' || changeNote.startsWith('re-rendered:'))
  );
}

/** The newest revision the wizard wrote, or null when there is none. */
export function lastWizardRevision(
  revisions: readonly { revision: number; changeNote: string }[],
  pipelineSource: string,
): number | null {
  let last: number | null = null;
  for (const r of revisions) {
    if (isWizardRevision(r.changeNote, pipelineSource) && (last === null || r.revision > last)) {
      last = r.revision;
    }
  }
  return last;
}
