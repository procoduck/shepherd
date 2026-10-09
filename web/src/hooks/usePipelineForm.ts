import { useCallback, useEffect, useRef, useState } from 'react';
import { clients } from '@/api/transport';
import type { Diagnostic } from '@/gen/shepherd/mgmt/v1/common_pb';

export interface PipelineFormValues {
  name: string;
  contents: string;
  matchers: string[];
}

interface ServerPipeline extends PipelineFormValues {
  id: string;
  /** The server's current revision for this copy (Pipeline.revision); 0 when unknown. */
  revision?: number;
}

function sameForm(a: PipelineFormValues, b: PipelineFormValues): boolean {
  return (
    a.name === b.name &&
    a.contents === b.contents &&
    a.matchers.length === b.matchers.length &&
    a.matchers.every((m, i) => m === b.matchers[i])
  );
}

// The text editor's form (name, contents, matchers) and the server
// validation of its contents.
//
// The form follows the server copy until the user edits it. `seeded` is the
// server snapshot the form was last loaded from; a newer server copy (a
// refetch, a cache update from a save elsewhere) replaces the form only while
// the form still equals that snapshot — nothing typed since. Edits are never
// overwritten: staleTime is 30s and refetchOnWindowFocus defaults to true, so
// a refetch that clobbered the form silently discarded minutes of work on an
// alt-tab.
//
// The snapshot also keeps the revision it was taken at, and a save sends
// that back as expected_revision (F1): a form still holding edits made on an
// older copy — another tab saved, a destination change re-rendered the
// wizard pipeline — is refused by the server instead of silently writing the
// old text over the newer one.
//
// Seeding only once per id (the previous guard) did the opposite harm (H1):
// landing here from the visual builder's or a wizard's save, the first copy
// seen could be the pre-save one the cache already held, and the fresh one
// that followed was ignored — the editor showed the old matchers, and a plain
// Save wrote them back over the change just made.
export function usePipelineForm({
  pipeline,
  orgId,
  canWrite,
}: {
  pipeline: ServerPipeline | undefined;
  orgId: string;
  canWrite: boolean;
}) {
  const [name, setName] = useState('');
  const [contents, setContents] = useState('');
  const [matchers, setMatchers] = useState<string[]>([]);
  const [diagnostics, setDiagnostics] = useState<Diagnostic[]>([]);
  // The buffer the diagnostics were reported for. Typing on from there keeps
  // them on screen (they still point near the problem) but they no longer
  // gate Save: only the answer for the current text may.
  const [diagnosticsFor, setDiagnosticsFor] = useState('');
  // Whether the last wholesale replacement was the user's own action (Format,
  // restore — undoable) or a background sync of the server copy (not).
  const [replaceIsEdit, setReplaceIsEdit] = useState(false);
  // Stages the server could not run (#209) — 2 when it has no Alloy binary.
  // A clean result with a skipped stage is a syntax check, not "No problems".
  const [skippedStages, setSkippedStages] = useState<number[]>([]);
  const [validating, setValidating] = useState(false);

  const seeded = useRef<ServerPipeline | null>(null);
  const formRef = useRef<PipelineFormValues>({ name, contents, matchers });
  formRef.current = { name, contents, matchers };
  const validateSeq = useRef(0);

  // Replaces the buffer wholesale (seed, refetch, restore, Format). Problems
  // reported for the old text no longer apply — their positions may not even
  // exist in the new one (M5) — so they are dropped, and an answer still in
  // flight for the old text is discarded; the new text is validated afresh.
  const replaceContents = useCallback((next: string, asEdit: boolean) => {
    if (next === formRef.current.contents) return;
    validateSeq.current++;
    setValidating(false);
    setDiagnostics([]);
    setSkippedStages([]);
    setReplaceIsEdit(asEdit);
    setContents(next);
  }, []);

  // Loads a server copy into the form; it becomes the snapshot later
  // refetches compare against. `asEdit` is true for a restore (the user's
  // action), false for a seed or re-sync.
  const loadForm = useCallback(
    (p: ServerPipeline, asEdit = false) => {
      seeded.current = {
        id: p.id,
        name: p.name,
        contents: p.contents,
        matchers: [...p.matchers],
        revision: p.revision ?? 0,
      };
      setName(p.name);
      replaceContents(p.contents, asEdit);
      setMatchers([...p.matchers]);
    },
    [replaceContents],
  );

  // After a save: what was submitted is now the server copy, at the revision
  // the save's response reports, so the form is pristine against it — the
  // refetches that follow may refresh it (a server-side normalisation shows
  // up) while anything typed meanwhile stays.
  const markSaved = useCallback((id: string, submitted: PipelineFormValues, revision: number) => {
    seeded.current = { id, ...submitted, matchers: [...submitted.matchers], revision };
  }, []);

  // The revision the form's base copy was loaded at — what a save sends as
  // expected_revision. 0 when unknown (a new pipeline, or a copy the server
  // reported none for), which the caller treats as "send none".
  const loadedRevision = useCallback(() => seeded.current?.revision ?? 0, []);

  useEffect(() => {
    if (!pipeline) return;
    const snap = seeded.current;
    if (snap && snap.id === pipeline.id) {
      // Unchanged on the server: the form stays, but a newer revision of the
      // same text (an enable/disable round-trip, a re-render that wrote the
      // same output) is the base it now stands on.
      if (sameForm(pipeline, snap)) {
        if ((pipeline.revision ?? 0) > (snap.revision ?? 0)) snap.revision = pipeline.revision;
        return;
      }
      // Edited here since: leave the form alone — and its base revision with
      // it, so saving those edits is refused rather than overwriting.
      if (!sameForm(formRef.current, snap)) return;
    }
    loadForm(pipeline);
  }, [pipeline, loadForm]);

  // Debounced validation. Requests overlap (debounce, the Validate button,
  // Format), and only the newest one's answer may land: a slow answer for an
  // older buffer used to overwrite a newer "valid" — the editor then showed
  // errors at lines the text no longer had, kept Save disabled and the gutter
  // red, and nothing re-validated (#201).
  const validate = useCallback(
    async (c: string) => {
      if (!orgId || !c.trim()) return;
      const seq = ++validateSeq.current;
      setValidating(true);
      try {
        const result = await clients.pipeline.validatePipeline({
          orgId,
          name: name || 'preview',
          contents: c,
        });
        // Only the newest request's answer, and only while the buffer is
        // still the text it was asked about: an answer for text typed past
        // since (before the debounce sent anything newer) would show
        // problems for text that no longer exists. The debounce re-validates
        // the current text.
        if (seq === validateSeq.current && c === formRef.current.contents) {
          setDiagnostics(result.diagnostics ?? []);
          setDiagnosticsFor(c);
          setSkippedStages(result.skippedStages ?? []);
        }
      } catch (_) {
        /* ignore */
      } finally {
        if (seq === validateSeq.current) setValidating(false);
      }
    },
    [name, orgId],
  );

  useEffect(() => {
    // Only someone who can save this pipeline validates it as they type: a
    // reader's buffer is never sent anywhere, so its problems are not worth a
    // request per keystroke.
    if (!canWrite) return;
    const t = setTimeout(() => validate(contents), 800);
    return () => clearTimeout(t);
  }, [contents, validate, canWrite]);

  return {
    name,
    setName,
    contents,
    setContents,
    matchers,
    setMatchers,
    diagnostics,
    // Problems that block Save: those reported for the text as it is now.
    blockingErrors: diagnostics.length > 0 && diagnosticsFor === contents,
    replaceIsEdit,
    skippedStages,
    validating,
    validate,
    replaceContents,
    loadForm,
    markSaved,
    loadedRevision,
  };
}
