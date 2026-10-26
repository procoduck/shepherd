import { type Timestamp, timestampDate } from '@bufbuild/protobuf/wkt';
import { Link } from '@tanstack/react-router';
import { toneClass } from '@/components/ui/statusTone';
import { useCanAdminister } from '@/hooks/useOrg';
import { formatTimestampRelative } from '@/lib/utils';

// A collector the agent API is refusing (migration 0041). A refused poll writes
// no instance row, so the instance status (APPLIED, …) and last_seen still
// describe the last poll that passed. Found by the 0.22 -> 0.23 upgrade
// walkthrough: a fleet whose every poll was refused (unbound token) read
// APPLIED / "1m ago".
//
// The server carries the latest refusal for agent.refusal_visible_for (15m by
// default) whether or not a poll passed since (0.20 walkthrough): one pod with
// credentials for another cluster, beside healthy pods of the same (cluster,
// role), used to vanish behind them. refusalState tells the two cases apart.

/** The refusal reasons the server reports (internal/metrics AgentRefused*). */
export type RefusalReason = 'token_unbound' | 'claim_denied' | 'role_denied';

/** The headline for a refusal reason; unknown reasons still read as refused. */
export function refusalMessage(reason: string): string {
  switch (reason) {
    case 'token_unbound':
      return 'Refused — agent token not bound to an organisation';
    case 'claim_denied':
      return 'Refused — cluster not allowed';
    case 'role_denied':
      return 'Refused — role not allowed';
    default:
      return 'Refused';
  }
}

/** The reason alone, lower-case, for a sentence about one refused instance. */
function refusalReasonText(reason: string): string {
  switch (reason) {
    case 'token_unbound':
      return 'agent token not bound to an organisation';
    case 'claim_denied':
      return 'cluster not allowed';
    case 'role_denied':
      return 'role not allowed';
    default:
      return 'refused on an identity check';
  }
}

/** What the operator does about it, one sentence per reason. */
function refusalHint(reason: string): string {
  switch (reason) {
    case 'token_unbound':
      return 'Every poll from this collector is refused until an app admin binds its agent token to an organisation. Binding keeps the secret, so the collector needs no new credentials.';
    case 'claim_denied':
      return "The collector's credentials do not permit this cluster: it is outside the token's cluster allowlist, claimed by another organisation, or unclaimed and the token may not claim it.";
    case 'role_denied':
      return "The collector's credentials do not permit this role: it is outside the token's role allowlist.";
    default:
      return 'Shepherd refused this collector on an identity check.';
  }
}

interface Refusable {
  lastRefusedReason: string;
  lastRefusedAt?: Timestamp;
  lastSeen?: Timestamp;
}

/**
 * How a collector's recorded refusal reads. `refused`: no poll has passed
 * since it (last_seen is the newest passing poll, as a refused poll writes no
 * instance row), so REFUSED is the collector's status. `sibling`: a poll passed
 * after it, so another instance is healthy; the status stays the instance
 * status and the refusal is a warning beside it.
 */
export function refusalState(c: Refusable): 'none' | 'refused' | 'sibling' {
  if (!c.lastRefusedReason) return 'none';
  if (!c.lastSeen || !c.lastRefusedAt) return 'refused';
  return timestampDate(c.lastRefusedAt) > timestampDate(c.lastSeen) ? 'refused' : 'sibling';
}

/** The one-line warning for a refused instance beside a healthy one. */
export function siblingRefusalText(c: Refusable): string {
  return `An instance using this collector was refused ${formatTimestampRelative(c.lastRefusedAt)}: ${refusalReasonText(c.lastRefusedReason)}`;
}

/** The Collectors list's marker beside a healthy status; renders nothing otherwise. */
export function SiblingRefusalBadge({ collector }: { collector: Refusable }) {
  if (refusalState(collector) !== 'sibling') return null;
  return (
    <span
      data-testid='collector-sibling-refused-badge'
      title={siblingRefusalText(collector)}
      className={`ml-2 text-xs font-medium px-1.5 py-0.5 rounded border ${toneClass('warn')}`}
    >
      INSTANCE REFUSED
    </span>
  );
}

/**
 * The status badge for a refused collector (refusalState === 'refused'); the
 * caller renders its instance-status badge otherwise.
 */
export function RefusedBadge({ collector, testId }: { collector: Refusable; testId?: string }) {
  return (
    <span
      data-testid={testId ?? 'collector-refused-badge'}
      title={`${refusalMessage(collector.lastRefusedReason)} (last refused ${formatTimestampRelative(collector.lastRefusedAt)})`}
      className={`text-xs font-medium px-2 py-0.5 rounded border ${toneClass('danger')}`}
    >
      REFUSED
    </span>
  );
}

/** The collector page's explanation of a refusal; renders nothing without one. */
export function CollectorRefusedBanner({ collector }: { collector: Refusable }) {
  const canAdminister = useCanAdminister();
  const reason = collector.lastRefusedReason;
  const state = refusalState(collector);
  if (state === 'none') return null;
  if (state === 'sibling') {
    return (
      <div
        className={`rounded-lg border px-4 py-3 text-sm ${toneClass('warn')}`}
        data-testid='collector-sibling-refused'
      >
        <p className='font-medium'>{siblingRefusalText(collector)}</p>
        <p className='mt-1'>
          {refusalHint(reason)} Other instances of this collector are polling successfully, so the
          status shown is theirs. This warning clears once no instance has been refused for a while.
        </p>
      </div>
    );
  }
  return (
    <div
      className={`rounded-lg border px-4 py-3 text-sm ${toneClass('danger')}`}
      data-testid='collector-refused'
    >
      <p className='font-medium'>{refusalMessage(reason)}</p>
      <p className='mt-1'>
        {refusalHint(reason)} Last refused {formatTimestampRelative(collector.lastRefusedAt)}; the
        status and last-seen time shown are from its last successful poll.
        {reason === 'token_unbound' &&
          (canAdminister ? (
            <>
              {' '}
              See{' '}
              <Link to='/admin/tokens' className='underline'>
                Agent tokens
              </Link>
              .
            </>
          ) : (
            ' Ask an admin to check Agent tokens.'
          ))}
      </p>
    </div>
  );
}
