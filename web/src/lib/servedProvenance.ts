/**
 * Helpers for a served config's provenance (F-CONTRIB, #423): which
 * pipelines built it, the matcher evidence that selected each, the lines of
 * each one's block, and which matched pipelines were excluded and why. The
 * server records all of it in the same recompute that writes the served
 * bytes; nothing here re-derives it.
 */

/** A 1-based, inclusive line range of the served config's content. */
export interface LineSpanLike {
  first: number;
  last: number;
}

export interface MatcherEvidenceLike {
  matcher: string;
  label: string;
  value: string;
  present: boolean;
}

/** The badge text for an ExcludedPipeline.reason. */
export function exclusionReasonLabel(reason: string): string {
  switch (reason) {
    case 'role':
      return 'Role';
    case 'strict_tenant':
      return 'Strict tenant check';
    case 'unparsable_matcher':
      return 'Unparsable matcher';
    case '':
      return 'Excluded';
    default:
      return reason;
  }
}

/** What the collector had for the label a matcher read. */
export function matcherEvidenceText(m: MatcherEvidenceLike): string {
  return m.present ? `${m.label} is "${m.value}"` : `${m.label} is not set`;
}

export function linesLabel(span: LineSpanLike | undefined): string {
  if (!span || span.first < 1) return '—';
  return span.first === span.last ? `Line ${span.first}` : `Lines ${span.first}–${span.last}`;
}

export function spanContains(span: LineSpanLike | undefined, line: number): boolean {
  return !!span && line >= span.first && line <= span.last;
}
