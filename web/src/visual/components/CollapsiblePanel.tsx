import { ChevronLeft, ChevronRight } from 'lucide-react';
import { type ReactNode, useCallback, useEffect, useState } from 'react';

/**
 * A builder side panel that can be collapsed to a narrow rail, after draw.io's
 * shape and Format panels.
 *
 * The canvas is the working surface and it was losing: with the app sidebar
 * (240px), the palette (256px) and the inspector (360px) all fixed, a 1280px
 * window left roughly 424px of canvas — under two node-widths, and narrow enough
 * that a placed node could land outside it entirely. Collapsing either panel
 * hands that space back, and the choice is remembered per panel.
 *
 * Collapsed state is a rail rather than nothing at all: a panel that vanishes
 * with no way back is worse than one that is merely small.
 */
export function CollapsiblePanel({
  side,
  storageKey,
  title,
  width,
  collapsedIcon,
  testId,
  children,
}: {
  side: 'left' | 'right';
  /** localStorage key holding this panel's collapsed state. */
  storageKey: string;
  /** Shown in the header and as the rail's tooltip. */
  title: string;
  /** Expanded width, e.g. `w-56`. */
  width: string;
  collapsedIcon: ReactNode;
  testId: string;
  children: ReactNode;
}) {
  const [collapsed, setCollapsed] = useState(() => {
    try {
      return localStorage.getItem(storageKey) === '1';
    } catch {
      // localStorage can throw (private browsing, disabled) — default to expanded.
      return false;
    }
  });

  useEffect(() => {
    try {
      localStorage.setItem(storageKey, collapsed ? '1' : '0');
    } catch {
      // Persistence is a convenience; failing to store it must not break the panel.
    }
  }, [collapsed, storageKey]);

  const toggle = useCallback(() => setCollapsed((c) => !c), []);

  // The chevron always points the way the panel will move.
  const ExpandIcon = side === 'left' ? ChevronRight : ChevronLeft;
  const CollapseIcon = side === 'left' ? ChevronLeft : ChevronRight;
  const borderSide = side === 'left' ? 'border-r' : 'border-l';

  if (collapsed) {
    return (
      <div
        className={`w-9 ${borderSide} border-border bg-panel flex flex-col items-center gap-2 py-2 shrink-0`}
        data-testid={testId}
        data-collapsed='true'
      >
        <button
          type='button'
          onClick={toggle}
          title={`Show ${title}`}
          aria-label={`Show ${title}`}
          aria-expanded={false}
          data-testid={`${testId}-toggle`}
          className='p-1 rounded text-muted hover:text-zinc-100 hover:bg-accent/10'
        >
          <ExpandIcon size={16} />
        </button>
        <div className='text-muted-2' aria-hidden='true'>
          {collapsedIcon}
        </div>
      </div>
    );
  }

  return (
    <div
      className={`${width} ${borderSide} border-border bg-panel flex flex-col overflow-hidden shrink-0`}
      data-testid={testId}
      data-collapsed='false'
    >
      <div className='h-8 px-2 flex items-center justify-between border-b border-border shrink-0'>
        <span className='text-[10px] font-semibold uppercase tracking-wider text-muted-2'>
          {title}
        </span>
        <button
          type='button'
          onClick={toggle}
          title={`Hide ${title}`}
          aria-label={`Hide ${title}`}
          aria-expanded={true}
          data-testid={`${testId}-toggle`}
          className='p-1 rounded text-muted hover:text-zinc-100 hover:bg-accent/10'
        >
          <CollapseIcon size={16} />
        </button>
      </div>
      <div className='flex-1 min-h-0 overflow-hidden flex flex-col'>{children}</div>
    </div>
  );
}
