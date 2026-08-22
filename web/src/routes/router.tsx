import { createRootRoute, createRoute, createRouter, Outlet } from '@tanstack/react-router';
import { lazy, Suspense } from 'react';
import { Shell } from '@/components/Shell';
import { AdminClustersPage } from '@/pages/AdminClustersPage';
import { AdminOrgsPage } from '@/pages/AdminOrgsPage';
import { AdminTokensPage } from '@/pages/AdminTokensPage';
import { AuditPage } from '@/pages/AuditPage';
import { CollectorDetailPage } from '@/pages/CollectorDetailPage';
import { CollectorsPage } from '@/pages/CollectorsPage';
import { DestinationsPage } from '@/pages/DestinationsPage';
import { GitPage } from '@/pages/GitPage';
import { LoginPage } from '@/pages/LoginPage';
import { OverviewPage } from '@/pages/OverviewPage';
import { PipelineEditorPage } from '@/pages/PipelineEditorPage';
import { PipelinesPage } from '@/pages/PipelinesPage';
import { WizardsPage } from '@/pages/WizardsPage';
import { WizardRunnerPage } from '@/wizard/WizardRunnerPage';

const rootRoute = createRootRoute({
  component: Outlet,
  errorComponent: ({ error }) => (
    <div style={{ padding: 16, color: 'red' }}>
      <strong>Router Error:</strong> {String(error)}
    </div>
  ),
});

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
  component: LoginPage,
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
  component: PipelineEditorPage,
});

const pipelineEditRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/pipelines/$id',
  component: PipelineEditorPage,
});

const VisualBuilderPageLazy = lazy(() =>
  import('@/visual/components/VisualBuilderPage').then((m) => ({ default: m.VisualBuilderPage })),
);
const GraphViewPageLazy = lazy(() =>
  import('@/visual/components/GraphViewPage').then((m) => ({ default: m.GraphViewPage })),
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
  component: GitPage,
});

const wizardsRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/wizards',
  component: WizardsPage,
});

// One route for every wizard: the runner reads the kind from the path and
// renders whatever schema the backend returns for it.
const wizardRunnerRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/wizards/$kind',
  component: WizardRunnerPage,
});

const adminOrgsRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/admin/orgs',
  component: AdminOrgsPage,
});

const adminClustersRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/admin/clusters',
  component: AdminClustersPage,
});

const adminTokensRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/admin/tokens',
  component: AdminTokensPage,
});

const auditRoute = createRoute({
  getParentRoute: () => contentRoute,
  path: '/audit',
  component: AuditPage,
});

const routeTree = rootRoute.addChildren([
  loginRoute,
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
      wizardsRoute,
      wizardRunnerRoute,
      adminOrgsRoute,
      adminClustersRoute,
      adminTokensRoute,
      auditRoute,
    ]),
    // Full-bleed canvas routes bypass contentRoute (see contentRoute comment).
    visualNewRoute,
    visualEditRoute,
    graphViewRoute,
  ]),
]);

export const router = createRouter({ routeTree });

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router;
  }
}
