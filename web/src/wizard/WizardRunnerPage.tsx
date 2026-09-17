import type { JsonObject } from '@bufbuild/protobuf';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate, useParams } from '@tanstack/react-router';
import { CheckCircle2, XCircle } from 'lucide-react';
import { useEffect, useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { Input } from '@/components/ui/Field';
import { AlloyEditor } from '@/editor/LazyAlloyEditor';
import type { MatchedCollector } from '@/gen/shepherd/mgmt/v1/pipeline_pb';
import { useOrgId } from '@/hooks/useOrg';
import {
  defaultFieldValue,
  isStepValid,
  type WizardFormState,
  WizardStepFields,
} from './WizardStepFields';
import { WizardStepper } from './WizardStepper';

function slugify(s: string): string {
  return (
    s
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '') || 'app'
  );
}

/** Pulls the quoted value out of a Prometheus-style label matcher, e.g.
 * `cluster=~"prod-.*"` -> "prod-.*", `role="singleton"` -> "singleton". */
function matcherValue(matcher: string): string | undefined {
  return /=~?"([^"]*)"/.exec(matcher)?.[1];
}

/** A matcher chip is flagged as wizard-added when its quoted value isn't
 * anything the user typed or picked on the form -- e.g. self-monitoring's
 * `role="singleton"` appended after the user's own cluster_pattern
 * (wizard.go:180-184). A value the user *did* enter (job_name feeding a
 * matcher, or a select they chose) matches one of the form's own string
 * values and stays unlabelled. */
function isWizardAddedMatcher(matcher: string, form: WizardFormState): boolean {
  const value = matcherValue(matcher);
  if (value === undefined) return false;
  return !Object.values(form).some((v) => typeof v === 'string' && v === value);
}

// One page for every wizard. The backend already returns a full schema —
// steps, fields, types, options — so a wizard needs no bespoke page; it needs
// this page pointed at its kind. It was written against a hardcoded
// 'app-observability' and a hardcoded catalog, which is why five wizards that
// existed in the registry were unreachable in the UI.
export function WizardRunnerPage() {
  const orgId = useOrgId();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { kind } = useParams({ from: '/shell/content/wizards/$kind' });
  const KIND = kind;

  const {
    data: schema,
    isLoading: schemaLoading,
    error: schemaError,
  } = useQuery({
    queryKey: ['wizard-schema', orgId, KIND],
    queryFn: () => clients.wizard.getWizardSchema({ orgId, kind: KIND }),
    enabled: !!orgId,
    // An unknown kind is a 404 and will stay one — retrying it only delays
    // the message. Without this the query sits pending through the retry
    // cycle and the page shows "Loading…" for a kind that does not exist.
    retry: false,
  });

  // Lets a *_dest_name field render as a picker of the org's real
  // destinations (F11) instead of free text.
  const { data: destinationsData } = useQuery({
    queryKey: ['destinations', orgId],
    queryFn: () => clients.destination.listDestinations({ orgId }),
    enabled: !!orgId,
  });
  const destinations = destinationsData?.items ?? [];

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
    // Suggest "<kind>-<job>" rather than a per-wizard literal, so a new
    // wizard gets a sensible default without touching this file.
    if (typeof jobName === 'string' && jobName) setName(`${slugify(KIND)}-${slugify(jobName)}`);
  }, [form.job_name, nameTouched, KIND]);

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
      qc.invalidateQueries({ queryKey: ['pipelines', orgId] });
      navigate({ to: '/pipelines/$id', params: { id: pipeline.id } });
    },
    onError: (e) => {
      const err = toApiError(e);
      toast.error(err.message || 'Failed to create pipeline');
    },
  });

  if (!orgId) return <p className='text-sm text-muted'>No organisation context.</p>;
  // An unknown kind used to sit on "Loading…" forever: the query had failed,
  // schema stayed undefined, and the two states were rendered identically.
  // Distinguish them — a mistyped or removed wizard should say so.
  // Covers the error case AND any settled-but-empty result: once the query is
  // no longer loading, no schema means no such wizard. Keying only on `error`
  // left a gap where a non-throwing failure still rendered "Loading…".
  if (schemaError || (!schemaLoading && !schema)) {
    return (
      <div className='space-y-2'>
        <h1 className='text-xl font-semibold'>Wizard not available</h1>
        <p className='text-sm text-muted'>
          No wizard named <span className='font-mono'>{KIND}</span> is registered on this server.
        </p>
        <Link to='/wizards' className='text-sm text-indigo-400 hover:text-indigo-300'>
          Back to wizards
        </Link>
      </div>
    );
  }
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
                  destinations={destinations}
                />
              </>
            ) : (
              <div className='space-y-4'>
                <h2 className='text-sm font-semibold text-zinc-100'>Review</h2>
                <label className='block text-xs font-medium text-muted'>
                  Pipeline name
                  <Input
                    mono
                    value={name}
                    onChange={(e) => {
                      setName(e.target.value);
                      setNameTouched(true);
                    }}
                    className='mt-1 focus:outline-none focus:ring-1 focus:ring-indigo-500'
                    placeholder={`${KIND}-my-app`}
                  />
                </label>

                {renderQuery.data && (
                  <>
                    <div className='space-y-1'>
                      <p className='text-xs font-medium text-muted'>Matchers</p>
                      <div className='flex flex-wrap items-center gap-1.5'>
                        {(renderQuery.data.matchers ?? []).map((m) => (
                          <span key={m} className='inline-flex items-center gap-1'>
                            <span className='rounded bg-border px-2 py-0.5 font-mono text-xs text-zinc-200'>
                              {m}
                            </span>
                            {isWizardAddedMatcher(m, form) && (
                              <span
                                data-testid='wizard-added-matcher'
                                className='rounded bg-accent/10 px-1.5 py-0.5 text-xs text-accent'
                              >
                                added by the wizard
                              </span>
                            )}
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

                    {(renderQuery.data.warnings ?? []).length > 0 && (
                      <div
                        className='rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-300'
                        data-testid='wizard-warnings'
                      >
                        <ul className='space-y-1'>
                          {(renderQuery.data.warnings ?? []).map((w) => (
                            <li key={w}>{w}</li>
                          ))}
                        </ul>
                      </div>
                    )}

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
