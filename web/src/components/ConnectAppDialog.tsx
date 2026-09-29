import { useMutation } from '@tanstack/react-query';
import { Check, Copy } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { Field, Input, Select } from '@/components/ui/Field';
import { Modal } from '@/components/ui/Modal';
import type { RenderConnectAppResponse, TenantRoute } from '@/gen/shepherd/mgmt/v1/tenant_route_pb';

// The artifacts RenderConnectApp returns, in the order a service owner is
// most likely to want them. Every one is derived server-side from the same
// endpoint the route's HTTPRoute matches (internal/onboarding), so nothing
// here builds a URL.
const ARTIFACTS: { key: keyof RenderConnectAppResponse; label: string }[] = [
  { key: 'env', label: '.env' },
  { key: 'k8s', label: 'Kubernetes' },
  { key: 'lambda', label: 'Lambda' },
  { key: 'terraform', label: 'Terraform' },
  { key: 'sam', label: 'SAM' },
  { key: 'cdk', label: 'CDK' },
  { key: 'sdkNotes', label: 'SDK notes' },
];

function CopyButton({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type='button'
      aria-label={`Copy ${label}`}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          toast.error('Copy failed — select and copy the text manually');
        }
      }}
      className='flex items-center gap-1 rounded border border-border px-2 py-1 text-xs text-muted hover:text-zinc-200'
    >
      {copied ? <Check size={12} /> : <Copy size={12} />} {copied ? 'Copied' : 'Copy'}
    </button>
  );
}

/**
 * "Connect an app" for one active OTLP tenant route: the endpoint and the
 * env-var / IaC snippets that point an OpenTelemetry SDK at it. Read-only —
 * any org reader may render them.
 */
export function ConnectAppDialog({
  route,
  orgId,
  onClose,
}: {
  route: TenantRoute;
  orgId: string;
  onClose: () => void;
}) {
  const [form, setForm] = useState({
    serviceName: '',
    gatewayBaseUrl: '',
    protocol: 'http/protobuf',
    includeAdotLayer: false,
  });
  const [tab, setTab] = useState<keyof RenderConnectAppResponse>('env');

  const render = useMutation({
    mutationFn: () =>
      clients.tenantRoute.renderConnectApp({
        orgId,
        id: route.id,
        serviceName: form.serviceName.trim(),
        gatewayBaseUrl: form.gatewayBaseUrl.trim(),
        protocol: form.protocol,
        includeAdotLayer: form.includeAdotLayer,
      }),
    onSuccess: (res) => {
      // Show the URL the server actually used, so a configured default is
      // visible (and editable) rather than implied.
      setForm((f) => ({ ...f, gatewayBaseUrl: res.gatewayBaseUrl }));
    },
  });
  const result = render.data;
  const error = render.error ? toApiError(render.error) : null;
  const artifact = result ? String(result[tab] ?? '') : '';

  return (
    <Modal title={`Connect an app to /otlp/${route.segment}`} onClose={onClose} size='xl'>
      <div className='space-y-4' data-testid='connect-app'>
        {route.applyStatus !== 'applied' && (
          <p
            className='rounded-md bg-amber-500/10 px-3 py-2 text-xs text-amber-400'
            data-testid='connect-app-not-applied'
          >
            This route is not applied to the cluster yet ({route.applyStatus || 'pending'}), so
            telemetry sent to it will not arrive until it is.
          </p>
        )}
        <form
          onSubmit={(e) => {
            e.preventDefault();
            render.mutate();
          }}
          className='grid gap-3 sm:grid-cols-2'
        >
          <Field label='Service name' hint='Becomes OTEL_SERVICE_NAME.'>
            <Input
              id='connect-app-service'
              value={form.serviceName}
              onChange={(e) => setForm((f) => ({ ...f, serviceName: e.target.value }))}
              required
              placeholder='checkout'
              data-testid='connect-app-service'
            />
          </Field>
          <Field
            label='Gateway URL'
            optional
            hint='Your gateway’s public https address. Empty uses the one your operator configured.'
          >
            <Input
              id='connect-app-gateway'
              value={form.gatewayBaseUrl}
              onChange={(e) => setForm((f) => ({ ...f, gatewayBaseUrl: e.target.value }))}
              placeholder='https://telemetry.example.com'
              data-testid='connect-app-gateway'
            />
          </Field>
          <Field label='Protocol' hint='OTLP over HTTP. gRPC cannot carry the tenant path.'>
            <Select
              id='connect-app-protocol'
              value={form.protocol}
              onChange={(e) => setForm((f) => ({ ...f, protocol: e.target.value }))}
              data-testid='connect-app-protocol'
            >
              <option value='http/protobuf'>http/protobuf (recommended)</option>
              <option value='http/json'>http/json</option>
            </Select>
          </Field>
          <label className='flex items-center gap-2 self-end pb-2 text-sm text-muted'>
            <input
              id='connect-app-adot'
              type='checkbox'
              checked={form.includeAdotLayer}
              onChange={(e) => setForm((f) => ({ ...f, includeAdotLayer: e.target.checked }))}
              data-testid='connect-app-adot'
            />
            Add the AWS Lambda OpenTelemetry layer (you supply its ARN)
          </label>
          <div className='sm:col-span-2 flex justify-end'>
            <button
              type='submit'
              disabled={render.isPending}
              data-testid='connect-app-render'
              className='rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500 disabled:opacity-60'
            >
              {render.isPending ? 'Generating…' : result ? 'Regenerate' : 'Generate snippets'}
            </button>
          </div>
        </form>

        {error && (
          <p className='text-sm text-red-400' role='alert' data-testid='connect-app-error'>
            {error.message}
          </p>
        )}

        {result && (
          <div className='space-y-3'>
            <div className='flex flex-wrap items-center gap-2 text-sm'>
              <span className='text-muted'>Endpoint</span>
              <code
                className='break-all rounded bg-card px-2 py-1 font-mono text-xs'
                data-testid='connect-app-endpoint'
              >
                {result.baseEndpoint}
              </code>
              <CopyButton text={result.baseEndpoint} label='endpoint' />
            </div>
            <div role='tablist' className='flex flex-wrap gap-1 border-b border-border'>
              {ARTIFACTS.map((a) => (
                <button
                  key={a.key}
                  type='button'
                  role='tab'
                  aria-selected={tab === a.key}
                  onClick={() => setTab(a.key)}
                  data-testid={`connect-app-tab-${a.key}`}
                  className={`-mb-px border-b-2 px-3 py-1.5 text-xs ${
                    tab === a.key
                      ? 'border-indigo-500 text-zinc-100'
                      : 'border-transparent text-muted hover:text-zinc-200'
                  }`}
                >
                  {a.label}
                </button>
              ))}
            </div>
            <div className='relative'>
              <div className='absolute right-2 top-2'>
                <CopyButton text={artifact} label={tab} />
              </div>
              <pre
                className='max-h-96 overflow-auto rounded-md bg-card p-3 pr-20 font-mono text-xs'
                data-testid='connect-app-artifact'
              >
                {artifact}
              </pre>
            </div>
          </div>
        )}
      </div>
    </Modal>
  );
}
