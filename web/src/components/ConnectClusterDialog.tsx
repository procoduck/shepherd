import { useMutation } from '@tanstack/react-query';
import { useState } from 'react';
import { clients, toApiError } from '@/api/transport';
import { CopyButton } from '@/components/ui/CopyButton';
import { Field, Input } from '@/components/ui/Field';
import { Modal } from '@/components/ui/Modal';

// The four collector roles k8s-monitoring deploys, as internal/signals names
// them. The server refuses any other.
const ROLES = [
  { id: 'metrics', label: 'Metrics', hint: 'alloy-metrics' },
  { id: 'logs', label: 'Logs', hint: 'alloy-logs' },
  { id: 'singleton', label: 'Singleton', hint: 'alloy-singleton (cluster events, one replica)' },
  { id: 'receiver', label: 'Receiver', hint: 'alloy-receiver (OTLP from apps)' },
];

// What the cluster needs from an app admin before its collectors land in
// this org's fleet, by the status the server reported.
const CLUSTER_STATUS: Record<string, string> = {
  new: 'Shepherd has not seen this cluster yet. Once its collectors register, an app admin claims it for this organisation on Admin → Clusters.',
  unclaimed:
    'This cluster has registered but is not claimed yet. An app admin claims it for this organisation on Admin → Clusters.',
  claimed: 'This cluster already belongs to this organisation.',
};

function Block({ title, text, testId }: { title: string; text: string; testId: string }) {
  return (
    <div className='space-y-1'>
      <div className='flex items-center justify-between'>
        <h3 className='text-xs font-medium text-muted'>{title}</h3>
        <CopyButton text={text} label={title} />
      </div>
      <pre
        className='max-h-72 overflow-auto rounded-md bg-card p-3 font-mono text-xs'
        data-testid={testId}
      >
        {text}
      </pre>
    </div>
  );
}

function download(filename: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: 'application/yaml' }));
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}

/**
 * "Connect a cluster": the Helm values that point a cluster's Grafana
 * k8s-monitoring collectors at this Shepherd (internal/chartvalues), plus the
 * Secret and helm commands that use them. Org-admin; nothing is written.
 */
export function ConnectClusterDialog({ orgId, onClose }: { orgId: string; onClose: () => void }) {
  const [form, setForm] = useState({
    clusterName: '',
    namespace: 'monitoring',
    roles: ['metrics', 'logs', 'singleton'] as string[],
  });
  const render = useMutation({
    mutationFn: () =>
      clients.fleet.renderChartValues({
        orgId,
        clusterName: form.clusterName.trim(),
        namespace: form.namespace.trim(),
        roles: form.roles,
      }),
  });
  const result = render.data;
  const error = render.error ? toApiError(render.error) : null;
  const toggleRole = (id: string) =>
    setForm((f) => ({
      ...f,
      roles: f.roles.includes(id) ? f.roles.filter((r) => r !== id) : [...f.roles, id],
    }));

  return (
    <Modal title='Connect a cluster' onClose={onClose} size='xl'>
      <div className='space-y-4' data-testid='connect-cluster'>
        <p className='text-sm text-muted'>
          Generates values for Grafana&rsquo;s k8s-monitoring Helm chart that make the
          cluster&rsquo;s Alloy collectors take their configuration from Shepherd. Use them
          alongside your own values file; they set nothing else.
        </p>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            render.mutate();
          }}
          className='grid gap-3 sm:grid-cols-2'
        >
          <Field label='Cluster name' hint='Becomes cluster.name; matchers see it as "cluster".'>
            <Input
              id='connect-cluster-name'
              value={form.clusterName}
              onChange={(e) => setForm((f) => ({ ...f, clusterName: e.target.value }))}
              required
              placeholder='prod-eu-1'
              data-testid='connect-cluster-name'
            />
          </Field>
          <Field label='Namespace' hint='Where k8s-monitoring runs; used in the commands only.'>
            <Input
              id='connect-cluster-namespace'
              value={form.namespace}
              onChange={(e) => setForm((f) => ({ ...f, namespace: e.target.value }))}
              data-testid='connect-cluster-namespace'
            />
          </Field>
          <fieldset className='sm:col-span-2 space-y-1'>
            <legend className='text-xs font-medium text-muted'>Collectors</legend>
            <div className='flex flex-wrap gap-x-5 gap-y-1'>
              {ROLES.map((r) => (
                <label key={r.id} className='flex items-center gap-2 text-sm' title={r.hint}>
                  <input
                    id={`connect-cluster-role-${r.id}`}
                    type='checkbox'
                    checked={form.roles.includes(r.id)}
                    onChange={() => toggleRole(r.id)}
                    data-testid={`connect-cluster-role-${r.id}`}
                  />
                  {r.label}
                </label>
              ))}
            </div>
          </fieldset>
          <div className='sm:col-span-2 flex justify-end'>
            <button
              type='submit'
              disabled={render.isPending || form.roles.length === 0}
              data-testid='connect-cluster-render'
              className='rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500 disabled:opacity-60'
            >
              {render.isPending ? 'Generating…' : result ? 'Regenerate' : 'Generate values'}
            </button>
          </div>
        </form>

        {error && (
          <p className='text-sm text-red-400' role='alert' data-testid='connect-cluster-error'>
            {error.message}
          </p>
        )}

        {result && (
          <div className='space-y-4'>
            <p
              className='rounded-md bg-indigo-500/10 px-3 py-2 text-xs text-indigo-300'
              data-testid='connect-cluster-status'
            >
              {CLUSTER_STATUS[result.clusterStatus] ?? result.clusterStatus} Collectors authenticate
              with an agent token, which an app admin creates on Admin → Agent Tokens.
            </p>
            <Block
              title='1. Store the agent token'
              text={result.secretCommand}
              testId='connect-cluster-secret-command'
            />
            <div className='space-y-1'>
              <div className='flex items-center justify-between gap-2'>
                <h3 className='text-xs font-medium text-muted'>
                  2. Values files (k8s-monitoring {result.chartVersion}, polling{' '}
                  {result.shepherdUrl})
                </h3>
                <span className='flex gap-2'>
                  <button
                    type='button'
                    onClick={() => download('shepherd-layer.yaml', result.valuesYaml)}
                    data-testid='connect-cluster-download-values'
                    className='rounded border border-border px-2 py-1 text-xs text-muted hover:text-zinc-200'
                  >
                    Download shepherd-layer.yaml
                  </button>
                  <button
                    type='button'
                    onClick={() => download('shepherd-credentials.yaml', result.credentialsYaml)}
                    className='rounded border border-border px-2 py-1 text-xs text-muted hover:text-zinc-200'
                  >
                    Download shepherd-credentials.yaml
                  </button>
                </span>
              </div>
            </div>
            <Block
              title='shepherd-layer.yaml'
              text={result.valuesYaml}
              testId='connect-cluster-values'
            />
            <Block
              title='shepherd-credentials.yaml'
              text={result.credentialsYaml}
              testId='connect-cluster-credentials'
            />
            <Block
              title='3. Install or upgrade'
              text={result.helmCommand}
              testId='connect-cluster-helm'
            />
          </div>
        )}
      </div>
    </Modal>
  );
}
