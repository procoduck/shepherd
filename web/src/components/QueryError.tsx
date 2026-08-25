import { toApiError } from '@/api/transport';

/**
 * The failed-to-load state for a list.
 *
 * Exists because several pages had no error branch at all and rendered a failed
 * query as their empty state — "No audit entries yet.", "Add your first
 * credential". A viewer denied the audit log was told nothing had happened, and
 * a non-admin was invited to create a credential they cannot create. An empty
 * state is a claim about the data; only say it when the server actually
 * answered.
 *
 * Permission denials get their own wording, because "retry" is useless advice
 * for a request that will always be refused.
 */
export function QueryError({ error, noun }: { error: unknown; noun: string }) {
  const err = toApiError(error);
  const denied = err.code === 'permission_denied' || err.code === 'unauthenticated';
  return (
    <div
      role='alert'
      data-testid='query-error'
      className='rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-400'
    >
      {denied
        ? `You do not have permission to view ${noun} in this organisation.`
        : `Failed to load ${noun}. ${err.message || 'Please retry.'}`}
    </div>
  );
}
