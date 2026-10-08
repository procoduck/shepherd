import { autocompletion, closeBrackets } from '@codemirror/autocomplete';
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands';
import { bracketMatching, foldGutter, indentOnInput } from '@codemirror/language';
import { type Diagnostic as CmDiagnostic, lintGutter, setDiagnostics } from '@codemirror/lint';
import { highlightSelectionMatches, searchKeymap } from '@codemirror/search';
import { EditorState, type Text, Transaction } from '@codemirror/state';
import { EditorView, highlightActiveLine, keymap, lineNumbers, ViewUpdate } from '@codemirror/view';
import { useEffect, useRef } from 'react';
import type { Diagnostic } from '@/gen/shepherd/mgmt/v1/common_pb';
import { alloyCompletionSource } from './alloyCompletion';
import { alloyLanguage } from './alloyLanguage';

export interface AlloyEditorProps {
  value: string;
  onChange?: (value: string) => void;
  readOnly?: boolean;
  diagnostics?: readonly Diagnostic[];
  height?: string;
  /**
   * Whether replacing the document from `value` is an undoable step. True
   * (the default) for the user's own actions — Format, a restore; false for
   * background syncs (loading or re-syncing the server copy), which undo must
   * not take back to the text they replaced.
   */
  replaceIsEdit?: boolean;
}

// One frozen default, so a caller that omits `diagnostics` does not hand the
// sync effect a new array (and a setDiagnostics dispatch) on every render.
const NO_DIAGNOSTICS: readonly Diagnostic[] = Object.freeze([]);

// Zinc dark theme matching spec §13.1
export const alloyTheme = EditorView.theme(
  {
    '&': {
      backgroundColor: '#09090b',
      color: '#f4f4f5',
      fontFamily: "'JetBrains Mono Variable', monospace",
      fontSize: '13px',
    },
    '.cm-content': { caretColor: '#a1a1aa' },
    '.cm-line': { padding: '0 4px' },
    '.cm-activeLine': { backgroundColor: 'rgba(39,39,42,0.6)' },
    '.cm-gutters': {
      backgroundColor: '#09090b',
      color: '#52525b',
      borderRight: '1px solid #27272a',
    },
    '.cm-activeLineGutter': { backgroundColor: 'rgba(39,39,42,0.6)' },
    '.cm-selectionBackground': { backgroundColor: 'rgba(99,102,241,0.25)' },
    '.cm-focused .cm-selectionBackground': { backgroundColor: 'rgba(99,102,241,0.25)' },
    '.cm-cursor': { borderLeftColor: '#a1a1aa' },
    '.cm-tooltip': { backgroundColor: '#18181b', border: '1px solid #27272a', color: '#f4f4f5' },
  },
  { dark: true },
);

// Server diagnostics → CodeMirror ranges, every one kept inside its line and
// so inside the document. A diagnostic is computed for the text that was
// validated; by the time it is applied the buffer may have been replaced by
// shorter text (Format, a restore, a refetch), and a column past the end of
// the line — or of the whole document — made CodeMirror throw
// `RangeError: Invalid position N in document of length M` (M5). Line 0
// ("unknown") and lines past the end clamp to the first/last line, as before.
export function toCmDiagnostics(doc: Text, diagnostics: readonly Diagnostic[]): CmDiagnostic[] {
  const cmDiags: CmDiagnostic[] = [];
  for (const d of diagnostics) {
    const line = doc.line(Math.max(1, Math.min(d.line, doc.lines)));
    const from = Math.min(line.from + Math.max(0, d.col - 1), line.to);
    const to = Math.min(from + 1, line.to);
    cmDiags.push({ from, to, severity: 'error', message: d.message });
  }
  return cmDiags;
}

