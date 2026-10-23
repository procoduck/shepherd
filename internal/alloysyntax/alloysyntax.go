// Package alloysyntax is the one way Shepherd parses Alloy configuration
// text: ParseFile runs a nesting prescan (CheckNesting) before
// github.com/grafana/alloy/syntax/parser, because that parser recurses once
// per nesting level and a deeply nested input — `x = [[[[…]]]]` at ~487 000
// levels, under 1 MiB — overflows the goroutine stack, a fatal error Go
// cannot recover from, killing the server. guard_test.go fails if any other
// package imports the parser directly.
package alloysyntax

import (
	"fmt"

	"github.com/grafana/alloy/syntax/ast"
	"github.com/grafana/alloy/syntax/parser"
)

// MaxNesting is the deepest nesting ParseFile accepts: brackets, braces and
// parentheses (blocks, objects, lists, calls, grouping) open at once, or a
// run of prefix operators (! and -, each of which the parser also recurses
// on). The deepest configuration in this repository — wizard output,
// golden files, the visual-builder corpus, the k8s-monitoring modules — is
// far below 64 (TestDeepestRepositoryConfig); 512 leaves an order of
// magnitude of headroom while keeping the parser's stack to a few hundred
// frames.
const MaxNesting = 512

// NestingError is CheckNesting's refusal, positioned at the opening token
// that went past MaxNesting.
type NestingError struct {
	Line, Col int
}

func (e *NestingError) Error() string {
	return fmt.Sprintf("%d:%d: nesting deeper than %d levels (brackets, braces, parentheses or prefix operators); "+
		"Shepherd does not parse configuration nested this deeply", e.Line, e.Col, MaxNesting)
}

// CheckNesting scans src the way Alloy's scanner tokenises it — skipping
// "…" and '…' strings (with backslash escapes, ending at a newline as the
// scanner does), `…` raw strings (multi-line, no escapes), // line comments
// and /* */ block comments — and refuses it when more than MaxNesting
// brackets, braces or parentheses are open at once, or more than MaxNesting
// prefix operators (! or -) follow one another. It allocates nothing and is
// linear in src. Unbalanced input is not its business: the parser reports
// it.
func CheckNesting(src []byte) error {
	depth, run := 0, 0
	line, lineStart := 1, 0
	at := func(i int) error { return &NestingError{Line: line, Col: i - lineStart + 1} }
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch c {
		case '\n':
			line, lineStart = line+1, i+1
		case ' ', '\t', '\r':
		case '"', '\'':
			run = 0
			for i++; i < len(src) && src[i] != c && src[i] != '\n'; i++ {
				if src[i] == '\\' && i+1 < len(src) && src[i+1] != '\n' {
					i++
				}
			}
			if i < len(src) && src[i] == '\n' {
				line, lineStart = line+1, i+1
			}
		case '`':
			run = 0
			for i++; i < len(src) && src[i] != '`'; i++ {
				if src[i] == '\n' {
					line, lineStart = line+1, i+1
				}
			}
		case '/':
			switch {
			case i+1 < len(src) && src[i+1] == '/':
				for i < len(src) && src[i] != '\n' {
					i++
				}
				if i < len(src) {
					line, lineStart = line+1, i+1
				}
			case i+1 < len(src) && src[i+1] == '*':
				for i += 2; i < len(src) && (src[i] != '*' || i+1 >= len(src) || src[i+1] != '/'); i++ {
					if src[i] == '\n' {
						line, lineStart = line+1, i+1
					}
				}
				i++ // the closing '/'
			default:
				run = 0
			}
		case '[', '{', '(':
			run = 0
			if depth++; depth > MaxNesting {
				return at(i)
			}
		case ']', '}', ')':
			run = 0
			if depth > 0 {
				depth--
			}
		case '!', '-':
			if run++; run > MaxNesting {
				return at(i)
			}
		default:
			run = 0
		}
	}
	return nil
}

// ParseFile is parser.ParseFile behind CheckNesting: every parse of Alloy
// text in Shepherd goes through it.
func ParseFile(filename string, src []byte) (*ast.File, error) {
	if err := CheckNesting(src); err != nil {
		return nil, err
	}
	return parser.ParseFile(filename, src)
}
