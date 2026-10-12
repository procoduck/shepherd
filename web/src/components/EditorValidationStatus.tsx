import { AlertTriangle, CheckCircle2, XCircle } from 'lucide-react';

/**
 * The pipeline editor toolbar's validation verdict. Readers get no indicator
 * at all (`show` false): their content is never validated, so "No problems"
 * would be a claim nothing checked.
 */
export function EditorValidationStatus({
  show,
  validating,
  problems,
  alloyValidateSkipped,
}: {
  show: boolean;
  validating: boolean;
  problems: number;
  alloyValidateSkipped: boolean;
}) {
  return (
    <div className='flex items-center gap-2 text-xs' aria-live='polite'>
      {show &&
        (validating ? (
          <span className='text-muted'>Validating…</span>
        ) : problems > 0 ? (
          <span className='flex items-center gap-1 text-red-400'>
            <XCircle size={14} /> {problems} problem
            {problems > 1 ? 's' : ''}
          </span>
        ) : alloyValidateSkipped ? (
          <span
            className='flex items-center gap-1 text-amber-400'
            data-testid='validate-skipped-note'
            title='The server has no validate.alloy_binary configured, so only the Alloy syntax was checked. Component and argument errors will not show here.'
          >
            <AlertTriangle size={14} /> Syntax checked — alloy validate skipped (no Alloy binary
            configured)
          </span>
        ) : (
          <span className='flex items-center gap-1 text-emerald-500'>
            <CheckCircle2 size={14} /> No problems
          </span>
        ))}
    </div>
  );
}
