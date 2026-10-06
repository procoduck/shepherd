import { useQuery } from '@tanstack/react-query';
import { clients } from '@/api/transport';
import type { CrumbResolver } from '@/components/breadcrumb';

// The detail routes whose breadcrumb names the item (#250) rather than the
// route's generic label ("Pipeline", "Collector", "Wizard").
const PIPELINE_ROUTE = /^\/pipelines\/([^/]+)(?:\/(?:visual|graph))?$/;
const COLLECTOR_ROUTE = /^\/collectors\/([^/]+)$/;
const WIZARD_ROUTE = /^\/wizards\/([^/]+)$/;

function match(re: RegExp, pathname: string, reserved: string[] = []): string {
  const id = re.exec(pathname)?.[1];
  return id && !reserved.includes(id) ? id : '';
}

/** How a collector is named on its own page's heading: "cluster · role". */
export function collectorCrumbName(c: { cluster?: string; role?: string }): string | undefined {
  const parts = [c.cluster, c.role].filter((p): p is string => !!p);
  return parts.length > 0 ? parts.join(' · ') : undefined;
}

/**
 * Resolves the name of the item a detail route shows, for the breadcrumb.
 *
 * Each query uses the SAME key and fetcher as the page that shows the item
 * (PipelineEditorPage, CollectorDetailPage, WizardRunnerPage), so on those
 * pages it is served from the page's own request rather than a second one —
 * and, unlike the getQueryData read this replaced, it re-renders the shell
 * when the item arrives instead of keeping the generic label it rendered
 * before the page's request finished. The canvas routes load the pipeline
 * outside React Query, so there this is the one GetPipeline the name costs.
 *
 * Retry options match the page's own observer for the same key, so sharing
 * the query never changes how the page behaves. A failure (no access,
 * another org) just leaves the generic label.
 */
export function useCrumbNames(pathname: string, orgId: string): CrumbResolver {
  const pipelineId = match(PIPELINE_ROUTE, pathname, ['new', 'visual']);
  const collectorId = match(COLLECTOR_ROUTE, pathname);
  const wizardKind = match(WIZARD_ROUTE, pathname);

  const { data: pipeline } = useQuery({
    queryKey: ['pipeline', orgId, pipelineId],
    queryFn: () => clients.pipeline.getPipeline({ orgId, id: pipelineId }),
    enabled: !!orgId && !!pipelineId,
  });
  const { data: collector } = useQuery({
    queryKey: ['collector', orgId, collectorId],
    queryFn: () => clients.fleet.getCollector({ orgId, id: collectorId }),
    enabled: !!orgId && !!collectorId,
  });
  const { data: wizard } = useQuery({
    queryKey: ['wizard-schema', orgId, wizardKind],
    queryFn: () => clients.wizard.getWizardSchema({ orgId, kind: wizardKind }),
    enabled: !!orgId && !!wizardKind,
    // Matches WizardRunnerPage's own observer: an unknown kind is a 404 that
    // stays one, and the page shows its message without a retry cycle.
    retry: false,
  });

  return (route, params) => {
    switch (route.path) {
      // Only the item's own crumb: on /pipelines/$id/graph the trail is
      // "Pipelines / <name> / Graph view", not the name twice.
      case '/pipelines/$id':
        return params.id === pipelineId ? pipeline?.name || undefined : undefined;
      case '/collectors/$id':
        return params.id === collectorId && collector ? collectorCrumbName(collector) : undefined;
      case '/wizards/$kind':
        return params.kind === wizardKind ? wizard?.title || undefined : undefined;
      default:
        return undefined;
    }
  };
}
