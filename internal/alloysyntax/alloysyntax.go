// Package alloysyntax is the one way Shepherd parses Alloy configuration
// text: ParseFile runs a nesting prescan (CheckNesting) before
// github.com/grafana/alloy/syntax/parser, because that parser recurses once
// per nesting level and a deeply nested input — `x = [[[[…]]]]` at ~487 000
// levels, under 1 MiB — overflows the goroutine stack, a fatal error Go
// cannot recover from, killing the server. TestNoDirectParserImports fails
// if any other package imports an Alloy package that can parse text.
package alloysyntax

import (
	"fmt"

	"github.com/grafana/alloy/syntax/ast"
	"github.com/grafana/alloy/syntax/parser"
	"github.com/grafana/alloy/syntax/scanner"
	"github.com/grafana/alloy/syntax/token"
	"github.com/grafana/alloy/syntax/token/builder"
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

// MaxChain is the longest chain of binary operators and field accesses
// (`a + b + …`, `a.b.…`) one expression may hold, counting the chains of
// every expression it sits inside. The parser builds such a chain
// iteratively, but as a left-leaning tree as deep as the chain, and every
// recursive walk of the AST (position lookup, ast.Walk, the visual
// builder, signal derivation, the tenant lint) recurses that deep: a 1 MiB
// `a+a+…` is 500 000 levels. Real configuration chains a handful of terms;
// 4096 bounds the tree's depth to MaxNesting + MaxChain deterministically.
const MaxChain = 4096

// MaxTreeDepth is the deepest AST ParseFile returns, measured by TreeDepth
// (an iterative walk, so measuring cannot overflow). It is the structural
// backstop behind CheckNesting: whatever construct the prescan does not
// count, no caller — the printer, the visual builder, signal derivation,
// the tenant lint, any recursive walker — is ever handed a tree deeper than
// this. The deepest configuration in the repository is far below 64
// (TestDeepestRepositoryConfig); 1024 frames of any walker is a few hundred
// KB of stack.
const MaxTreeDepth = 1024

// NestingError is CheckNesting's refusal, positioned at the token it is
// about: one that went past MaxNesting or MaxChain, or one Alloy's scanner
// refused.
type NestingError struct {
	Line, Col int
	// Scan is the scanner's message when it refused the text; empty for a
	// nesting refusal.
	Scan string
	// Chain is set for a MaxChain refusal.
	Chain bool
}

func (e *NestingError) Error() string {
	if e.Scan != "" {
		return fmt.Sprintf("%d:%d: %s", e.Line, e.Col, e.Scan)
	}
	if e.Chain {
		return fmt.Sprintf("%d:%d: an expression chains more than %d operators or field accesses; "+
			"Shepherd does not parse expressions this long", e.Line, e.Col, MaxChain)
	}
	return fmt.Sprintf("%d:%d: nesting deeper than %d levels (brackets, braces, parentheses or prefix operators); "+
		"Shepherd does not parse configuration nested this deeply", e.Line, e.Col, MaxNesting)
}

// CheckNesting tokenises src with Alloy's OWN scanner (iterative, the one
// the parser drives, so strings, raw strings, escapes and comments are read
// exactly as the parser will read them) and refuses it when more than
// MaxNesting brackets, braces or parentheses are open at once, or more than
// MaxNesting prefix operators (! or -) follow one another. Text the scanner
// itself refuses (an illegal character, an unterminated string, a bad
// escape) is refused too, at the scanner's first error: the parser would
// fail on it anyway, and nothing of it is parsed. Linear in src.
func CheckNesting(src []byte) error {
	var first *NestingError
	file := token.NewFile("<pipeline>")
	s := scanner.New(file, src, func(pos token.Pos, msg string) {
		if first == nil {
			p := pos.Position()
			first = &NestingError{Line: p.Line, Col: p.Column, Scan: msg}
		}
	}, 0)
	// chain is the current expression's operator/access count; outer holds
	// the counts of the expressions each open bracket sits inside, and
	// outerSum their total, so a chain inside a chain counts both.
	depth, run, chain, outerSum := 0, 0, 0, 0
	var outer []int
	// prevPrimary is whether the previous token ends a primary expression,
	// so a following ( or [ is a postfix call or index — a chain link the
	// parser builds into a left-deep tree like a binary operator.
	prevPrimary := false
	for {
		pos, tok, _ := s.Scan()
		wasPrimary := prevPrimary
		switch tok {
		case token.IDENT, token.STRING, token.NUMBER, token.FLOAT, token.BOOL, token.NULL,
			token.RBRACK, token.RPAREN, token.RCURLY:
			prevPrimary = true
		case token.ILLEGAL, token.EOF, token.LITERAL, token.COMMENT, token.OR, token.AND, token.NOT, token.ASSIGN,
			token.EQ, token.NEQ, token.LT, token.LTE, token.GT, token.GTE, token.ADD, token.SUB, token.MUL,
			token.DIV, token.MOD, token.POW, token.LCURLY, token.LPAREN, token.LBRACK, token.COMMA, token.DOT,
			token.TERMINATOR:
			prevPrimary = false
		default:
			prevPrimary = false
		}
		switch tok {
		case token.EOF:
			if first != nil {
				return first
			}
			return nil
		case token.LBRACK, token.LCURLY, token.LPAREN:
			run = 0
			if wasPrimary && tok != token.LCURLY {
				if chain++; outerSum+chain > MaxChain {
					return nestingAt(pos, true)
				}
			}
			if depth++; depth > MaxNesting {
				return nestingAt(pos, false)
			}
			outer = append(outer, chain)
			outerSum += chain
			chain = 0
		case token.RBRACK, token.RCURLY, token.RPAREN:
			run = 0
			if depth > 0 {
				depth--
			}
			if n := len(outer); n > 0 {
				chain = outer[n-1]
				outerSum -= chain
				outer = outer[:n-1]
			}
		case token.NOT:
			if run++; run > MaxNesting {
				return nestingAt(pos, false)
			}
		case token.SUB:
			if run++; run > MaxNesting {
				return nestingAt(pos, false)
			}
			if chain++; outerSum+chain > MaxChain {
				return nestingAt(pos, true)
			}
		case token.OR, token.AND, token.EQ, token.NEQ, token.LT, token.LTE, token.GT, token.GTE,
			token.ADD, token.MUL, token.DIV, token.MOD, token.POW, token.DOT:
			run = 0
			if chain++; outerSum+chain > MaxChain {
				return nestingAt(pos, true)
			}
		case token.COMMA, token.ASSIGN:
			run, chain = 0, 0
		case token.TERMINATOR:
			// A line end ends a statement's expression, but not a run of
			// prefix operators the parser would still recurse through.
			chain = 0
		case token.ILLEGAL, token.LITERAL, token.COMMENT, token.IDENT, token.NUMBER, token.FLOAT,
			token.STRING, token.BOOL, token.NULL:
			run = 0
		default:
			run = 0
		}
	}
}

func nestingAt(pos token.Pos, chain bool) error {
	p := pos.Position()
	return &NestingError{Line: p.Line, Col: p.Column, Chain: chain}
}

// ParseFile is parser.ParseFile behind CheckNesting: every parse of Alloy
// text in Shepherd goes through it.
func ParseFile(filename string, src []byte) (*ast.File, error) {
	if err := CheckNesting(src); err != nil {
		return nil, err
	}
	f, err := parser.ParseFile(filename, src)
	if f != nil {
		if d := TreeDepth(f); d > MaxTreeDepth {
			return nil, &NestingError{Line: 1, Col: 1, Scan: fmt.Sprintf(
				"the configuration's syntax tree is %d levels deep, more than the %d Shepherd handles", d, MaxTreeDepth)}
		}
	}
	return f, err
}

// TreeDepth is the deepest path from f to a leaf, one level per edge,
// measured with an explicit stack: it cannot overflow, however deep f is.
func TreeDepth(f *ast.File) int {
	type item struct {
		n ast.Node
		d int
	}
	stack := []item{{f, 0}}
	deepest := 0
	push := func(n ast.Node, d int) {
		if n != nil {
			stack = append(stack, item{n, d})
		}
	}
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		deepest = max(deepest, it.d)
		d := it.d + 1
		switch n := it.n.(type) {
		case *ast.File:
			for _, st := range n.Body {
				push(st, d)
			}
		case *ast.BlockStmt:
			for _, st := range n.Body {
				push(st, d)
			}
		case *ast.AttributeStmt:
			if n.Value != nil {
				push(n.Value, d)
			}
		case *ast.ArrayExpr:
			for _, e := range n.Elements {
				push(e, d)
			}
		case *ast.ObjectExpr:
			for _, fl := range n.Fields {
				if fl != nil && fl.Value != nil {
					push(fl.Value, d)
				}
			}
		case *ast.CallExpr:
			if n.Value != nil {
				push(n.Value, d)
			}
			for _, e := range n.Args {
				push(e, d)
			}
		case *ast.IndexExpr:
			if n.Value != nil {
				push(n.Value, d)
			}
			if n.Index != nil {
				push(n.Index, d)
			}
		case *ast.AccessExpr:
			if n.Value != nil {
				push(n.Value, d)
			}
		case *ast.BinaryExpr:
			if n.Left != nil {
				push(n.Left, d)
			}
			if n.Right != nil {
				push(n.Right, d)
			}
		case *ast.UnaryExpr:
			if n.Value != nil {
				push(n.Value, d)
			}
		case *ast.ParenExpr:
			if n.Inner != nil {
				push(n.Inner, d)
			}
		}
	}
	return deepest
}

// QuoteString renders s as an Alloy string literal, with the Alloy syntax
// package's own encoder (token/builder): parsed back, it is exactly s.
func QuoteString(s string) string {
	e := builder.NewExpr()
	e.SetValue(s)
	return string(e.Bytes())
}
