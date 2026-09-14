import { type ReactNode, useEffect, useRef, useState } from 'react';

/**
 * Shared modal shell for every overlay in the app — originally
 * AdminModal.tsx, promoted here (S2) so the one dialog implementation (and
 * its accessibility guarantees) covers admin forms and page-level overlays
 * alike, rather than each page re-inventing the backdrop and focus trap.
 */
let modalSeq = 0;

const SIZE_CLASSES: Record<'md' | 'lg' | 'xl', string> = {
  md: 'max-w-md',
  lg: 'max-w-lg',
  xl: 'max-w-xl',
};

export function Modal({
  title,
  onClose,
  children,
  size = 'md',
  testId,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  size?: 'md' | 'lg' | 'xl';
  testId?: string;
}) {
  const [titleId] = useState(() => `modal-title-${++modalSeq}`);
  const panelRef = useRef<HTMLDivElement>(null);

  // Read the latest onClose at Escape time without making it an effect
  // dependency — see the effect below (the same pattern UpgradeReview.tsx
  // uses for docRef).
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  // Escape to close, and keep focus inside the dialog.
  //
  // aria-modal="true" tells assistive technology the rest of the page is inert.
  // It was not: focus tabbed straight out into the visually obscured page
  // behind, which is the worst combination -- a screen reader is told one thing
  // while the keyboard does another.
  //
  // Keyed on [] — deliberately NOT [onClose]: every page-owned dialog whose
  // form state lives in the page (not the dialog itself) passes a fresh
  // inline `onClose` arrow on every keystroke's re-render. Keying this
  // effect on `onClose` reran it, including the panelRef.current?.focus()
  // call, on every keystroke — which yanked focus out of the input back to
  // the panel, so only the first typed character ever landed. The panel
  // must be focused once per mount and the keydown listener installed once;
  // onCloseRef above keeps Escape calling the current onClose regardless.
  useEffect(() => {
    const previouslyFocused = document.activeElement as HTMLElement | null;
    panelRef.current?.focus();

    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        onCloseRef.current();
        return;
      }
      if (e.key !== 'Tab') return;
      const focusable = panelRef.current?.querySelectorAll<HTMLElement>(
        'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
      );
      if (!focusable || focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    };

    document.addEventListener('keydown', onKeyDown, true);
    return () => {
      document.removeEventListener('keydown', onKeyDown, true);
      // Returning focus matters as much as trapping it: without this the
      // caret lands back at the top of the document after every dialog.
      previouslyFocused?.focus?.();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <div className='fixed inset-0 z-50 flex items-center justify-center bg-black/60'>
      <div
        ref={panelRef}
        tabIndex={-1}
        role='dialog'
        aria-modal='true'
        aria-labelledby={titleId}
        data-testid={testId}
        className={`w-full ${SIZE_CLASSES[size]} rounded-xl border border-border bg-background p-6 shadow-2xl`}
      >
        <div className='mb-4 flex items-center justify-between'>
          <h2 id={titleId} className='text-base font-semibold'>
            {title}
          </h2>
          <button
            type='button'
            onClick={onClose}
            aria-label='Close'
            className='text-muted-3 hover:text-zinc-200'
          >
            ×
          </button>
        </div>
        {children}
      </div>
    </div>
  );
}

/** Shared Cancel/Submit button row for modal forms. */
export function ModalActions({
  onCancel,
  submitLabel,
  pendingLabel,
  pending,
  danger,
  submitTestId,
}: {
  onCancel: () => void;
  submitLabel: string;
  pendingLabel: string;
  pending: boolean;
  danger?: boolean;
  submitTestId?: string;
}) {
  return (
    <div className='flex justify-end gap-2 pt-2'>
      <button
        type='button'
        onClick={onCancel}
        className='px-4 py-1.5 text-sm text-muted hover:text-zinc-200'
      >
        Cancel
      </button>
      <button
        type='submit'
        disabled={pending}
        data-testid={submitTestId}
        className={`rounded-md px-4 py-1.5 text-sm text-white disabled:opacity-50 ${
          danger ? 'bg-red-600 hover:bg-red-500' : 'bg-indigo-600 hover:bg-indigo-500'
        }`}
      >
        {pending ? pendingLabel : submitLabel}
      </button>
    </div>
  );
}
