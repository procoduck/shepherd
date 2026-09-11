import { type ComponentType, type LazyExoticComponent, lazy } from 'react';

/**
 * React.lazy for a chunk's NAMED export.
 *
 * React.lazy takes a chunk's `default`; the pages and widgets split out of the
 * entry are named exports, so the loader re-shapes the module. A chunk that
 * evaluates but lacks the export (a stale deploy, a proxy answering with the
 * wrong body) must reject here, with a message the error boundary can show,
 * rather than resolve to `undefined` and surface as React's opaque error #306
 * ("Element type is invalid. Received a promise that resolves to: undefined.
 * Lazy element type must resolve to a class or function"). Checked against
 * null, not `typeof 'function'`: a memo() or forwardRef() component is an
 * object.
 */
/** The props of the component at `M[K]`, so the lazy stand-in is typed like
 * the real thing and a caller passing the wrong props is caught at compile
 * time, not by the chunk at runtime. */
type PropsOf<C> = C extends ComponentType<infer P> ? P : never;

export function lazyNamed<M extends object, K extends keyof M & string>(
  load: () => Promise<M>,
  name: K,
): LazyExoticComponent<ComponentType<PropsOf<M[K]>>> {
  return lazy(async () => {
    const mod = await load();
    const Component = mod[name];
    if (Component == null) {
      throw new Error(`Failed to load this page: its code loaded without a "${name}" export.`);
    }
    return { default: Component as unknown as ComponentType<PropsOf<M[K]>> };
  });
}
