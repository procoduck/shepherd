import { Suspense } from 'react';
import { lazyNamed } from '@/lib/lazyNamed';
import type { AlloyEditorProps } from './AlloyEditor';
import type { RevisionDiffProps } from './RevisionDiff';

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

// RevisionDiff pulls in @codemirror/merge. AlloyEditor.tsx re-exports it
// purely so this `import('./AlloyEditor')` is the one and only dynamic
// import that has to resolve — the same chunk as the plain editor, loaded
// the first time either is used. A page importing RevisionDiff straight
// from './RevisionDiff' instead of here would put @codemirror/merge back in
// the entry chunk (W-7's chunk-boundary proof).
const RevisionDiffChunk = lazyNamed(() => import('./AlloyEditor'), 'RevisionDiff');

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

export function RevisionDiff(props: RevisionDiffProps) {
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
      <RevisionDiffChunk {...props} />
    </Suspense>
  );
}
