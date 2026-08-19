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
  { path: '/wizards', tag: 'protected', distinctLocator: 'text=Wizards' },
  { path: '/wizards/app-observability', tag: 'protected' },
  { path: '/admin/orgs', tag: 'protected', distinctLocator: 'text=Organisations' },
  { path: '/admin/clusters', tag: 'protected', distinctLocator: 'text=Clusters' },
  { path: '/admin/tokens', tag: 'protected', distinctLocator: 'text=Agent Tokens' },
  { path: '/audit', tag: 'protected', distinctLocator: 'text=Audit log' },
];
