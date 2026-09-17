import { timestampDate } from '@bufbuild/protobuf/wkt';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Plus, RotateCw, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { AdminConfirmDialog } from '@/components/admin/AdminConfirmDialog';
import { QueryError } from '@/components/QueryError';
import { DataTable, type DataTableColumn } from '@/components/ui/DataTable';
import { Field, Input, Select } from '@/components/ui/Field';
import { Modal, ModalActions } from '@/components/ui/Modal';
import type { TenantRoute } from '@/gen/shepherd/mgmt/v1/tenant_route_pb';
import { useCanAdminister, useOrgId } from '@/hooks/useOrg';

// A route's lifecycle state, coloured so "active" reads apart from a route
// that is mid-rotation or already revoked at a glance.
function StatusBadge({ status }: { status: string }) {
  const tone =
    status === 'active'
      ? 'bg-emerald-500/15 text-emerald-400'
      : status === 'deprecated'
        ? 'bg-amber-500/15 text-amber-400'
        : 'bg-zinc-500/15 text-muted-2';
  return <span className={`rounded px-1.5 py-0.5 text-xs font-medium ${tone}`}>{status}</span>;
}

function routeColumns(
  canAdminister: boolean,
  onRotate: (r: TenantRoute) => void,
  onRevoke: (r: TenantRoute) => void,
): DataTableColumn<TenantRoute>[] {
  const cols: DataTableColumn<TenantRoute>[] = [
    {
      key: 'segment',
      header: 'Segment',
      cellClassName: 'px-4 py-2.5 font-mono text-xs',
      render: (r) => `/${r.kind}/${r.segment}`,
    },
    { key: 'kind', header: 'Kind', render: (r) => r.kind },
    { key: 'status', header: 'Status', render: (r) => <StatusBadge status={r.status} /> },
    {
      key: 'gateway',
      header: 'Gateway',
      render: (r) =>
        r.gatewayMode === 'managed'
          ? 'managed'
          : `${r.gatewayNamespace ? `${r.gatewayNamespace}/` : ''}${r.gatewayName}`,
    },
    {
      key: 'valid_until',
      header: 'Valid until',
      cellClassName: 'px-4 py-2.5 text-muted',
      render: (r) =>
        r.validUntil ? (
          timestampDate(r.validUntil).toLocaleString()
        ) : (
          <span className='text-muted-3'>—</span>
        ),
    },
  ];
  if (canAdminister) {
    cols.push({
      key: 'actions',
      header: '',
      cellClassName: 'px-4 py-2.5 text-right',
      render: (r) =>
        r.status === 'revoked' ? null : (
          <span className='flex items-center justify-end gap-2'>
            {r.status === 'active' && (
              <button
                type='button'
                onClick={() => onRotate(r)}
                aria-label={`Rotate ${r.segment}`}
                className='text-muted-3 hover:text-indigo-400'
                data-testid={`route-rotate-${r.segment}`}
              >
                <RotateCw size={14} />
              </button>
            )}
            <button
              type='button'
              onClick={() => onRevoke(r)}
              aria-label={`Revoke ${r.segment}`}
              className='text-muted-3 hover:text-red-400'
              data-testid={`route-revoke-${r.segment}`}
            >
              <Trash2 size={14} />
            </button>
          </span>
        ),
    });
  }
  return cols;
}

const EMPTY_CREATE = {
  kind: 'otlp',
  format: 'slug-suffix',
  gatewayMode: 'managed',
  gatewayName: '',
  gatewayNamespace: '',
};

/**
 * Admin → Tenant routes (org-scoped).
 *
 * A tenant route pairs the org's tenant identity with a rotatable, unguessable
 * path segment that the receiver-tier gateway renders into a Gateway API
 * HTTPRoute. This page is the storage/lifecycle surface: create, rotate (issue
 * a new segment and run both briefly), and revoke. It does not apply anything
 * to Kubernetes — that is the receiver tier. Reads are org-reader; writes are
 * org-admin (useCanAdminister), matching TenantRouteService.
 *
 * The segment is always minted server-side; the URL is an identifier, not an
 * authorizer — Shepherd enforces the CORS origin allowlist and rotation, while
 * request rate limiting belongs at the gateway/ingress.
 */
