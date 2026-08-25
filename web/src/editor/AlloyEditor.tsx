import { autocompletion, closeBrackets } from '@codemirror/autocomplete';
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands';
import { bracketMatching, foldGutter, indentOnInput } from '@codemirror/language';
import {
  type Diagnostic as CmDiagnostic,
  forceLinting,
  linter,
  lintGutter,
} from '@codemirror/lint';
import { highlightSelectionMatches, searchKeymap } from '@codemirror/search';
import { EditorState } from '@codemirror/state';
import { EditorView, highlightActiveLine, keymap, lineNumbers, ViewUpdate } from '@codemirror/view';
import { useEffect, useRef } from 'react';
import type { Diagnostic } from '@/gen/shepherd/mgmt/v1/common_pb';
import { alloyCompletionSource } from './alloyCompletion';
import { alloyLanguage } from './alloyLanguage';

interface AlloyEditorProps {
  value: string;
  onChange?: (value: string) => void;
  readOnly?: boolean;
  diagnostics?: Diagnostic[];
  height?: string;
}

// Zinc dark theme matching spec §13.1
const alloyTheme = EditorView.theme(
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

export function AlloyEditor({
  value,
  onChange,
  readOnly = false,
  diagnostics = [],
  height = '100%',
}: AlloyEditorProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);

  // The linter closes over a REF, not over the prop.
  //
  // It is installed once, in an effect keyed on [readOnly], so a closure over
  // `diagnostics` captured whatever the array was at mount -- usually empty --
  // and kept returning that forever. The squiggles and the lint gutter
  // therefore never showed server validation results at all; only the separate
  // Problems panel did. Dispatching an empty transaction could not fix that:
  // it re-runs the linter, but the linter still sees the stale closure.
  const diagnosticsRef = useRef(diagnostics);
  diagnosticsRef.current = diagnostics;

  // Convert server diagnostics to CodeMirror diagnostics
  const cmLinter = linter((view) => {
    const cmDiags: CmDiagnostic[] = [];
    for (const d of diagnosticsRef.current) {
      const line = view.state.doc.line(Math.max(1, Math.min(d.line, view.state.doc.lines)));
      const from = line.from + Math.max(0, d.col - 1);
      const to = Math.min(from + 1, line.to);
      cmDiags.push({ from, to, severity: 'error', message: d.message });
    }
    return cmDiags;
  });

  useEffect(() => {
    if (!containerRef.current) return;

    const extensions = [
      alloyTheme,
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
      cmLinter,
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
      view.dispatch({
        changes: { from: 0, to: current.length, insert: value },
      });
    }
  }, [value]);

  // Sync diagnostics. forceLinting re-runs the linter immediately; the empty
  // dispatch keeps the view in step for the gutter.
  useEffect(() => {
    const view = viewRef.current;
    if (!view) return;
    view.dispatch({});
    forceLinting(view);
  }, [diagnostics]);

  return <div ref={containerRef} style={{ height }} className='overflow-auto' />;
}
