import { MergeView } from '@codemirror/merge';
import { EditorView, lineNumbers } from '@codemirror/view';
import { useEffect, useRef } from 'react';
import { alloyTheme } from './AlloyEditor';
import { alloyLanguage } from './alloyLanguage';

export interface RevisionDiffProps {
  /** Contents of the old revision — rendered read-only in the left pane. */
  oldText: string;
  /** Contents to compare it against — the pipeline's current text, read-only
   *  in the right pane. */
  newText: string;
  height?: string;
}

/**
 * Read-only side-by-side diff of a past revision against the current
 * pipeline text, built on @codemirror/merge's MergeView (S7/W-2).
 *
 * Both panes share the editor's own theme/language so the diff reads like
 * the editor itself, not a second unrelated widget. Neither pane is
 * editable: this view answers "what changed", restoring is a separate,
 * explicit action in PipelineEditorPage.
 *
 * This module is the only one that imports @codemirror/merge. It is
 * re-exported from AlloyEditor.tsx purely so it shares that module's lazy
 * chunk (see LazyAlloyEditor.tsx) — pages must import it from
 * '@/editor/LazyAlloyEditor', never straight from here, or the merge
 * package ends up in the entry chunk (W-7's chunk-boundary proof).
 */
export function RevisionDiff({ oldText, newText, height = '100%' }: RevisionDiffProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<MergeView | null>(null);

  useEffect(() => {
    if (!containerRef.current) return;

    const sharedExtensions = [
      alloyTheme,
      alloyLanguage(),
      lineNumbers(),
      EditorView.editable.of(false),
    ];

    const view = new MergeView({
      a: { doc: oldText, extensions: sharedExtensions },
      b: { doc: newText, extensions: sharedExtensions },
      parent: containerRef.current,
      highlightChanges: true,
      collapseUnchanged: { margin: 3 },
    });
    viewRef.current = view;

    return () => {
      view.destroy();
      viewRef.current = null;
    };
    // oldText/newText are re-created whole on every mount of this component
    // (PipelineEditorPage swaps it in/out via selectedRevision rather than
    // updating props in place), so a fresh MergeView per mount is correct —
    // no separate "sync value" effect like AlloyEditor's is needed here.
  }, [oldText, newText]);

  return (
    <div
      ref={containerRef}
      style={{ height }}
      className='overflow-auto'
      data-testid='revision-diff'
    />
  );
}
