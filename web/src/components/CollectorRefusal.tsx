import type { Timestamp } from '@bufbuild/protobuf/wkt';
import { Link } from '@tanstack/react-router';
import { toneClass } from '@/components/ui/statusTone';
import { useCanAdminister } from '@/hooks/useOrg';
import { formatTimestampRelative } from '@/lib/utils';

// A collector the agent API is refusing (migration 0041). A refused poll writes
// no instance row, so the instance status (APPLIED, …) and last_seen still
// describe the last poll that passed; the server sets last_refused_reason only
// while the latest refusal is newer than that, and the UI shows it in place of
// the instance status. Found by the 0.22 -> 0.23 upgrade walkthrough: a fleet
// whose every poll was refused (unbound token) read APPLIED / "1m ago".

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
}

/**
 * The status badge for a collector: REFUSED while the server reports a
 * refusal, otherwise the caller's instance-status badge.
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
  if (!reason) return null;
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
