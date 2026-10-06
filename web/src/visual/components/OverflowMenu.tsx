import { Check, MoreHorizontal } from 'lucide-react';
import { type KeyboardEvent, useEffect, useId, useRef, useState } from 'react';

export type OverflowMenuItem =
  | {
      kind: 'item';
      testId: string;
      label: string;
      onSelect: () => void;
      /** Set when the action is unavailable: the item renders disabled with
       *  this as its tooltip. */
      disabledReason?: string;
    }
  | {
      kind: 'checkbox';
      testId: string;
      label: string;
      checked: boolean;
      onSelect: () => void;
    };

function enabledItems(menu: HTMLElement | null): HTMLButtonElement[] {
  return Array.from(
    menu?.querySelectorAll<HTMLButtonElement>('[role^="menuitem"]:not(:disabled)') ?? [],
  );
}

/**
 * A "More" button holding actions a narrow toolbar has no room for (#251).
 * WAI-ARIA menu button: the trigger reports aria-expanded, items are
 * menuitem / menuitemcheckbox, Escape and an outside click close it, and the
 * arrow keys move between the enabled items. Choosing an item closes it.
 */
export function OverflowMenu({
  items,
  testId,
  label,
}: {
  items: OverflowMenuItem[];
  testId: string;
  label: string;
}) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const menuId = useId();

  useEffect(() => {
    if (!open) return;
    enabledItems(menuRef.current)[0]?.focus();
    const onPointerDown = (e: PointerEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('pointerdown', onPointerDown);
    return () => document.removeEventListener('pointerdown', onPointerDown);
  }, [open]);

  const close = () => {
    setOpen(false);
    triggerRef.current?.focus();
  };

  const onMenuKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'Escape') {
      e.preventDefault();
      close();
      return;
    }
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    e.preventDefault();
    const list = enabledItems(menuRef.current);
    if (list.length === 0) return;
    const i = list.indexOf(document.activeElement as HTMLButtonElement);
    const next =
      e.key === 'ArrowDown' ? (i + 1) % list.length : (i - 1 + list.length) % list.length;
    list[next]?.focus();
  };

  return (
    <div className='relative shrink-0' ref={rootRef}>
      <button
        ref={triggerRef}
        type='button'
        data-testid={testId}
        aria-label={label}
        title={label}
        aria-haspopup='menu'
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={() => setOpen((v) => !v)}
        className='flex items-center gap-1 text-sm px-2 py-1 rounded border whitespace-nowrap'
      >
        <MoreHorizontal size={14} />
        More
      </button>
      {open && (
        <div
          ref={menuRef}
          id={menuId}
          role='menu'
          aria-label={label}
          onKeyDown={onMenuKeyDown}
          className='absolute right-0 top-full z-30 mt-1 min-w-[12rem] rounded border border-border bg-card py-1 text-xs shadow-md'
        >
          {items.map((item) => {
            const disabledReason = item.kind === 'item' ? item.disabledReason : undefined;
            return (
              <button
                key={item.testId}
                type='button'
                data-testid={item.testId}
                role={item.kind === 'checkbox' ? 'menuitemcheckbox' : 'menuitem'}
                aria-checked={item.kind === 'checkbox' ? item.checked : undefined}
                disabled={!!disabledReason}
                title={disabledReason}
                tabIndex={-1}
                onClick={() => {
                  close();
                  item.onSelect();
                }}
                className='flex w-full items-center gap-2 px-3 py-2 text-left whitespace-nowrap hover:bg-accent/10 focus:bg-accent/10 focus:outline-none disabled:opacity-50 disabled:cursor-not-allowed'
              >
                <span className='inline-flex w-3 justify-center'>
                  {item.kind === 'checkbox' && item.checked && <Check size={12} />}
                </span>
                {item.label}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
