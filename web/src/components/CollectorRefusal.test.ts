import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import { describe, expect, it } from 'vitest';
import { refusalState, siblingRefusalText } from './CollectorRefusal';

const at = (msAgo: number) => timestampFromDate(new Date(Date.now() - msAgo));

// 0.20 walkthrough: a refusal is kept for agent.refusal_visible_for even while
// healthy instances of the same collector poll. It is the collector's state
// (REFUSED) only when no poll passed since; otherwise it is a warning beside
// the healthy status.
describe('refusalState', () => {
  it('is none without a refusal', () => {
    expect(refusalState({ lastRefusedReason: '', lastSeen: at(0) })).toBe('none');
  });

  it('is refused when no instance ever passed', () => {
    expect(refusalState({ lastRefusedReason: 'token_unbound', lastRefusedAt: at(30_000) })).toBe(
      'refused',
    );
  });

  it('is refused when the refusal is newer than the last passing poll', () => {
    expect(
      refusalState({
        lastRefusedReason: 'claim_denied',
        lastRefusedAt: at(30_000),
        lastSeen: at(120_000),
      }),
    ).toBe('refused');
  });

  it('is a sibling warning when a poll passed after the refusal', () => {
    expect(
      refusalState({
        lastRefusedReason: 'claim_denied',
        lastRefusedAt: at(180_000),
        lastSeen: at(5_000),
      }),
    ).toBe('sibling');
  });
});

describe('siblingRefusalText', () => {
  it('names when and why an instance was refused', () => {
    expect(
      siblingRefusalText({ lastRefusedReason: 'claim_denied', lastRefusedAt: at(3 * 60_000) }),
    ).toBe('An instance using this collector was refused 3m ago: cluster not allowed');
  });
});
