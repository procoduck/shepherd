package alloysyntax

import (
	"strconv"
	"testing"

	"github.com/grafana/alloy/syntax/ast"
	alloytoken "github.com/grafana/alloy/syntax/token"
)

// QuoteString's output, parsed back as an Alloy string literal, is exactly
// the input: what strict mode inlines is byte for byte what it linted.
func TestQuoteStringRoundTrips(t *testing.T) {
	for _, s := range []string{
		"",
		"plain",
		"declare \"out\" {\n  x = \"y\" // comment\n}\n",
		"back\\slash and \"quotes\" and \t tab \r\n",
		"unicode: é ✓   \x00 control",
		"${not_interpolated} {{ }}",
	} {
		q := QuoteString(s)
		f, err := ParseFile("q", []byte("v = "+q+"\n"))
		if err != nil {
			t.Fatalf("QuoteString(%q) = %s does not parse: %v", s, q, err)
		}
		lit, ok := f.Body[0].(*ast.AttributeStmt).Value.(*ast.LiteralExpr)
		if !ok || lit.Kind != alloytoken.STRING {
			t.Fatalf("QuoteString(%q) is not a string literal", s)
		}
		got, err := strconv.Unquote(lit.Value)
		if err != nil || got != s {
			t.Errorf("QuoteString(%q) round-trips to %q (%v)", s, got, err)
		}
	}
}
