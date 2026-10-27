import type { Diagnostic } from '@/gen/shepherd/mgmt/v1/common_pb';

/**
 * The pipeline editor's problems panel: every diagnostic of the last
 * validation, errors and warnings, each with its line:col. A diagnostic about
 * the whole config (the nearest-older-schema note) has no position — line 0 —
 * and is listed without one rather than as "0:0" (0.21 walkthrough).
 */
export function EditorProblemsPanel({ diagnostics }: { diagnostics: Diagnostic[] }) {
  if (diagnostics.length === 0) return null;
  return (
    <div className='max-h-40 overflow-y-auto border-t border-border bg-background p-2 space-y-1'>
      {diagnostics.map((d, i) => (
        <p
          key={i}
          className={`font-mono text-xs ${d.warning ? 'text-warn' : 'text-red-400'}`}
          data-testid={d.warning ? 'editor-warning' : 'editor-problem'}
        >
          {d.line > 0 && (
            <>
              {d.line}:{d.col} &nbsp;{' '}
            </>
          )}
          {d.warning ? 'warning: ' : ''}
          {d.message}
        </p>
      ))}
    </div>
  );
}
