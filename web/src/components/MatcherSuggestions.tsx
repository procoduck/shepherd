import { useMatcherSuggestions } from '@/hooks/useMatcherSuggestions';

/**
 * A <datalist> of `key="value"` matchers the org's collectors actually carry,
 * for a matcher input to reference with `list={id}`. Native datalist keeps
 * typing free-form (regex and negated matchers still work) while offering
 * type-ahead for the common exact-match case.
 */
export function MatcherSuggestions({ id, orgId }: { id: string; orgId: string }) {
  const suggestions = useMatcherSuggestions(orgId);
  return (
    <datalist id={id} data-testid='matcher-suggestions'>
      {suggestions.map((s) => (
        <option key={s} value={s} />
      ))}
    </datalist>
  );
}
