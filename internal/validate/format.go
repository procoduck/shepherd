package validate

import (
	"bytes"
	"strings"

	"github.com/grafana/alloy/syntax/parser"
	"github.com/grafana/alloy/syntax/printer"
)

// Format canonicalises Alloy source the way `alloy fmt` does: it parses the
// content and re-prints the AST through the same printer the Alloy CLI uses,
// in-process (no temp file, no exec — the Stage 1 parser already runs
// in-process). It is a pure text transform: it never wraps, validates
// semantically, or reaches the bundled binary.
//
// content is the raw pipeline body as the user typed it, NOT declare-wrapped —
// formatting operates on exactly what is stored and shown in the editor.
//
// A parse failure returns a non-nil error carrying the parser's message; the
// caller surfaces it (FormatPipeline maps it to InvalidArgument) and leaves the
// buffer untouched, so formatting only ever succeeds on syntactically valid
// input. The editor's live validation already reports the same failure inline.
func Format(content string) (string, error) {
	f, err := parser.ParseFile("<pipeline>", []byte(content))
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, f); err != nil {
		return "", err
	}
	out := buf.String()
	// alloy fmt leaves a trailing newline; the printer does not always, and an
	// editor round-trip reads more predictably with one.
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out, nil
}
