/**
 * elkLayout.ts — loads elkjs for the graph view, off the main thread.
 *
 * elkjs is the GWT-compiled Eclipse Layout Kernel: ~1.6 MB of JavaScript. It is
 * never imported statically. GraphViewPage calls loadElk() on mount, so the
 * other routes never fetch it: elk-api (the small client) comes in as its own
 * chunk, and the layout engine itself (elk-worker.min.js) is shipped as a plain
 * asset that runs in a Web Worker — the layout never blocks the page, and the
 * engine is not a JS chunk that would trip vite.config.ts's chunk-size
 * warning. Every failure (the chunk or the worker script fails to load, the
 * worker throws, the layout hangs) rejects, and the page falls back to the
 * server's grid.
 */
import type { ELK } from 'elkjs/lib/elk-api';

/** How long a layout may take before the page gives up on it. */
export const LAYOUT_TIMEOUT_MS = 15_000;

export interface ElkHandle {
  elk: ELK;
  /** Rejects as soon as the worker fails (its script did not load, it threw). */
  failed: Promise<never>;
  dispose: () => void;
}

export async function loadElk(): Promise<ElkHandle> {
  const [{ default: ElkConstructor }, { default: workerUrl }] = await Promise.all([
    import('elkjs/lib/elk-api.js'),
    import('elkjs/lib/elk-worker.min.js?url'),
  ]);
  let worker: Worker | undefined;
  let fail: (reason: Error) => void = () => undefined;
  const failed = new Promise<never>((_, reject) => {
    fail = reject;
  });
  // Handled here so an unobserved failure is never an unhandled rejection;
  // callers race against it.
  failed.catch(() => undefined);
  const elk = new ElkConstructor({
    workerUrl,
    workerFactory: (url) => {
      worker = new Worker(url ?? workerUrl);
      worker.addEventListener('error', () => fail(new Error('the layout worker failed to load')));
      return worker;
    },
  });
  return { elk, failed, dispose: () => worker?.terminate() };
}

/** Races a layout against its worker failing and LAYOUT_TIMEOUT_MS. */
export function withLayoutGuards<T>(handle: ElkHandle, layout: Promise<T>): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error('the layout timed out')), LAYOUT_TIMEOUT_MS);
  });
  return Promise.race([layout, handle.failed, timeout]).finally(() => clearTimeout(timer));
}
