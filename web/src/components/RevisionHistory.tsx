import { timestampDate } from '@bufbuild/protobuf/wkt';
import { ChevronDown } from 'lucide-react';
import { useState } from 'react';
import type { PipelineRevision } from '@/gen/shepherd/mgmt/v1/pipeline_pb';
import { formatTimestampRelative } from '@/lib/utils';

/**
 * The pipeline page's collapsible revision list. Each row shows when it was
 * made — relative, with the exact date and time on hover (#252) — and the
 * newest one is marked current: it is what the pipeline already is, so the
 * page offers no Restore for it.
 */
export function RevisionHistory({
  revisions,
  currentRevision,
  onView,
}: {
  revisions: PipelineRevision[];
  currentRevision: number;
  onView: (revision: number) => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div className='space-y-2'>
      <button
        onClick={() => setOpen((r) => !r)}
        className='flex items-center gap-1 text-xs font-medium text-muted hover:text-zinc-200'
      >
        <ChevronDown size={12} className={`transition-transform ${open ? '' : '-rotate-90'}`} />
        Revision history ({revisions.length})
      </button>
      {open && (
        <div className='space-y-1 max-h-52 overflow-y-auto pr-1'>
          {revisions.map((r) => (
            <div
              key={r.revision}
              className='rounded border border-border bg-card/40 p-2 text-xs space-y-1'
            >
              <div className='flex items-center justify-between'>
                <span className='font-medium text-zinc-300'>
                  #{r.revision}
                  {r.revision === currentRevision && (
                    <span
                      className='ml-1.5 rounded bg-emerald-500/15 px-1 py-0.5 text-[10px] font-medium text-emerald-500'
                      data-testid='current-revision-badge'
                    >
                      current
                    </span>
                  )}
                </span>
                {r.changedAt && (
                  <time
                    className='text-muted-3'
                    dateTime={timestampDate(r.changedAt).toISOString()}
                    title={timestampDate(r.changedAt).toLocaleString(undefined, {
                      dateStyle: 'medium',
                      timeStyle: 'short',
                    })}
                    data-testid='revision-time'
                    data-revision={r.revision}
                  >
                    {formatTimestampRelative(r.changedAt)}
                  </time>
                )}
              </div>
              <div className='text-muted-2'>{r.changedBy}</div>
              {r.changeNote && <div className='text-muted italic'>{r.changeNote}</div>}
              <button
                onClick={() => onView(r.revision)}
                className='text-indigo-400 hover:text-indigo-300 text-xs'
                data-testid='view-revision-btn'
                data-revision={r.revision}
              >
                View diff
              </button>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
