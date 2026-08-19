# Autocomplete kill probe

The Playwright autocomplete scenarios in `web/tests/specs/editor-autocomplete.spec.ts`
exercise four independent completion contexts: top-level components, block attributes,
enum values after `=`, and suppression inside a string/comment. Removing the
`alloyCompletionSource` override from `AlloyEditor` causes the first three scenarios to
fail because the CodeMirror autocomplete tooltip and expected options disappear.
