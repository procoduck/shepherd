import { describe, expect, it } from 'vitest';
import { isValidMatcher } from '@/lib/matcher';
import { MAX_MATCHER_SUGGESTIONS, matcherSuggestions } from './useMatcherSuggestions';

describe('matcherSuggestions', () => {
  it('turns each key/value into a key="value" matcher, keys sorted, values in server order', () => {
    expect(
      matcherSuggestions({ role: ['metrics', 'logs'], cluster: ['prod'], team: ['payments'] }),
    ).toEqual(['cluster="prod"', 'role="metrics"', 'role="logs"', 'team="payments"']);
  });

  it('skips keys the matcher grammar cannot express', () => {
    expect(
      matcherSuggestions({ 'k8s.namespace': ['default'], '1st': ['x'], zone: ['eu-1'] }),
    ).toEqual(['zone="eu-1"']);
  });

  it('escapes quotes and backslashes so every suggestion is a valid matcher', () => {
    const out = matcherSuggestions({ path: ['C:\\logs', 'say "hi"'] });
    expect(out).toEqual(['path="C:\\\\logs"', 'path="say \\"hi\\""']);
    for (const m of out) expect(isValidMatcher(m)).toBe(true);
  });

  it('ignores non-string values and non-list entries', () => {
    expect(matcherSuggestions({ port: [8080, 'http'], broken: 'nope', empty: [] })).toEqual([
      'port="http"',
    ]);
  });

  it('is empty for no attributes', () => {
    expect(matcherSuggestions(undefined)).toEqual([]);
    expect(matcherSuggestions({})).toEqual([]);
  });

  it('caps the list', () => {
    const many = Array.from({ length: MAX_MATCHER_SUGGESTIONS + 50 }, (_, i) => `v${i}`);
    expect(matcherSuggestions({ key: many })).toHaveLength(MAX_MATCHER_SUGGESTIONS);
  });
});
