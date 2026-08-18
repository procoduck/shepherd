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

const overviewRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: '/',
  component: OverviewPage,
});

const collectorsRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: '/collectors',
  component: CollectorsPage,
});

const collectorDetailRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: '/collectors/$id',
  component: CollectorDetailPage,
});

const pipelinesRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: '/pipelines',
  component: PipelinesPage,
});

const pipelineNewRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: '/pipelines/new',
  component: PipelineEditorPage,
});

const pipelineEditRoute = createRoute({
  getParentRoute: () => shellRoute,
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
    <Suspense fallback={<div className='p-4 text-sm'>Loading visual builder…</div>}>
      <VisualBuilderPageLazy />
    </Suspense>
  ),
});
const visualEditRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: '/pipelines/$id/visual',
  component: () => (
    <Suspense fallback={<div className='p-4 text-sm'>Loading visual builder…</div>}>
      <VisualBuilderPageLazy />
    </Suspense>
  ),
});
const graphViewRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: '/pipelines/$id/graph',
  component: () => (
    <Suspense fallback={<div className='p-4 text-sm'>Loading graph view…</div>}>
      <GraphViewPageLazy />
    </Suspense>
  ),
});

const destinationsRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: '/destinations',
  component: DestinationsPage,
});

const gitRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: '/git',
  component: GitPage,
});

const wizardsRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: '/wizards',
  component: WizardsPage,
});

const adminOrgsRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: '/admin/orgs',
  component: AdminOrgsPage,
});

const adminClustersRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: '/admin/clusters',
  component: AdminClustersPage,
});

const adminTokensRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: '/admin/tokens',
  component: AdminTokensPage,
});

const auditRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: '/audit',
  component: AuditPage,
});

const routeTree = rootRoute.addChildren([
  loginRoute,
  shellRoute.addChildren([
    overviewRoute,
    collectorsRoute,
    collectorDetailRoute,
    pipelinesRoute,
    pipelineNewRoute,
    pipelineEditRoute,
    visualNewRoute,
    visualEditRoute,
    graphViewRoute,
    destinationsRoute,
    gitRoute,
    wizardsRoute,
    adminOrgsRoute,
    adminClustersRoute,
    adminTokensRoute,
    auditRoute,
  ]),
]);

export const router = createRouter({ routeTree });

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router;
  }
}
