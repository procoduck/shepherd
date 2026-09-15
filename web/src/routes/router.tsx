import { createRootRoute, createRoute, createRouter, Outlet } from '@tanstack/react-router';
import { type JSX, Suspense } from 'react';
import { RequireRole } from '@/components/RequireRole';
import { RouteErrorFallback } from '@/components/RouteErrorFallback';
import { Shell } from '@/components/Shell';
import { lazyNamed } from '@/lib/lazyNamed';
import { AdminAuthPage } from '@/pages/AdminAuthPage';
import { AdminClustersPage } from '@/pages/AdminClustersPage';
import { AdminOrgsPage } from '@/pages/AdminOrgsPage';
import { AdminTokensPage } from '@/pages/AdminTokensPage';
import { AdminUsersPage } from '@/pages/AdminUsersPage';
import { AuditPage } from '@/pages/AuditPage';
import { ChangePasswordPage } from '@/pages/ChangePasswordPage';
import { CollectorDetailPage } from '@/pages/CollectorDetailPage';
import { CollectorsPage } from '@/pages/CollectorsPage';
import { DestinationsPage } from '@/pages/DestinationsPage';
import { GitPage } from '@/pages/GitPage';
import { LoginPage } from '@/pages/LoginPage';
import { OverviewPage } from '@/pages/OverviewPage';
import { PipelineEditorPage } from '@/pages/PipelineEditorPage';
import { PipelinesPage } from '@/pages/PipelinesPage';
import { TeamsPage } from '@/pages/TeamsPage';
import { WizardsPage } from '@/pages/WizardsPage';
import { WizardRunnerPage } from '@/wizard/WizardRunnerPage';
import { routeManifest } from './routeManifest';

// Wraps a page component in RequireRole using routeManifest's requiredRole
// for `path` as the single source of truth (W6-S7 / routeManifest.test.ts) —
// a route with no requiredRole entry renders unguarded, same as before.
function withRequiredRole<P extends object>(path: string, Component: (props: P) => JSX.Element) {
  const requiredRole = routeManifest.find((r) => r.path === path)?.requiredRole;
  if (!requiredRole) return Component;
  return function RoleGuarded(props: P) {
    return (
      <RequireRole requiredRole={requiredRole}>
        <Component {...props} />
      </RequireRole>
    );
  };
}

const rootRoute = createRootRoute({
  component: Outlet,
  // No errorComponent here: TanStack Router resolves error boundaries per
  // LEAF match, so a rootRoute errorComponent never actually sees a page
  // crash — the nearest matched route below it always catches first.
  // RouteErrorFallback is wired as the router-wide defaultErrorComponent
  // below instead, which every leaf without its own errorComponent falls
  // back to.
});

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
  component: LoginPage,
});

// Outside the shell: a user owing a password change is refused by every
// shell route, so the screen has to live where no session guard runs. Both
// the forced flow (login / a 403 anywhere) and the voluntary one (Admin →
// Users) land here; `required` distinguishes them.
const changePasswordRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/change-password',
  validateSearch: (search: Record<string, unknown>): { required: boolean } => ({
    // TanStack's default parser JSON-decodes the value, so ?required=1 arrives
    // as the number 1 and ?required=true as the boolean — accept every shape a
    // link or a hard redirect can produce.
    required:
      search.required === true ||
      search.required === 1 ||
      search.required === '1' ||
      search.required === 'true',
  }),
  component: ChangePasswordPage,
});

const shellRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: 'shell',
  component: Shell,
});

// Pathless layout applied to ordinary (non-canvas) routes: the padded,
// max-width, page-scrolling content wrapper. The visual pipeline builder and
// graph view routes intentionally bypass this layout — they render directly
// under the shell so they can fill the viewport without page scroll (R1-C1).
const contentRoute = createRoute({
  getParentRoute: () => shellRoute,
  id: 'content',
  component: () => (
    <div className='h-full overflow-y-auto'>
      <div className='max-w-[1400px] mx-auto px-6 py-6'>
        <Outlet />
      </div>
    </div>
  ),
});

const overviewRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/',
  component: OverviewPage,
});

const collectorsRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/collectors',
  component: CollectorsPage,
});

const collectorDetailRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/collectors/$id',
  component: CollectorDetailPage,
});

const pipelinesRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/pipelines',
  component: PipelinesPage,
});

const pipelineNewRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/pipelines/new',
  component: withRequiredRole('/pipelines/new', PipelineEditorPage),
});

const pipelineEditRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/pipelines/$id',
  component: PipelineEditorPage,
});

// The visual builder and graph view are the heaviest pages (React Flow,
// dagre); they load on first visit. lazyNamed (src/lib) rejects readably when
// a chunk lacks its export, so RouteErrorFallback shows a message instead of
// React's opaque #306.
const VisualBuilderPageLazy = lazyNamed(
  () => import('@/visual/components/VisualBuilderPage'),
  'VisualBuilderPage',
);
const GraphViewPageLazy = lazyNamed(
  () => import('@/visual/components/GraphViewPage'),
  'GraphViewPage',
);
const visualNewRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: '/pipelines/visual/new',
  component: () => (
    <Suspense fallback={<div className='h-full p-4 text-sm'>Loading visual builder…</div>}>
      <VisualBuilderPageLazy />
    </Suspense>
  ),
});
const visualEditRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: '/pipelines/$id/visual',
  component: () => (
    <Suspense fallback={<div className='h-full p-4 text-sm'>Loading visual builder…</div>}>
      <VisualBuilderPageLazy />
    </Suspense>
  ),
});
const graphViewRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: '/pipelines/$id/graph',
  component: () => (
    <Suspense fallback={<div className='h-full p-4 text-sm'>Loading graph view…</div>}>
      <GraphViewPageLazy />
    </Suspense>
  ),
});

const destinationsRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/destinations',
  component: DestinationsPage,
});

const gitRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/git',
  component: withRequiredRole('/git', GitPage),
});

const wizardsRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/wizards',
  component: withRequiredRole('/wizards', WizardsPage),
});

// One route for every wizard: the runner reads the kind from the path and
// renders whatever schema the backend returns for it.
const wizardRunnerRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/wizards/$kind',
  component: withRequiredRole('/wizards/$kind', WizardRunnerPage),
});

const adminOrgsRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/admin/orgs',
  component: withRequiredRole('/admin/orgs', AdminOrgsPage),
});

const adminClustersRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/admin/clusters',
  component: withRequiredRole('/admin/clusters', AdminClustersPage),
});

const adminTokensRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/admin/tokens',
  component: withRequiredRole('/admin/tokens', AdminTokensPage),
});

// Admin → Single sign-on. Not org-scoped: OIDC configuration decides who can
// hold an app-admin session at all, so it sits beside the other app-admin
// surfaces rather than under an org.
const adminUsersRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/admin/users',
  component: withRequiredRole('/admin/users', AdminUsersPage),
});

const adminAuthRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/admin/auth',
  component: withRequiredRole('/admin/auth', AdminAuthPage),
});

const auditRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/audit',
  component: withRequiredRole('/audit', AuditPage),
});

// Teams are org-scoped, not app-admin: they live beside the other org
// surfaces and read the selected org, rather than under /admin.
const teamsRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/teams',
  component: withRequiredRole('/teams', TeamsPage),
});

const routeTree = rootRoute.addChildren([
  loginRoute,
  changePasswordRoute,
  shellRoute.addChildren([
    contentRoute.addChildren([
      overviewRoute,
      collectorsRoute,
      collectorDetailRoute,
      pipelinesRoute,
      pipelineNewRoute,
      pipelineEditRoute,
      destinationsRoute,
      gitRoute,
      teamsRoute,
      wizardsRoute,
      wizardRunnerRoute,
      adminOrgsRoute,
      adminClustersRoute,
      adminTokensRoute,
      adminUsersRoute,
      adminAuthRoute,
      auditRoute,
    ]),
    // Full-bleed canvas routes bypass contentRoute (see contentRoute comment).
    visualNewRoute,
    visualEditRoute,
    graphViewRoute,
  ]),
]);

export const router = createRouter({ routeTree, defaultErrorComponent: RouteErrorFallback });

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router;
  }
}
