import { useState } from 'react';

const WRAP_LINES_KEY = 'shepherd.editor.wrapLines';

/** The text editor's wrap-lines choice (#307), remembered in localStorage —
 *  which can throw (private browsing, blocked storage), and then the choice
 *  simply lasts for this visit only. */
export function useWrapLines(): [boolean, (wrap: boolean) => void] {
  const [wrap, setWrap] = useState(() => {
    try {
      return window.localStorage.getItem(WRAP_LINES_KEY) === '1';
    } catch {
      return false;
    }
  });
  const set = (next: boolean) => {
    setWrap(next);
    try {
      window.localStorage.setItem(WRAP_LINES_KEY, next ? '1' : '0');
    } catch {
      // Not remembered; the toggle still works for this visit.
    }
  };
  return [wrap, set];
}

/** #307: long lines either scroll sideways or wrap. A toggle button, so it is
 *  announced as "Wrap lines, toggle button, pressed / not pressed". */
export function WrapLinesToggle({
  wrap,
  onChange,
}: {
  wrap: boolean;
  onChange: (wrap: boolean) => void;
}) {
  return (
    <button
      type='button'
      aria-pressed={wrap}
      onClick={() => onChange(!wrap)}
      className={`rounded border border-border px-2 py-1 text-xs font-medium hover:text-zinc-100 ${
        wrap ? 'text-accent' : 'text-muted'
      }`}
      data-testid='wrap-lines-toggle'
    >
      Wrap lines
    </button>
  );
}