export function AlloyEditor({
  value,
  onChange,
  readOnly = false,
  diagnostics = NO_DIAGNOSTICS,
  height = '100%',
  replaceIsEdit = true,
}: AlloyEditorProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);

  // Server diagnostics are pushed into CodeMirror with setDiagnostics — there
  // is no `linter()` source. The source used to read them from a ref and was
  // re-run with forceLinting, but forceLinting does nothing once the linter
  // has no run pending, so diagnostics arriving after the first lint (the
  // server's answer, ~1s after load) drew no squiggle and no gutter marker.
  // A source also re-ran on every document change and applied diagnostics
  // computed for the OLD text to the new one — past the end of a shorter
  // buffer that threw `RangeError: Invalid position` (M5). Pushed diagnostics
  // stay where CodeMirror puts them as the user types (their ranges shift
  // with the text around them; whether the problem still applies is only
  // known when the next validation answers), are clamped into the document
  // whenever they are set, and are dropped on a wholesale replacement (see
  // the value effect below).
  const diagnosticsRef = useRef(diagnostics);
  diagnosticsRef.current = diagnostics;

  useEffect(() => {
    if (!containerRef.current) return;

    const extensions = [
      alloyTheme,
      // Alloy config identifiers (component names, labels, attribute names)
      // are not English prose, so the browser's native spell-checker just
      // underlines every one of them. @codemirror/view's own contentAttrs
      // defaults happen to already turn this off, but that is an
      // implementation detail of the library we should not depend on —
      // declare the editor's own intent explicitly so it stays off even if
      // an upstream default ever changes.
      EditorView.contentAttributes.of({
        spellcheck: 'false',
        autocorrect: 'off',
        autocapitalize: 'off',
      }),
      alloyLanguage(),
      lineNumbers(),
      foldGutter(),
      bracketMatching(),
      closeBrackets(),
      autocompletion({ override: [alloyCompletionSource], activateOnTyping: true }),
      highlightActiveLine(),
      highlightSelectionMatches(),
      indentOnInput(),
      history(),
      lintGutter(),
      keymap.of([...defaultKeymap, ...historyKeymap, ...searchKeymap]),
    ];

    if (readOnly) {
      extensions.push(EditorView.editable.of(false));
    } else if (onChange) {
      extensions.push(
        EditorView.updateListener.of((update: ViewUpdate) => {
          if (update.docChanged) onChange(update.state.doc.toString());
        }),
      );
    }

    const view = new EditorView({
      state: EditorState.create({ doc: value, extensions }),
      parent: containerRef.current,
    });
    viewRef.current = view;
    // A remount (readOnly flipped) must not lose what is already reported.
    if (diagnosticsRef.current.length > 0) {
      view.dispatch(
        setDiagnostics(view.state, toCmDiagnostics(view.state.doc, diagnosticsRef.current)),
      );
    }

    return () => {
      view.destroy();
      viewRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [readOnly]);

  // Sync external value changes without losing cursor position
  useEffect(() => {
    const view = viewRef.current;
    if (!view) return;
    const current = view.state.doc.toString();
    if (current !== value) {
      // A wholesale replacement (Format, restore, a refetch) invalidates the
      // diagnostics shown for the old text: clear them in the same
      // transaction rather than leaving stale squiggles at mapped positions
      // until the next validation answers.
      view.dispatch(setDiagnostics(view.state, []), {
        changes: { from: 0, to: current.length, insert: value },
        annotations: replaceIsEdit ? undefined : Transaction.addToHistory.of(false),
      });
    }
  }, [value]);

  // Sync diagnostics, clamped to the document as it is now.
  useEffect(() => {
    const view = viewRef.current;
    if (!view) return;
    view.dispatch(setDiagnostics(view.state, toCmDiagnostics(view.state.doc, diagnostics)));
  }, [diagnostics]);

  return <div ref={containerRef} style={{ height }} className='overflow-auto' />;
}

// Re-exported so RevisionDiff lives in the same module graph as the rest of
// the CodeMirror setup — LazyAlloyEditor.tsx's `import('./AlloyEditor')`
// chunk boundary is what actually keeps @codemirror/merge out of the entry
// bundle (W-2 / S7); this file never imports @codemirror/merge itself.
export { RevisionDiff } from './RevisionDiff';
