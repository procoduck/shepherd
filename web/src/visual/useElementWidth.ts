import { type RefObject, useLayoutEffect, useState } from 'react';

/**
 * The element's current border-box width in px, kept up to date with a
 * ResizeObserver; 0 until the element is mounted. Measured in a layout
 * effect, so the first paint already uses the real width — a layout that
 * switches on it does not flash the wrong variant first.
 */
export function useElementWidth(ref: RefObject<HTMLElement | null>): number {
  const [width, setWidth] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setWidth(el.getBoundingClientRect().width);
    if (typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(() => setWidth(el.getBoundingClientRect().width));
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref]);
  return width;
}
