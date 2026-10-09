import type { RouteEntry } from '@/routes/routeManifest';

export interface Crumb {
  label: string;
}

/**
 * Resolves an entity's own name for a matched route's dynamic params (e.g.
 * a pipeline's title for '/pipelines/$id'), reading whatever the caller
 * already has cached rather than fetching. Return undefined to fall back to
 * the route's generic `label`.
 */
export type CrumbResolver = (
  route: RouteEntry,
  params: Record<string, string>,
) => string | undefined;

const GROUP_LABELS: Record<string, string> = {
  admin: 'Admin',
};

function titleCase(segment: string): string {
  return segment.length === 0 ? segment : segment.charAt(0).toUpperCase() + segment.slice(1);
}

/** True when every literal (non-`$param`) segment of `template` matches the
 *  same-position segment of `actual`, and both have the same segment count. */
function pathMatches(template: string, actualSegments: string[]): boolean {
  const templateSegments = template.split('/').filter(Boolean);
  if (templateSegments.length !== actualSegments.length) return false;
  return templateSegments.every((seg, i) => seg.startsWith('$') || seg === actualSegments[i]);
}

function extractParams(template: string, actualSegments: string[]): Record<string, string> {
  const params: Record<string, string> = {};
  template
    .split('/')
    .filter(Boolean)
    .forEach((seg, i) => {
      if (seg.startsWith('$')) params[seg.slice(1)] = actualSegments[i];
    });
  return params;
}

/**
 * Builds the breadcrumb trail for `pathname` from routeManifest's route
 * labels, walking every path segment — not only the terminal route — so a
 * grouping segment with no page of its own (e.g. 'admin' in /admin/users)
 * still gets a readable crumb instead of the raw pathname Shell.tsx used to
 * render directly.
 */
export function buildCrumbs(
  pathname: string,
  manifest: RouteEntry[],
  resolveName: CrumbResolver,
): Crumb[] {
  if (pathname === '/') {
    const overview = manifest.find((r) => r.path === '/');
    return [{ label: overview?.label ?? 'Overview' }];
  }

  const segments = pathname.split('/').filter(Boolean);
  const crumbs: Crumb[] = [];

  for (let end = 1; end <= segments.length; end++) {
    const actualSegments = segments.slice(0, end);
    // '$' is the router's 404 catch-all (a splat, not a one-segment param):
    // it names no page, so it never supplies a crumb.
    const route = manifest.find(
      (r) => r.path !== '/' && r.path !== '$' && pathMatches(r.path, actualSegments),
    );
    if (route) {
      const params = extractParams(route.path, actualSegments);
      const resolved = resolveName(route, params);
      crumbs.push({ label: resolved ?? route.label ?? titleCase(actualSegments[end - 1]) });
    } else {
      const segment = actualSegments[end - 1];
      crumbs.push({ label: GROUP_LABELS[segment] ?? titleCase(segment) });
    }
  }
  return crumbs;
}
