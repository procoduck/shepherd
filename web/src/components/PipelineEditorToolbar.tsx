import { PlayCircle, Save, Wand2 } from 'lucide-react';
import { EditorValidationStatus } from '@/components/EditorValidationStatus';
import { WrapLinesToggle } from '@/components/WrapLinesToggle';

const SECONDARY =
  'flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded border border-border px-3 py-1 text-xs font-medium text-muted hover:text-zinc-100 disabled:opacity-50';

/**
 * The text editor's toolbar: the validation verdict, Wrap lines, and — for
 * whoever may write — Validate, Format and Save.
 *
 * It wraps rather than overflows (2026-10-12 walkthrough): at a 1024px window
 * it ran past the edge, Save's right edge at 1046px with nothing to scroll to
 * it, and "No problems" and "Wrap lines" broke onto two lines. Every label
 * stays on one line; when the row is too narrow the actions move onto a
 * second row instead. The pane holding it needs `min-w-0` to shrink at all.
 */
export function PipelineEditorToolbar({
  showStatus,
  validating,
  problems,
  warnings,
  alloyValidateSkipped,
  wrapLines,
  onWrapLines,
  readOnly,
  canRun,
  onValidate,
  formatting,
  onFormat,
  saveDisabled,
  onSave,
}: {
  showStatus: boolean;
  validating: boolean;
  problems: number;
  warnings: number;
  alloyValidateSkipped: boolean;
  wrapLines: boolean;
  onWrapLines: (wrap: boolean) => void;
  readOnly: boolean;
  /** False while the buffer is empty: nothing to validate or format. */
  canRun: boolean;
  onValidate: () => void;
  formatting: boolean;
  onFormat: () => void;
  saveDisabled: boolean;
  onSave: () => void;
}) {
  return (
    <div
      className='min-h-11 border-b border-border px-3 py-1.5 flex flex-wrap items-center justify-between gap-x-3 gap-y-1.5 shrink-0'
      data-testid='editor-toolbar'
    >
      <EditorValidationStatus
        show={showStatus}
        validating={validating}
        problems={problems}
        warnings={warnings}
        alloyValidateSkipped={alloyValidateSkipped}
      />
      <div className='ml-auto flex flex-wrap items-center justify-end gap-2'>
        <WrapLinesToggle wrap={wrapLines} onChange={onWrapLines} />
        {!readOnly && (
          <>
            <button
              type='button'
              onClick={onValidate}
              disabled={validating || !canRun}
              className={SECONDARY}
              data-testid='validate-btn'
            >
              <PlayCircle size={13} /> Validate
            </button>
            <button
              type='button'
              onClick={onFormat}
              disabled={formatting || !canRun}
              className={SECONDARY}
              data-testid='format-btn'
            >
              <Wand2 size={13} /> Format
            </button>
            <button
              type='button'
              onClick={onSave}
              disabled={saveDisabled}
              className='flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded bg-indigo-600 px-3 py-1 text-xs font-medium text-white hover:bg-indigo-500 disabled:opacity-50'
            >
              <Save size={13} /> Save
            </button>
          </>
        )}
      </div>
    </div>
  );
}
