import { describe, expect, it } from 'vitest';
import { isValidMatcher, matcherError } from './matcher';

// Every case below was checked against the server's parser, Alertmanager's
// labels.ParseMatcher (github.com/prometheus/alertmanager v0.34.1,
// pkg/labels/parse.go), which internal/mgmtapi/rpc_pipeline.go runs on every
// matcher a pipeline is saved with and internal/merge/merge.go evaluates.

describe('matcherError accepts what labels.ParseMatcher accepts', () => {
  it.each([
    ['cluster="prod-eu-1"', 'plain equality'],
    ['env=~"prod.*"', 'regex match'],
    ['role!="debug"', 'negated equality'],
    ['name!~"^test.*"', 'negated regex'],
    ['k="a\\"b"', 'escaped quote in value'],
    ['k="a\\\\b"', 'escaped backslash in value'],
    ['k="line\\nbreak"', 'escaped newline in value'],
    ['k="a\\qb"', 'spurious escape kept as a literal backslash'],
    ['k="trailing\\"x"', 'escaped quote mid-value'],
    ['  cluster="prod-eu-1"  ', 'surrounding whitespace'],
    ['cluster = "prod-eu-1"', 'whitespace around operator'],
    ['\tcluster\n=\r"x"\f', 'Go \\s whitespace (tab, newline, CR, form feed)'],
    ['empty=""', 'empty quoted value'],
    ['empty=', 'empty unquoted value'],
    ['cluster=prod', 'unquoted value'],
    ['cluster=prod eu', 'unquoted value with a space'],
    ['__name__="up"', 'reserved-looking name'],
    ['_x="1"', 'leading underscore'],
    [':ns:metric="1"', 'colons in the name'],
    ['k="ünïcödé ✓"', 'non-ASCII value'],
    ['k=~""', 'empty regex'],
    ['k=~"a|b|(c+)?"', 'alternation and groups'],
    ['k=~"(?i)prod"', 'RE2 inline flags'],
    ['k=~"(?is:a.b)"', 'RE2 flag group'],
    ['k=~"(?P<env>prod)"', 'RE2 named group'],
    ['k=~"[[:alpha:]]+"', 'POSIX class'],
    ['k=~"\\\\d{1,3}\\\\.\\\\d+"', 'escaped digit classes'],
    ['k=~"\\\\pL+"', 'Unicode class'],
    ['k=~"a{1000}"', 'repeat count at the RE2 limit'],
    ['k=~"\\\\x41\\\\x{1F600}"', 'hex escapes'],
    ['k=~"\\\\Qa.b\\\\E"', 'quoted literal'],
    ['k=~"\\\\012"', 'octal escape'],
    ['k=a\\"', 'unquoted value ending in an escaped quote'],
    ['k=~"(?)a"', 'empty flag group'],
    ['k=~"(?ii)a(?U)b+"', 'repeated and ungreedy flags'],
    ['k=~"(?-i)a"', 'cleared flag'],
    ['k=~"^*a$+"', 'repeated anchors'],
    ['k=~"\\\\b?\\\\A*\\\\z+"', 'repeated empty-width escapes'],
    ['k=~"(?<n>a)(?P<n>b)"', 'a repeated group name'],
    ['k=~"(?P<1a>x)"', 'group name starting with a digit'],
    ['k=~"[[:^alpha:][:digit:]]"', 'negated POSIX class'],
    ['k=~"\\\\p{Greek}\\\\PN"', 'long and one-letter Unicode classes'],
    ['k=~"[]a]"', 'leading ] in a class'],
  ])('accepts %s (%s)', (input) => {
    expect(matcherError(input)).toBeNull();
    expect(isValidMatcher(input)).toBe(true);
  });
});

describe('matcherError refuses what labels.ParseMatcher refuses', () => {
  it.each([
    ['', 'empty string', /key="value"/],
    ['   ', 'whitespace only', /key="value"/],
    ['cluster', 'no operator or value', /key="value"/],
    ['="value"', 'missing key', /key="value"/],
    ['1cluster="x"', 'key starting with a digit', /label name/i],
    ['clus-ter="x"', 'hyphen in the key', /label name/i],
    ['clüster="x"', 'non-ASCII key', /label name/i],
    ['cluster: "x"', 'colon instead of an operator', /key="value"/],
    ['cluster=="x"', 'doubled operator', /unescaped double quote/i],
    ['cluster="unterminated', 'unterminated quote', /closing double quote/i],
    ['cluster="', 'lone opening quote', /closing double quote/i],
    ['k="a"b"', 'unescaped quote inside the value', /unescaped double quote/i],
    ['k=a"b', 'unescaped quote in an unquoted value', /unescaped double quote/i],
    ['k="abc\\"', 'escaped closing quote leaves it unterminated', /closing double quote/i],
    ['k="\uD800"', 'lone surrogate (not valid UTF-8)', /UTF-8/],
    ['k=~"prod("', 'unbalanced parenthesis', /regular expression/i],
    ['k=~"[a-"', 'unterminated class', /regular expression/i],
    ['k=~"a**"', 'nested repetition', /regular expression/i],
    ['k=~"a++"', 'possessive quantifier', /regular expression/i],
    ['k!~"*a"', 'missing repetition argument', /regular expression/i],
    ['k=~"(?=a)"', 'lookahead (unsupported by RE2)', /lookaround/i],
    ['k=~"(?<!a)b"', 'lookbehind (unsupported by RE2)', /lookaround/i],
    ['k=~"(a)\\\\1"', 'backreference (unsupported by RE2)', /backreference/i],
    ['k=~"\\\\k<n>"', 'unknown escape', /escape/i],
    ['k=~"\\\\u0041"', '\\u escape (RE2 has none)', /escape/i],
    ['k=~"\\\\C"', '\\C (RE2 refuses it)', /escape/i],
    ['k=~"\\\\8"', 'escaped 8', /escape/i],
    ['k=~"\\\\x4"', 'short hex escape', /escape/i],
    ['k=~"\\\\x{zz}"', 'bad braced hex escape', /escape/i],
    ['k=~"a{1001}"', 'repeat count over the RE2 limit', /1000/],
    ['k=~"a{2,1001}"', 'repeat bound over the RE2 limit', /1000/],
    ['k=~"(?x)a"', 'unsupported flag', /flag/i],
    ['k=~"(?-)a"', 'dash with no flag', /flag/i],
    ['k=~"(?i-)a"', 'trailing dash', /flag/i],
    ['k=~"(?<>x)"', 'empty group name', /group name/i],
    ['k=~"(?P=n)"', 'Python-style backreference', /flag group/i],
    ['k=~"[[:foo:]]"', 'unknown POSIX class', /character class/i],
    ['k=~"\\\\pX"', 'unknown Unicode category', /Unicode category/],
    ['k=~"\\\\Q("', '\\Q without \\E', /\\E/],
    ['k=~"x{1001,}"', 'open repeat over the RE2 limit', /1000/],
    ['k=~"a\\\\"', 'trailing backslash', /backslash/i],
  ])('refuses %s (%s)', (input, _why, message) => {
    const err = matcherError(input);
    expect(err).not.toBeNull();
    expect(err).toMatch(message);
    expect(isValidMatcher(input)).toBe(false);
  });

  it('starts every message with a capital letter and ends it with a full stop', () => {
    for (const input of ['', '1x="a"', 'k="a', 'k="a"b"', 'k=~"("', 'k=~"(?=a)"']) {
      const err = matcherError(input) ?? '';
      expect(err).toMatch(/^[A-Z]/);
      expect(err).toMatch(/\.$/);
    }
  });
});
