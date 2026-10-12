import { timestampDate } from '@bufbuild/protobuf/wkt';
import { type StatusTone, toneClass } from '@/components/ui/statusTone';
import type { TenantRoute } from '@/gen/shepherd/mgmt/v1/tenant_route_pb';

// Whether the route's HTTPRoute is in the cluster and attached — what the
// tenant-route reconciler last recorded. A refusal or error carries the
// gateway's or apiserver's reason, shown under the badge so it is not lost in
// a tooltip.
const APPLY: Record<string, { label: string; tone: StatusTone; hint: string }> = {
  pending: {
    label: 'pending',
    tone: 'neutral',
    hint: 'Not applied yet: the tenant-route reconciler applies it on its next pass.',
  },
  applied: {
    label: 'applied',
    tone: 'ok',
    hint: 'In the cluster and attached to the gateway.',
  },
  refused: {
    label: 'refused',
    tone: 'danger',
    hint: 'The gateway refused to attach the route — check its listeners and allowedRoutes.',
  },
  error: {
    label: 'error',
    tone: 'warn',
    hint: 'Applying the route failed; Shepherd retries with backoff.',
  },
  removed: {
    label: 'removed',
    tone: 'neutral',
    hint: 'The route no longer routes and its HTTPRoute has been deleted.',
  },
  // A Faro route, or (#304) any route on a server where no tenant-route
  // reconciler runs — apply_message says which.
  not_applicable: {
    label: 'not applied',
    tone: 'neutral',
    hint: 'Shepherd does not apply this route.',
  },
};

export function ApplyStatus({ route }: { route: TenantRoute }) {
  // A revoked route no longer routes whatever the reconciler has done yet;
  // "pending" would read as "about to be applied" (#205).
  if (route.status === 'revoked' && route.applyStatus !== 'removed') {
    return (
      <span className='block max-w-sm' data-testid={`route-apply-${route.segment}`}>
        <span
          className={`rounded px-1.5 py-0.5 text-xs font-medium ${toneClass('neutral')}`}
          title='Revoked: this route no longer routes. With route apply on, Shepherd removes its HTTPRoute.'
        >
          not routed
        </span>
      </span>
    );
  }
  const a = APPLY[route.applyStatus] ?? APPLY.pending;
  const title = route.appliedAt
    ? `${a.hint} Last verified ${timestampDate(route.appliedAt).toLocaleString()}.`
    : a.hint;
  const showMessage =
    route.applyStatus === 'refused' ||
    route.applyStatus === 'error' ||
    route.applyStatus === 'not_applicable';
  return (
    <span className='block max-w-sm' data-testid={`route-apply-${route.segment}`}>
      <span
        className={`rounded px-1.5 py-0.5 text-xs font-medium ${toneClass(a.tone)}`}
        title={title}
      >
        {a.label}
      </span>
      {showMessage && route.applyMessage && (
        <span className='mt-1 block break-words text-xs text-muted'>{route.applyMessage}</span>
      )}
    </span>
  );
}

export function gatewayLabel(r: TenantRoute): string {
  const name = `${r.gatewayNamespace ? `${r.gatewayNamespace}/` : ''}${r.gatewayName}`;
  return r.gatewayName ? `${r.gatewayMode} · ${name}` : r.gatewayMode;
}