export function TenantRoutesPage() {
  const orgId = useOrgId();
  const canAdminister = useCanAdminister();
  const qc = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [createForm, setCreateForm] = useState(EMPTY_CREATE);
  const [toRotate, setToRotate] = useState<TenantRoute | null>(null);
  const [overlapHours, setOverlapHours] = useState('24');
  const [toRevoke, setToRevoke] = useState<TenantRoute | null>(null);

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['tenant-routes', orgId],
    queryFn: () => clients.tenantRoute.listTenantRoutes({ orgId }),
    enabled: !!orgId,
  });
  const invalidate = () => qc.invalidateQueries({ queryKey: ['tenant-routes', orgId] });

  const createMut = useMutation({
    mutationFn: () =>
      clients.tenantRoute.createTenantRoute({
        orgId,
        kind: createForm.kind,
        format: createForm.format,
        gatewayMode: createForm.gatewayMode,
        gatewayName: createForm.gatewayMode === 'operator' ? createForm.gatewayName.trim() : '',
        gatewayNamespace:
          createForm.gatewayMode === 'operator' ? createForm.gatewayNamespace.trim() : '',
      }),
    onSuccess: () => {
      toast.success('Tenant route created');
      invalidate();
      setShowCreate(false);
      setCreateForm(EMPTY_CREATE);
    },
    onError: (e) => {
      const err = toApiError(e);
      toast.error(
        err.code === 'failed_precondition'
          ? 'This organisation has no tenant identity yet — an app admin sets it on the Organisations page.'
          : err.message || 'Failed to create tenant route',
      );
    },
  });

  const rotateMut = useMutation({
    mutationFn: () => {
      const hours = Number(overlapHours);
      return clients.tenantRoute.rotateTenantRoute({
        orgId,
        id: toRotate?.id ?? '',
        format: '',
        overlapSeconds: Number.isFinite(hours) && hours > 0 ? Math.round(hours * 3600) : 0,
      });
    },
    onSuccess: () => {
      toast.success('Route rotated — the previous segment keeps routing during the overlap window');
      invalidate();
      setToRotate(null);
      setOverlapHours('24');
    },
    onError: (e) => toast.error(toApiError(e).message || 'Failed to rotate route'),
  });

  const revokeMut = useMutation({
    mutationFn: () => clients.tenantRoute.revokeTenantRoute({ orgId, id: toRevoke?.id ?? '' }),
    onSuccess: () => {
      toast.success('Route revoked');
      invalidate();
      setToRevoke(null);
    },
    onError: (e) => toast.error(toApiError(e).message || 'Failed to revoke route'),
  });

  return (
    <div className='space-y-4' data-testid='tenant-routes'>
      <div className='flex items-start justify-between gap-4'>
        <div>
          <h1 className='text-xl font-semibold'>Tenant routes</h1>
          <p className='mt-1 text-sm text-muted'>
            Rotatable ingress paths for this organisation&rsquo;s telemetry. The segment is an
            identifier, not an authorizer: rate limiting belongs at your gateway or ingress.
          </p>
        </div>
        {!!orgId && canAdminister && (
          <button
            onClick={() => setShowCreate(true)}
            data-testid='route-new'
            className='flex shrink-0 items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500'
          >
            <Plus size={14} /> New route
          </button>
        )}
      </div>

      {!orgId ? (
        <p className='text-sm text-muted'>No organisation context.</p>
      ) : isError ? (
        <QueryError error={error} noun='tenant routes' />
      ) : isLoading ? (
        <p className='text-sm text-muted'>Loading…</p>
      ) : (data?.items ?? []).length === 0 ? (
        <div className='rounded-lg border border-border bg-card/40 p-8 text-center'>
          <p className='text-sm text-muted'>No tenant routes yet.</p>
        </div>
      ) : (
        <DataTable
          columns={routeColumns(canAdminister, setToRotate, setToRevoke)}
          rows={data?.items ?? []}
          rowKey={(r) => r.id}
        />
      )}

      {showCreate && (
        <Modal title='New tenant route' onClose={() => setShowCreate(false)}>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              createMut.mutate();
            }}
            className='space-y-4'
          >
            <Field label='Kind' hint='The telemetry protocol this route accepts.'>
              <Select
                value={createForm.kind}
                onChange={(e) => setCreateForm((f) => ({ ...f, kind: e.target.value }))}
                data-testid='route-kind'
              >
                <option value='otlp'>OTLP</option>
                <option value='faro'>Faro</option>
              </Select>
            </Field>
            <Field label='Segment format' hint='How the unguessable path segment is generated.'>
              <Select
                value={createForm.format}
                onChange={(e) => setCreateForm((f) => ({ ...f, format: e.target.value }))}
                data-testid='route-format'
              >
                <option value='slug-suffix'>Slug + suffix (default)</option>
                <option value='opaque'>Opaque</option>
              </Select>
            </Field>
            <Field
              label='Gateway mode'
              hint='Whether Shepherd owns the Gateway or attaches to yours.'
            >
              <Select
                value={createForm.gatewayMode}
                onChange={(e) => setCreateForm((f) => ({ ...f, gatewayMode: e.target.value }))}
                data-testid='route-gateway-mode'
              >
                <option value='managed'>Managed (Shepherd owns the Gateway)</option>
                <option value='operator'>Operator (attach to an existing Gateway)</option>
              </Select>
            </Field>
            {createForm.gatewayMode === 'operator' && (
              <>
                <Field label='Gateway name'>
                  <Input
                    value={createForm.gatewayName}
                    onChange={(e) => setCreateForm((f) => ({ ...f, gatewayName: e.target.value }))}
                    required
                    data-testid='route-gateway-name'
                    placeholder='shared-gateway'
                  />
                </Field>
                <Field
                  label='Gateway namespace'
                  optional
                  hint='Empty means the route&rsquo;s own namespace.'
                >
                  <Input
                    value={createForm.gatewayNamespace}
                    onChange={(e) =>
                      setCreateForm((f) => ({ ...f, gatewayNamespace: e.target.value }))
                    }
                    data-testid='route-gateway-namespace'
                    placeholder='gateway-system'
                  />
                </Field>
              </>
            )}
            <ModalActions
              onCancel={() => setShowCreate(false)}
              submitLabel='Create route'
              pendingLabel='Creating…'
              pending={createMut.isPending}
            />
          </form>
        </Modal>
      )}

      {toRotate && (
        <Modal
          title={`Rotate /${toRotate.kind}/${toRotate.segment}`}
          onClose={() => setToRotate(null)}
        >
          <form
            onSubmit={(e) => {
              e.preventDefault();
              rotateMut.mutate();
            }}
            className='space-y-4'
          >
            <p className='text-sm text-muted'>
              A new segment is issued immediately. The current one keeps routing for the overlap
              window below, then must be revoked.
            </p>
            <Field
              label='Overlap (hours)'
              hint='How long the old segment keeps routing after rotation.'
            >
              <Input
                type='number'
                min='0'
                value={overlapHours}
                onChange={(e) => setOverlapHours(e.target.value)}
                data-testid='route-overlap-hours'
              />
            </Field>
            <ModalActions
              onCancel={() => setToRotate(null)}
              submitLabel='Rotate'
              pendingLabel='Rotating…'
              pending={rotateMut.isPending}
            />
          </form>
        </Modal>
      )}

      {toRevoke && (
        <AdminConfirmDialog
          title='Revoke tenant route'
          body={`Revoke /${toRevoke.kind}/${toRevoke.segment}? Traffic to this segment stops immediately and cannot be restored — mint a new route instead.`}
          confirmLabel='Revoke'
          pendingLabel='Revoking…'
          pending={revokeMut.isPending}
          onConfirm={() => revokeMut.mutate()}
          onCancel={() => setToRevoke(null)}
        />
      )}
    </div>
  );
}
