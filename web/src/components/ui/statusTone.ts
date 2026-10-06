/**
 * Status badge tones (#253). One place maps a semantic tone to its classes so
 * every status pill in the app reads the same and stays on the contrast-checked
 * tokens in index.css (--color-ok/-warn/-danger/-info and their -surface
 * pairs). theme.test.ts computes the WCAG ratio of each pair in both themes.
 *
 * `neutral` is for states with no good/bad reading (pending, removed, unknown):
 * text-muted on bg-border, which clears AA in both themes where the old
 * text-muted-2 on bg-border did not in dark (≈3.2:1).
 */
export type StatusTone = 'ok' | 'warn' | 'danger' | 'info' | 'neutral';

export const STATUS_TONE_CLASS: Record<StatusTone, string> = {
  ok: 'text-ok bg-ok-surface border-ok/30',
  warn: 'text-warn bg-warn-surface border-warn/30',
  danger: 'text-danger bg-danger-surface border-danger/30',
  info: 'text-info bg-info-surface border-info/30',
  neutral: 'text-muted bg-border border-border-strong',
};

/** Classes for a tone, with the badge's shape left to the caller. */
export function toneClass(tone: StatusTone): string {
  return STATUS_TONE_CLASS[tone];
}

/** A collector's remote-config status (APPLIED/APPLYING/FAILED/…) as a tone. */
export function collectorStatusTone(status: string): StatusTone {
  switch (status) {
    case 'APPLIED':
      return 'ok';
    case 'APPLYING':
      return 'warn';
    case 'FAILED':
      return 'danger';
    default:
      return 'neutral';
  }
}
