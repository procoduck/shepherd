import { describe, expect, it } from 'vitest';
import { isValidMatcher } from './matcher';

describe('isValidMatcher', () => {
  it.each([
    ['cluster="prod-eu-1"', 'plain equality'],
    ['env=~"prod.*"', 'regex match'],
    ['role!="debug"', 'negated equality'],
    ['name!~"^test.*"', 'negated regex'],
    ['k="a\\"b"', 'escaped quote in value'],
    ['  cluster="prod-eu-1"  ', 'surrounding whitespace'],
    ['cluster = "prod-eu-1"', 'whitespace around operator'],
    ['empty=""', 'empty value'],
  ])('accepts %s (%s)', (input) => {
    expect(isValidMatcher(input)).toBe(true);
  });

  it.each([
    ['', 'empty string'],
    ['cluster', 'no operator or value'],
    ['cluster=prod', 'unquoted value'],
    ['cluster="unterminated', 'unterminated quote'],
    ['="value"', 'missing key'],
    ['1cluster="x"', 'key starting with a digit'],
    ['cluster: "x"', 'colon instead of an operator'],
    ['cluster=="x"', 'invalid operator'],
  ])('rejects %s (%s)', (input) => {
    expect(isValidMatcher(input)).toBe(false);
  });
});
