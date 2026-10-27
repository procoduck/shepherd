import { AlertTriangle, CheckCircle2, XCircle } from 'lucide-react';

/**
 * The pipeline editor toolbar's validation verdict. Readers get no indicator
 * at all (`show` false): their content is never validated, so "No problems"
 * would be a claim nothing checked.
 *
 * Warnings are counted beside the verdict: "No problems" next to a warning in
 * the problems panel read as a clean bill (0.21 walkthrough). They never make
 * the verdict an error — a warning does not block Save.
 */
export function EditorValidationStatus({
  show,
  validating,
  problems,
  warnings = 0,
  alloyValidateSkipped,
}: {
  show: boolean;
  validating: boolean;
  problems: number;
  warnings?: number;
  alloyValidateSkipped: boolean;
}) {
  const warningCount =
    show && !validating && warnings > 0 ? (
      <span
        className='flex items-center gap-1 whitespace-nowrap text-warn'
        data-testid='editor-warning-count'
      >
        {problems === 0 && <AlertTriangle size={14} className='shrink-0' />}
        {warnings} warning{warnings > 1 ? 's' : ''}
      </span>
    ) : null;
  return (
    <div className='flex min-w-0 items-center gap-2 text-xs' aria-live='polite'>
      {show &&
        (validating ? (
          <span className='whitespace-nowrap text-muted'>Validating…</span>
        ) : problems > 0 ? (
          <>
            <span className='flex items-center gap-1 whitespace-nowrap text-red-400'>
              <XCircle size={14} /> {problems} problem
              {problems > 1 ? 's' : ''}
            </span>
            {warningCount}
          </>
        ) : warnings > 0 ? (
          <>
            {warningCount}
            {alloyValidateSkipped && (
              <span
                className='flex items-center gap-1 text-amber-400'
                data-testid='validate-skipped-note'
                title='The server has no validate.alloy_binary configured, so only the Alloy syntax was checked. Component and argument errors will not show here.'
              >
                Syntax checked — alloy validate skipped (no Alloy binary configured)
              </span>
            )}
          </>
        ) : alloyValidateSkipped ? (
          <span
            className='flex items-center gap-1 text-amber-400'
            data-testid='validate-skipped-note'
            title='The server has no validate.alloy_binary configured, so only the Alloy syntax was checked. Component and argument errors will not show here.'
          >
            <AlertTriangle size={14} className='shrink-0' /> Syntax checked — alloy validate skipped
            (no Alloy binary configured)
          </span>
        ) : (
          <span className='flex items-center gap-1 whitespace-nowrap text-emerald-500'>
            <CheckCircle2 size={14} /> No problems
          </span>
        ))}
    </div>
  );
}
