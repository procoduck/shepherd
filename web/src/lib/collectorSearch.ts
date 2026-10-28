// The collector page's search parameters (src/pages/CollectorDetailPage.tsx),
// kept out of the page so the router's entry chunk can validate them.
import { lineSpanParam, parseLineSpan } from './servedProvenance';

export type CollectorTab =
  | 'config'
  | 'pipelines'
  | 'reconciliation'
  | 'info'
  | 'attributes'
  | 'access';

const TABS: readonly CollectorTab[] = [
  'config',
  'pipelines',
  'reconciliation',
  'info',
  'attributes',
  'access',
];

/** The collector page's search parameters: `tab` (absent: Served Config)
 *  and `lines` ("8-14"), a highlighted span of the served config. Anything
 *  else, or a value that names nothing, is dropped. */
export interface CollectorSearch {
  tab?: CollectorTab;
  lines?: string;
}

export function validateCollectorSearch(search: Record<string, unknown>): CollectorSearch {
  const out: CollectorSearch = {};
  const tab = TABS.find((t) => t === search.tab);
  if (tab) out.tab = tab;
  // A highlight is on the Served Config tab only.
  const span = parseLineSpan(search.lines);
  if (span && (tab ?? 'config') === 'config') out.lines = lineSpanParam(span);
  return out;
}
