import { Suspense } from 'react';
import { lazyNamed } from '@/lib/lazyNamed';
import type { AlloyEditorProps } from './AlloyEditor';

/**
 * AlloyEditor, loaded on first use.
 *
 * CodeMirror (plus the Lezer grammar and the Alloy language/completion
 * sources built on it) is the single largest thing in the SPA that most
 * page views never touch: only the pipeline editor and the wizard's preview
 * render it. Importing it statically from those two pages put it in the
 * entry chunk, so every first paint — the login page included — paid for it.
 * Behind this boundary it is its own chunk, fetched when an editor first
 * mounts.
 *
 * The fallback reserves the editor's height so the surrounding layout does
 * not jump when the chunk lands. A chunk that fails to load rejects the lazy
 * boundary; that propagates to the route's error boundary
 * (RouteErrorFallback), the same path a failed page chunk takes.
 */
const AlloyEditorChunk = lazyNamed(() => import('./AlloyEditor'), 'AlloyEditor');

export function AlloyEditor(props: AlloyEditorProps) {
  return (
    <Suspense
      fallback={
        <div
          style={{ height: props.height ?? '100%' }}
          className='overflow-auto'
          data-testid='editor-loading'
          aria-busy='true'
        />
      }
    >
      <AlloyEditorChunk {...props} />
    </Suspense>
  );
}
