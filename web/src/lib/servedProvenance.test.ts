import { describe, expect, it } from 'vitest';
import {
  exclusionReasonLabel,
  linesLabel,
  matcherEvidenceText,
  spanContains,
} from './servedProvenance';

describe('served-config provenance helpers (F-CONTRIB)', () => {
  it('labels every exclusion reason the server sends, and falls back for an unknown one', () => {
    expect(exclusionReasonLabel('role')).toBe('Role');
    expect(exclusionReasonLabel('strict_tenant')).toBe('Strict tenant check');
    expect(exclusionReasonLabel('unparsable_matcher')).toBe('Unparsable matcher');
    expect(exclusionReasonLabel('something_new')).toBe('something_new');
    expect(exclusionReasonLabel('')).toBe('Excluded');
  });

  it('phrases matcher evidence with the value the collector has, or says it has none', () => {
    expect(
      matcherEvidenceText({ matcher: 'team=~"a.*"', label: 'team', value: 'alpha', present: true }),
    ).toBe('team is "alpha"');
    expect(
      matcherEvidenceText({ matcher: 'env!="dev"', label: 'env', value: '', present: false }),
    ).toBe('env is not set');
  });

  it('labels a line span and tests membership inclusively', () => {
    expect(linesLabel({ first: 6, last: 12 })).toBe('Lines 6–12');
    expect(linesLabel({ first: 4, last: 4 })).toBe('Line 4');
    expect(linesLabel(undefined)).toBe('—');
    const span = { first: 6, last: 12 };
    expect(spanContains(span, 5)).toBe(false);
    expect(spanContains(span, 6)).toBe(true);
    expect(spanContains(span, 12)).toBe(true);
    expect(spanContains(span, 13)).toBe(false);
    expect(spanContains(undefined, 6)).toBe(false);
  });
});
