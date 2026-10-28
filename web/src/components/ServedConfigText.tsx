import { useEffect, useRef } from 'react';
import type { ShowLines } from '@/components/CollectorPipelines';
import { spanContains } from '@/lib/servedProvenance';

/**
 * The served config as read-only text, one span per line so a pipeline's
 * block (F-CONTRIB: the Pipelines tab's "Lines N–M") can be highlighted and
 * scrolled to. The text content is the served content unchanged.
 */
export function ServedConfigText({
  content,
  highlight,
}: {
  content: string;
  highlight?: ShowLines;
}) {
  const first = useRef<HTMLSpanElement>(null);
  const from = highlight?.lines.first;
  const to = highlight?.lines.last;
  useEffect(() => {
    if (from !== undefined && to !== undefined)
      first.current?.scrollIntoView?.({ block: 'center' });
  }, [from, to]);
  const lines = content.split('\n');
  return (
    <pre
      data-testid='served-config-text'
      className='rounded-lg border border-border bg-background p-4 text-xs font-mono text-zinc-300 overflow-auto max-h-[60vh] whitespace-pre-wrap'
    >
      {lines.map((line, i) => {
        const n = i + 1;
        const on = spanContains(highlight?.lines, n);
        const text = i < lines.length - 1 ? `${line}\n` : line;
        if (!on) return text;
        return (
          <mark
            key={n}
            ref={n === highlight?.lines.first ? first : undefined}
            data-line={n}
            className='bg-info-surface text-info'
          >
            {text}
          </mark>
        );
      })}
    </pre>
  );
}
