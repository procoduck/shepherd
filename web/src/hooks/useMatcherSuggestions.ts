import type { JsonObject } from '@bufbuild/protobuf';
import { useQuery } from '@tanstack/react-query';
import { clients } from '@/api/transport';

// Matcher key names the server's parser accepts (Prometheus label-name
// syntax — see internal/merge and web/src/visual/matcher.ts). An attribute
// whose key falls outside it (e.g. an agent-reported "k8s.namespace") cannot
// be written as a matcher at all, so it is never suggested.
const MATCHER_KEY_RE = /^[a-zA-Z_][a-zA-Z0-9_]*$/;

// Suggestions are a convenience, not an inventory: past this many the list
// stops helping, and a very large fleet would otherwise ship it all to every
// editor.
export const MAX_MATCHER_SUGGESTIONS = 500;

/** A double-quoted matcher value, with `\` and `"` escaped. */
function quote(value: string): string {
  return `"${value.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`;
}

/**
 * Turns FleetService.ListAttributes' `{key: [values…]}` into ready-to-add
 * `key="value"` matchers, sorted by key (values keep the server's sorted order). The server already lists
 * only keys pipeline matching evaluates for the org (#139); this drops the
 * ones the matcher grammar cannot express and any non-string value.
 */
export function matcherSuggestions(attributes: JsonObject | undefined): string[] {
  const out: string[] = [];
  for (const key of Object.keys(attributes ?? {}).sort()) {
    if (!MATCHER_KEY_RE.test(key)) continue;
    const values = attributes?.[key];
    if (!Array.isArray(values)) continue;
    for (const v of values) {
      if (typeof v !== 'string') continue;
      out.push(`${key}=${quote(v)}`);
      if (out.length >= MAX_MATCHER_SUGGESTIONS) return out;
    }
  }
  return out;
}

/** Matcher suggestions for an org; an empty list while loading or on error. */
export function useMatcherSuggestions(orgId: string): string[] {
  const { data } = useQuery({
    queryKey: ['matcher-suggestions', orgId],
    queryFn: () => clients.fleet.listAttributes({ orgId }),
    enabled: orgId !== '',
    staleTime: 60_000,
  });
  return matcherSuggestions(data?.attributes);
}
