// matcher.ts — client-side shape validation for pipeline matchers.
//
// Mirrors the operator set the backend actually accepts: matchers are
// parsed server-side with Prometheus's labels.ParseMatcher (see
// internal/merge/merge.go), which supports `=`, `!=`, `=~`, `!~`. This is a
// cheap, purely syntactic pre-check so the toolbar's chip editor can reject
// an obviously malformed matcher (e.g. missing quotes, no operator) before
// it ever reaches the server — it does not attempt to validate that a `=~`/
// `!~` value is a legal regex, which the server-side parser still owns.
const MATCHER_RE = /^[a-zA-Z_][a-zA-Z0-9_]*\s*(=~|!~|!=|=)\s*"(?:[^"\\]|\\.)*"$/;

/**
 * True when `input` has the shape `key="value"` or `key=~"regex"` (also
 * accepts the `!=`/`!~` negated forms). Leading/trailing whitespace is
 * tolerated; the value itself must be a double-quoted string, matching the
 * PromQL-style matcher syntax used throughout the app (see
 * PipelineEditorPage's matcher editor and internal/merge).
 */
export function isValidMatcher(input: string): boolean {
  return MATCHER_RE.test(input.trim());
}
