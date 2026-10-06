// matcher.ts — client-side validation for pipeline matchers.
//
// The server parses every matcher with Alertmanager's labels.ParseMatcher
// (github.com/prometheus/alertmanager v0.34.1, pkg/labels/parse.go): it runs
// on save in internal/mgmtapi/rpc_pipeline.go and again when
// internal/merge/merge.go (MatchesPipeline) evaluates a pipeline. This file
// mirrors that parser rule for rule so a chip editor can refuse a matcher the
// server would refuse, with a reason, before it is ever added:
//
//   1. The whole input must match
//        ^\s*([a-zA-Z_:][a-zA-Z0-9_:]*)\s*(=~|=|!=|!~)\s*((?s).*?)\s*$
//      where Go's \s is [\t\n\f\r ]. `=~` is tried before `=`.
//   2. The value may be wrapped in double quotes. Only a LEADING quote makes
//      it quoted; a quoted value must end in an unescaped quote.
//   3. Inside the value, `\"`, `\\` and `\n` are escapes; any other `\x` is
//      kept as a literal backslash plus x, and a final lone `\` is literal.
//      An unescaped `"` is only allowed as the closing quote.
//   4. The value must be valid UTF-8.
//   5. For `=~` / `!~`, `^(?:value)$` must compile as a Go (RE2) regexp.
//
// Rule 5 is the one place this cannot be exact: the browser has no RE2. The
// check below refuses what RE2 refuses that JavaScript would accept
// (lookarounds, backreferences, unknown escapes, repeat counts over 1000, bad
// flags), rewrites the RE2-only syntax JavaScript would choke on (`(?P<n>`,
// inline flags, `\Q…\E`, `\x{…}`), and lets the JavaScript engine judge the
// structure (balanced groups, classes, nothing-to-repeat). The server's
// parser stays authoritative: anything that slips through is refused on save
// and shown inline there.

const GO_SPACE = '[\\t\\n\\f\\r ]';
const MATCHER_RE = new RegExp(
  `^${GO_SPACE}*([a-zA-Z_:][a-zA-Z0-9_:]*)${GO_SPACE}*(=~|=|!=|!~)${GO_SPACE}*([\\s\\S]*?)${GO_SPACE}*$`,
);
// The same shape with any name, to tell "bad label name" from "no operator".
const LOOSE_RE = new RegExp(`^${GO_SPACE}*([^=!~\\t\\n\\f\\r ]+)${GO_SPACE}*(=~|=|!=|!~)`);
const LONE_SURROGATE = /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/;

const FORMAT_HINT = 'Write a matcher as key="value", key!="value", key=~"regex" or key!~"regex".';

/**
 * Why `input` is not a matcher the server will accept, as a sentence for the
 * person typing it — or null when it is valid.
 */
export function matcherError(input: string): string | null {
  const m = MATCHER_RE.exec(input);
  if (!m) {
    const loose = LOOSE_RE.exec(input);
    if (loose) {
      return `The label name "${loose[1]}" is not valid: start it with a letter, underscore or colon, then use only letters, digits, underscores or colons.`;
    }
    return FORMAT_HINT;
  }
  const [, , op, rawMatch] = m;

  let raw = rawMatch;
  let expectTrailingQuote = false;
  if (raw.startsWith('"')) {
    raw = raw.slice(1);
    expectTrailingQuote = true;
  }
  if (LONE_SURROGATE.test(raw)) return 'The value is not valid UTF-8 text.';

  // Unescape exactly as parse.go does (rule 3).
  const chars = Array.from(raw);
  let value = '';
  let escaped = false;
  for (let i = 0; i < chars.length; i++) {
    const r = chars[i];
    const last = i === chars.length - 1;
    if (escaped) {
      escaped = false;
      if (r === 'n') value += '\n';
      else if (r === '"' || r === '\\') value += r;
      else value += `\\${r}`;
      continue;
    }
    if (r === '\\') {
      if (!last) {
        escaped = true;
        continue;
      }
      value += '\\';
    } else if (r === '"') {
      if (!expectTrailingQuote || !last) {
        return 'The value has an unescaped double quote: write \\" for a quote inside the value.';
      }
      expectTrailingQuote = false;
    } else {
      value += r;
    }
  }
  if (expectTrailingQuote) return 'The value is missing its closing double quote.';

  if (op === '=~' || op === '!~') {
    const reErr = re2Error(value);
    if (reErr) return `The value is not a valid regular expression: ${reErr}.`;
  }
  return null;
}

/** True when the server's labels.ParseMatcher would accept `input`. */
export function isValidMatcher(input: string): boolean {
  return matcherError(input) === null;
}

// (?flags) / (?flags:…): Go accepts an empty set, but a `-` needs a flag after it.
const FLAGS_RE = /^[imsU]*(?:-[imsU]+)?$/;
const POSIX_CLASSES = new Set([
  'alnum',
  'alpha',
  'ascii',
  'blank',
  'cntrl',
  'digit',
  'graph',
  'lower',
  'print',
  'punct',
  'space',
  'upper',
  'word',
  'xdigit',
]);
// Go's regexp/syntax escapes (parseEscape and the Perl/Unicode class
// shortcuts); everything else that is a letter or digit is refused.
const ESCAPE_LETTERS_OUTSIDE_CLASS = new Set('afnrtvdDsSwWpPAbBz');
const ESCAPE_LETTERS_IN_CLASS = new Set('afnrtvdDsSwWpP');
const MAX_REPEAT = 1000;

/**
 * Best-effort RE2 compile check of `^(?:pattern)$` (rule 5): null when it
 * compiles, else a lowercase reason.
 */
