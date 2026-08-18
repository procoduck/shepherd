// Alloy syntax highlighting via CodeMirror StreamLanguage.
// Implements token rules per spec §13.6.

import { StreamLanguage } from '@codemirror/language';
import { tags } from '@lezer/highlight';

interface StreamState {
  inBlockComment: boolean;
}

const alloyStreamLang = StreamLanguage.define<StreamState>({
  name: 'alloy',

  startState(): StreamState {
    return { inBlockComment: false };
  },

  token(stream, state) {
    // Block comment continuation
    if (state.inBlockComment) {
      if (stream.skipTo('*/')) {
        stream.next();
        stream.next();
        state.inBlockComment = false;
      } else {
        stream.skipToEnd();
      }
      return 'comment';
    }

    // Whitespace
    if (stream.eatSpace()) return null;

    // Line comment
    if (stream.match('//')) {
      stream.skipToEnd();
      return 'comment';
    }

    // Block comment start
    if (stream.match('/*')) {
      state.inBlockComment = true;
      if (stream.skipTo('*/')) {
        stream.next();
        stream.next();
        state.inBlockComment = false;
      } else {
        stream.skipToEnd();
      }
      return 'comment';
    }

    // Double-quoted string
    if (stream.peek() === '"') {
      stream.next();
      let ch: string | null | undefined;
      while ((ch = stream.next() ?? null) != null) {
        if (ch === '\\') {
          stream.next();
          continue;
        }
        if (ch === '"') break;
      }
      return 'string';
    }

    // Numbers (including duration/size suffixes)
    if (stream.match(/^[0-9]+(\.[0-9]+)?([smhd]|ms|us|ns|MiB|GiB|KiB|MB|GB|KB)?/)) {
      return 'number';
    }

    // Boolean / null
    if (stream.match(/^(true|false|null)\b/)) {
      return 'bool';
    }

    // Keywords: dotted identifiers in block-header position
    // (start of statement followed by optional label then {)
    if (stream.sol() || stream.peek()?.match(/[a-z]/)) {
      const m = stream.match(/^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*/);
      if (m) {
        const next = stream.peek();
        if (next === ' ' || next === '\t' || next === '"' || next === '{') {
          return 'keyword';
        }
        // property name (before =)
        return 'propertyName';
      }
    }

    // Operators and punctuation
    if (stream.eat(/[=(){}[\],+\-*/]/)) return 'operator';

    stream.next();
    return null;
  },

  languageData: {
    commentTokens: { line: '//', block: { open: '/*', close: '*/' } },
    indentOnInput: /^\s*\}/,
  },
});

export function alloyLanguage() {
  return alloyStreamLang;
}

// Re-export tags for use in themes
export { tags };
