/**
 * Canvas geometry helpers for specs that drive React Flow with raw mouse
 * events.
 *
 * Placing a node re-fits the viewport (store.ts `refitView` → CanvasPane's
 * FitOnFirstNodes). The fit is synchronous once React Flow has measured the
 * new node, but that measurement lands on a ResizeObserver tick AFTER the node
 * is in the DOM — so a `boundingBox()` read the moment `toHaveCount` passes
 * can see the pre-fit position. Under CI's four-worker load that window is
 * wide enough to hit: the drag then starts on empty pane, pans the canvas
 * instead of moving the node, and no history step is written. Every screen
 * coordinate a drag is built from must come from a settled layout.
 */
import { expect, type Locator, type Page } from '@playwright/test';

export interface Box {
  x: number;
  y: number;
  width: number;
  height: number;
}

/** `locator.boundingBox()`, but only once two reads 100 ms apart agree — i.e.
 * once no fit, pan or layout pass is still moving the element. */
export async function settledBox(locator: Locator, timeout = 5_000): Promise<Box> {
  let prev: Box | null = null;
  let box: Box | null = null;
  await expect
    .poll(
      async () => {
        prev = box;
        box = await locator.boundingBox();
        return (
          box !== null &&
          prev !== null &&
          box.x === prev.x &&
          box.y === prev.y &&
          box.width === prev.width &&
          box.height === prev.height
        );
      },
      { timeout, intervals: [100], message: 'canvas layout did not settle' },
    )
    .toBe(true);
  return box!;
}

/** The `transform` React Flow applies to the viewport — its pan/zoom state.
 * Unchanged across a node drag; changed by a pane drag. Specs that move a node
 * assert it stayed put so a drag that missed the node fails on the spot,
 * instead of later and obscurely. */
export async function viewportTransform(page: Page): Promise<string> {
  return page.locator('.react-flow__viewport').evaluate((el) => el.style.transform);
}

/** Waits out a fitView pan/zoom (or any viewport transition) by polling
 * `.react-flow__viewport`'s inline style until two consecutive reads agree.
 * For specs that click with `{ force: true }` — which skips Playwright's own
 * "stable target" check — this is the only thing standing between the click
 * and a moving target (W7-13). */
export async function waitForViewportSettled(page: Page, timeout = 2_000): Promise<void> {
  const viewport = page.locator('.react-flow__viewport');
  let last: string | null = null;
  await expect
    .poll(
      async () => {
        const current = await viewport.getAttribute('style');
        const settled = last !== null && current === last;
        last = current;
        return settled;
      },
      { timeout, message: 'React Flow viewport did not settle' },
    )
    .toBe(true);
}
