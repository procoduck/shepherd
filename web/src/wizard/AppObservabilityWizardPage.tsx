import type { JsonObject } from '@bufbuild/protobuf';
import { useMutation, useQuery } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { CheckCircle2, XCircle } from 'lucide-react';
import { useEffect, useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { AlloyEditor } from '@/editor/AlloyEditor';
import type { MatchedCollector } from '@/gen/shepherd/mgmt/v1/pipeline_pb';
import { useOrgId } from '@/hooks/useOrg';
import {
  defaultFieldValue,
  isStepValid,
  type WizardFormState,
  WizardStepFields,
} from './WizardStepFields';
import { WizardStepper } from './WizardStepper';

const KIND = 'app-observability';

function slugify(s: string): string {
  return (
    s
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '') || 'app'
  );
}

export function AppObservabilityWizardPage() {
  const orgId = useOrgId();
  const navigate = useNavigate();

  const { data: schema, isLoading: schemaLoading } = useQuery({
    queryKey: ['wizard-schema', orgId, KIND],
    queryFn: () => clients.wizard.getWizardSchema({ orgId, kind: KIND }),
    enabled: !!orgId,
  });

  const dataSteps = schema?.steps ?? [];
  const reviewIndex = dataSteps.length;
  const totalSteps = dataSteps.length + 1;

  const [stepIndex, setStepIndex] = useState(0);
  const [form, setForm] = useState<WizardFormState>({});
  const [name, setName] = useState('');
  const [nameTouched, setNameTouched] = useState(false);

  // Seed defaults from the schema once it loads.
  useEffect(() => {
    if (!schema) return;
    setForm((prev) => {
      const next = { ...prev };
      for (const step of schema.steps) {
        for (const field of step.fields) {
          if (next[field.name] === undefined) {
            const d = defaultFieldValue(field);
            if (d !== undefined) next[field.name] = d;
          }
        }
      }
      return next;
    });
  }, [schema]);

  // Suggest a pipeline name from job_name until the user edits it directly.
  useEffect(() => {
    if (nameTouched) return;
    const jobName = form.job_name;
    if (typeof jobName === 'string' && jobName) setName(`appobs-${slugify(jobName)}`);
  }, [form.job_name, nameTouched]);

  const isReview = stepIndex === reviewIndex;

  const renderQuery = useQuery({
    queryKey: ['wizard-render', orgId, KIND, name, form],
    queryFn: () =>
      clients.wizard.renderWizard({ orgId, kind: KIND, name, state: form as JsonObject }),
    enabled: !!orgId && isReview && !!name,
  });

  const commitMut = useMutation({
    mutationFn: () =>
      clients.wizard.commitWizard({ orgId, kind: KIND, name, state: form as JsonObject }),
    onSuccess: (pipeline) => {
      toast.success('Pipeline created from wizard');
      navigate({ to: '/pipelines/$id', params: { id: pipeline.id } });
    },
    onError: (e) => {
      const err = toApiError(e);
      toast.error(err.message || 'Failed to create pipeline');
    },
  });

  if (!orgId) return <p className='text-sm text-muted'>No organisation context.</p>;
  if (schemaLoading || !schema) return <p className='text-sm text-muted'>Loading…</p>;

  const steppers = [
    ...dataSteps.map((s) => ({ id: s.id, title: s.title })),
    { id: 'review', title: 'Review' },
  ];
  const currentFields = isReview ? [] : dataSteps[stepIndex].fields;
  const canContinue = isReview || isStepValid(currentFields, form);
  const diagnostics = renderQuery.data?.diagnostics ?? [];
  const hasErrors = diagnostics.length > 0;

  return (
    <div className='space-y-4'>
      <div>
        <h1 className='text-xl font-semibold'>{schema.title}</h1>
        <p className='text-sm text-muted' aria-live='polite'>
          Step {stepIndex + 1} of {totalSteps}
        </p>
      </div>

      <div className='flex gap-8'>
        <WizardStepper steps={steppers} activeIndex={stepIndex} />

        <div className='max-w-2xl flex-1 space-y-4'>
          <div className='rounded-lg border border-border bg-card/40 p-6'>
            {!isReview ? (
              <>
                <h2 className='mb-4 text-sm font-semibold text-zinc-100'>
                  {dataSteps[stepIndex].title}
                </h2>
                <WizardStepFields
                  fields={currentFields}
                  state={form}
                  onChange={(fieldName, value) => setForm((f) => ({ ...f, [fieldName]: value }))}
                />
              </>
            ) : (
              <div className='space-y-4'>
                <h2 className='text-sm font-semibold text-zinc-100'>Review</h2>
                <label className='block text-xs font-medium text-muted'>
                  Pipeline name
                  <input
                    value={name}
                    onChange={(e) => {
                      setName(e.target.value);
                      setNameTouched(true);
                    }}
                    className='mt-1 w-full rounded-md border border-border-strong bg-card px-3 py-1.5 text-sm font-mono focus:outline-none focus:ring-1 focus:ring-indigo-500'
                    placeholder='appobs-my-app'
                  />
                </label>

                {renderQuery.data && (
                  <>
                    <div className='space-y-1'>
                      <p className='text-xs font-medium text-muted'>Matchers</p>
                      <div className='flex flex-wrap gap-1.5'>
                        {(renderQuery.data.matchers ?? []).map((m) => (
                          <span
                            key={m}
                            className='rounded bg-border px-2 py-0.5 font-mono text-xs text-zinc-200'
                          >
                            {m}
                          </span>
                        ))}
                      </div>
                      <p className='text-xs text-muted-2' data-testid='wizard-match-preview'>
                        Matches {(renderQuery.data.matchedCollectors ?? []).length} collector
                        {(renderQuery.data.matchedCollectors ?? []).length === 1 ? '' : 's'} in this
                        org.
                      </p>
                      {(renderQuery.data.matchedCollectors ?? []).length > 0 && (
                        <ul className='text-xs text-muted-2'>
                          {(renderQuery.data.matchedCollectors ?? []).map((c: MatchedCollector) => (
                            <li key={c.id}>
                              {c.cluster} / {c.role}
                            </li>
                          ))}
                        </ul>
                      )}
                    </div>

                    <div className='flex items-center gap-1.5 text-xs' aria-live='polite'>
                      {renderQuery.isFetching ? (
                        <span className='text-muted'>Validating…</span>
                      ) : hasErrors ? (
                        <span className='flex items-center gap-1 text-red-400'>
                          <XCircle size={14} /> {diagnostics.length} problem
                          {diagnostics.length > 1 ? 's' : ''}
                        </span>
                      ) : (
                        <span className='flex items-center gap-1 text-emerald-500'>
                          <CheckCircle2 size={14} /> No problems
                        </span>
                      )}
                    </div>

                    <div className='h-64 overflow-hidden rounded-md border border-border'>
                      <AlloyEditor
                        value={renderQuery.data.contents}
                        readOnly
                        diagnostics={diagnostics}
                        height='100%'
                      />
                    </div>
                  </>
                )}
              </div>
            )}
          </div>

          <div className='flex justify-end gap-2'>
            <button
              type='button'
              onClick={() => setStepIndex((i) => Math.max(0, i - 1))}
              disabled={stepIndex === 0}
              className='px-4 py-1.5 text-sm text-muted hover:text-zinc-200 disabled:opacity-40'
            >
              Back
            </button>
            {!isReview ? (
              <button
                type='button'
                onClick={() => setStepIndex((i) => Math.min(reviewIndex, i + 1))}
                disabled={!canContinue}
                className='rounded-md bg-indigo-600 px-4 py-1.5 text-sm text-white hover:bg-indigo-500 disabled:opacity-50'
              >
                Continue
              </button>
            ) : (
              <button
                type='button'
                onClick={() => commitMut.mutate()}
                disabled={!name || hasErrors || commitMut.isPending || renderQuery.isFetching}
                className='rounded-md bg-indigo-600 px-4 py-1.5 text-sm text-white hover:bg-indigo-500 disabled:opacity-50'
              >
                {commitMut.isPending ? 'Creating…' : 'Create pipeline'}
              </button>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