function re2Error(pattern: string): string | null {
  let js = '';
  let inClass = false;
  const s = pattern;
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (c === '\\') {
      if (i + 1 >= s.length) return 'it ends with a lone backslash';
      const n = s[i + 1];
      if (n === 'Q' && !inClass) {
        // \Q…\E: everything up to \E is literal. Without the \E the quote
        // runs on into the `)$` the server wraps the value in, so the
        // server's compile fails on an unclosed group.
        const end = s.indexOf('\\E', i + 2);
        if (end === -1) return '\\Q is not closed by \\E';
        js += s.slice(i + 2, end).replace(/[\\^$.*+?()[\]{}|/-]/g, '\\$&');
        i = end + 1;
        continue;
      }
      if (n === 'x') {
        if (s[i + 2] === '{') {
          const close = s.indexOf('}', i + 3);
          const hex = close === -1 ? '' : s.slice(i + 3, close);
          if (!/^[0-9a-fA-F]+$/.test(hex) || Number.parseInt(hex, 16) > 0x10ffff) {
            return `the escape \\x{${hex}} is not a valid code point`;
          }
          js += 'X';
          i = close;
          continue;
        }
        if (!/^[0-9a-fA-F]{2}$/.test(s.slice(i + 2, i + 4))) {
          return 'the escape \\x needs two hex digits';
        }
        js += s.slice(i, i + 4);
        i += 3;
        continue;
      }
      if (n.charCodeAt(0) >= 0x80) return `the escape \\${n} is not supported`;
      if (n >= '1' && n <= '7' && !(s[i + 2] >= '0' && s[i + 2] <= '7')) {
        return `backreferences like \\${n} are not supported`;
      }
      if (n >= '0' && n <= '7') {
        // Octal: \0, or a digit followed by more digits (up to three).
        let j = i + 2;
        while (j < s.length && j < i + 4 && s[j] >= '0' && s[j] <= '7') j++;
        js += 'O';
        i = j - 1;
        continue;
      }
      if (/[A-Za-z0-9]/.test(n)) {
        const allowed = inClass ? ESCAPE_LETTERS_IN_CLASS : ESCAPE_LETTERS_OUTSIDE_CLASS;
        if (!allowed.has(n)) return `the escape \\${n} is not supported`;
        if (n === 'p' || n === 'P') {
          // \p{Name}: a long class name is the server's to judge. \pX: the
          // one-letter form takes a general category only.
          if (s[i + 2] === '{') {
            const close = s.indexOf('}', i + 3);
            if (close === -1) return `the escape \\${n}{ is not closed`;
            i = close;
          } else {
            if (!'CLMNPSZ'.includes(s[i + 2] ?? '-')) {
              return `\\${n}${s[i + 2] ?? ''} is not a Unicode category`;
            }
            i += 2;
          }
          js += 'P';
          continue;
        }
      }
      // Go may repeat an empty-width assertion (`\b?`); JavaScript may
      // only repeat a group, so wrap them in one.
      if (!inClass && 'AzbB'.includes(n)) {
        js += n === 'A' ? '(?:^)' : n === 'z' ? '(?:$)' : `(?:\\${n})`;
      } else {
        js += c + n;
      }
      i++;
      continue;
    }
    if (inClass) {
      if (c === '[' && s[i + 1] === ':') {
        const close = s.indexOf(':]', i + 2);
        if (close !== -1) {
          const name = s.slice(i + 2, close).replace(/^\^/, '');
          if (!POSIX_CLASSES.has(name)) return `[:${name}:] is not a character class`;
          js += 'K';
          i = close + 1;
          continue;
        }
      }
      if (c === ']') inClass = false;
      js += c;
      continue;
    }
    if (c === '[') {
      inClass = true;
      js += c;
      // A leading ] (after an optional ^) is a literal in RE2.
      if (s[i + 1] === '^') {
        js += '^';
        i++;
      }
      if (s[i + 1] === ']') {
        js += '\\]';
        i++;
      }
      continue;
    }
    if (c === '(' && s[i + 1] === '?') {
      const rest = s.slice(i + 2);
      if (/^(=|!|<=|<!)/.test(rest)) return 'lookarounds like (?= and (?! are not supported';
      // Named groups: Go allows a name to repeat, so the JavaScript copy
      // drops the names and keeps the capture.
      const named = /^P?<([^>]*)>/.exec(rest);
      if (named) {
        if (!/^[A-Za-z0-9_]+$/.test(named[1])) return `the group name "${named[1]}" is not valid`;
        js += '(';
        i += 1 + named[0].length;
        continue;
      }
      const flags = /^([^:)]*)([:)])/.exec(rest);
      if (!flags || !FLAGS_RE.test(flags[1])) {
        return `the flag group (?${flags ? flags[1] + flags[2] : ''} is not valid`;
      }
      // Flags change meaning, not validity: drop a bare (?i), keep the group.
      if (flags[2] === ':') js += '(?:';
      i += 1 + flags[0].length;
      continue;
    }
    if (c === '{') {
      const rep = /^\{(\d+)(,(\d*))?\}/.exec(s.slice(i));
      if (rep && (Number(rep[1]) > MAX_REPEAT || (rep[3] && Number(rep[3]) > MAX_REPEAT))) {
        return `repeat counts cannot exceed ${MAX_REPEAT}`;
      }
    }
    js += c === '^' ? '(?:^)' : c === '$' ? '(?:$)' : c;
  }
  try {
    new RegExp(`^(?:${js})$`);
    return null;
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    // "Invalid regular expression: /…/: Unterminated group" -> "unterminated group"
    const reason = msg.replace(/^Invalid regular expression: \/.*\/[a-z]*: /s, '');
    return reason.charAt(0).toLowerCase() + reason.slice(1);
  }
}
