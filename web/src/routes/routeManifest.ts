// routeManifest.ts — single source of truth for route tags.
// The protected-routes spec imports this to build its test matrix.
// Every route MUST have a tag. Missing tags fail the spec's completeness guard.

export interface RouteEntry {
  path: string;
  tag: 'public' | 'protected';
  /** A locator string that should be unique to this page when authenticated */
  distinctLocator?: string;
}

export const routeManifest: RouteEntry[] = [
  { path: '/login', tag: 'public' },
  { path: '/', tag: 'protected', distinctLocator: 'text=Overview' },
  { path: '/collectors', tag: 'protected', distinctLocator: 'text=Collectors' },
  { path: '/collectors/$id', tag: 'protected', distinctLocator: 'text=Collector' },
  { path: '/pipelines', tag: 'protected', distinctLocator: 'text=Pipelines' },
  { path: '/pipelines/new', tag: 'protected' },
  { path: '/pipelines/$id', tag: 'protected' },
  { path: '/destinations', tag: 'protected', distinctLocator: 'text=Destinations' },
  { path: '/teams', tag: 'protected', distinctLocator: 'text=Teams' },
  { path: '/git', tag: 'protected', distinctLocator: 'text=Git sync' },
  // Full-bleed canvas routes. They bypass contentRoute (see router.tsx) but are
  // still protected, and were missing here entirely -- which the completeness
  // guard could not notice, because it only checked that listed routes had a
  // tag rather than that every real route was listed.
  { path: '/pipelines/visual/new', tag: 'protected' },
  { path: '/pipelines/$id/visual', tag: 'protected' },
  { path: '/pipelines/$id/graph', tag: 'protected' },
  { path: '/wizards', tag: 'protected', distinctLocator: 'text=Wizards' },
  { path: '/wizards/$kind', tag: 'protected' },
  { path: '/admin/orgs', tag: 'protected', distinctLocator: 'text=Organisations' },
  { path: '/admin/clusters', tag: 'protected', distinctLocator: 'text=Clusters' },
  { path: '/admin/tokens', tag: 'protected', distinctLocator: 'text=Agent Tokens' },
  { path: '/admin/users', tag: 'protected', distinctLocator: 'text=Users' },
  { path: '/admin/auth', tag: 'protected', distinctLocator: 'text=Single sign-on' },
  { path: '/audit', tag: 'protected', distinctLocator: 'text=Audit log' },
];
