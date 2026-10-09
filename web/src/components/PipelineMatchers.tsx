import { useState } from 'react';
import { MatcherSuggestions } from '@/components/MatcherSuggestions';
import { matcherError } from '@/lib/matcher';

/**
 * The text editor's matcher list: one chip per matcher, and an input that
 * adds the typed matcher on Enter — only when the server's matcher parser
 * would accept it (lib/matcher.ts). Otherwise it says why, under the input
 * (#249); the chip used to be added as typed and refused only on save.
 */
export function PipelineMatchers({
  matchers,
  onChange,
  orgId,
  readOnly,
}: {
  matchers: string[];
  onChange: (update: (ms: string[]) => string[]) => void;
  orgId: string;
  readOnly: boolean;
}) {
  const [draft, setDraft] = useState('');
  const [error, setError] = useState<string | null>(null);

  const commit = () => {
    const value = draft.trim();
    if (!value) return;
    const problem = matcherError(value);
    if (problem) {
      setError(problem);
      return;
    }
    onChange((ms) => [...ms, value]);
    setDraft('');
  };

  return (
    <div className='space-y-2'>
      <label className='text-xs font-medium text-muted'>Matchers</label>
      {matchers.map((m, i) => (
        <div key={i} className='flex items-center gap-2' data-testid='pipeline-matcher-chip'>
          <span className='flex-1 font-mono text-xs bg-border px-2 py-1 rounded'>{m}</span>
          <button
            type='button'
            aria-label={`Remove matcher ${m}`}
            onClick={() => onChange((ms) => ms.filter((_, j) => j !== i))}
            disabled={readOnly}
            className='text-muted-2 hover:text-red-400 text-xs'
          >
            ×
          </button>
        </div>
      ))}
      <div className='flex gap-2'>
        <input
          list='pipeline-matcher-suggestions'
          data-testid='pipeline-matcher-input'
          aria-label='New matcher'
          aria-invalid={!!error}
          aria-describedby={error ? 'pipeline-matcher-error' : undefined}
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value);
            setError(null);
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault();
              commit();
            }
          }}
          className='flex-1 font-mono text-xs rounded border border-border-strong bg-card px-2 py-1 focus:outline-none focus:ring-1 focus:ring-indigo-500'
          placeholder='cluster="prod"  (Enter to add)'
          disabled={readOnly}
        />
        <MatcherSuggestions id='pipeline-matcher-suggestions' orgId={orgId} />
      </div>
      {error && (
        <p
          id='pipeline-matcher-error'
          data-testid='pipeline-matcher-error'
          role='alert'
          className='text-xs text-red-400'
        >
          {error}
        </p>
      )}
    </div>
  );
}
