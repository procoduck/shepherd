package alloysyntax

import (
	"errors"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"

	"github.com/grafana/alloy/syntax/ast"
	alloyparser "github.com/grafana/alloy/syntax/parser"
	"github.com/grafana/alloy/syntax/printer"
	"github.com/grafana/alloy/syntax/scanner"
	alloytoken "github.com/grafana/alloy/syntax/token"
)

// deepList is `x = [[[[…]]]]` nested n deep, the shape that overflows the
// parser's stack (fifth review of #388: ~487 000 levels, ~975 KB, under the
// pipeline contents cap).
func deepList(n int) string {
	return "x = " + strings.Repeat("[", n) + strings.Repeat("]", n) + "\n"
}

// TestMain runs the crash reproducer in a child process when asked:
// ALLOYSYNTAX_CRASH=raw parses with the Alloy parser directly, =guarded
// through ParseFile. The child's stack is capped low so the overflow is
// cheap; it is the same overflow at any cap.
func TestMain(m *testing.M) {
	if mode := os.Getenv("ALLOYSYNTAX_CRASH"); mode != "" {
		debug.SetMaxStack(16 << 20)
		n, _ := strconv.Atoi(os.Getenv("ALLOYSYNTAX_DEPTH")) //nolint:errcheck // set by the parent
		src := []byte(deepList(n))
		switch os.Getenv("ALLOYSYNTAX_SHAPE") {
		case "quote":
			src = []byte(quoteEscape(n))
		case "calls":
			src = []byte(callChain(n))
		}
		var err error
		switch mode {
		case "raw":
			_, err = alloyparser.ParseFile("x", src)
		case "print":
			// What validate.Format does: parse, then the (recursive) printer.
			var f *ast.File
			if f, err = ParseFile("x", src); err == nil {
				err = printer.Fprint(io.Discard, f)
			}
		default:
			_, err = ParseFile("x", src)
		}
		var ne *NestingError
		if errors.As(err, &ne) {
			os.Stdout.WriteString("refused: " + err.Error()) //nolint:errcheck // child output
			os.Exit(0)
		}
		os.Stdout.WriteString("parsed") //nolint:errcheck // child output
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runChild(t *testing.T, mode string, depth int) (string, error) {
	t.Helper()
	return runChildShape(t, mode, "list", depth)
}

func runChildShape(t *testing.T, mode, shape string, depth int) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "ALLOYSYNTAX_CRASH="+mode, "ALLOYSYNTAX_DEPTH="+strconv.Itoa(depth), "ALLOYSYNTAX_SHAPE="+shape)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestDeepNestingCrashesTheParserButNotParseFile: the parser itself dies
// with a fatal stack overflow on the reproducer (the bug, as it stands in
// Alloy); ParseFile refuses the same text cleanly.
func TestDeepNestingCrashesTheParserButNotParseFile(t *testing.T) {
	const depth = 487_000
	out, err := runChild(t, "raw", depth)
	if err == nil || !strings.Contains(out, "stack overflow") {
		t.Fatalf("the raw parser was expected to overflow its stack; exit %v, output %.300s", err, out)
	}
	out, err = runChild(t, "guarded", depth)
	if err != nil || !strings.HasPrefix(out, "refused: ") {
		t.Fatalf("ParseFile should refuse the reproducer cleanly; exit %v, output %.300s", err, out)
	}
}

// quoteEscape is the final review's reproducer: in a single-quoted string
// Alloy's scanner does not take \' as an escape (scanEscape stops at the
// unknown character), so the string closes at that quote and the brackets
// after it are real — a hand-written prescan that skipped the character
// after every backslash read them as string text.
func quoteEscape(n int) string {
	return "x = f('\\', " + strings.Repeat("[", n)
}

// TestQuoteEscapeReproducer: the 1 MiB version crashed ParseFile before the
// prescan was driven by Alloy's own scanner; it is refused now, in a child
// process so a crash is a failed test, not a dead test binary. The 737-byte
// version is refused in-process.
func TestQuoteEscapeReproducer(t *testing.T) {
	out, err := runChildShape(t, "guarded", "quote", 1_048_556)
	if err != nil || !strings.HasPrefix(out, "refused: ") {
		t.Fatalf("ParseFile should refuse the quote-escape reproducer; exit %v, output %.300s", err, out)
	}
	if err := CheckNesting([]byte(quoteEscape(726))); err == nil {
		t.Fatalf("CheckNesting accepted the 737-byte reproducer (726 open brackets)")
	}
}

// callChain is `x = f()()()…` with n calls: the parser builds a call or
// index chain iteratively but as a left-deep tree, two bytes per level, and
// the printer (validate.Format) recursed through it to a fatal stack
// overflow (final review: ~2.1M calls, 4.19 MB, past every cap then).
func callChain(n int) string {
	return "x = f" + strings.Repeat("()", n) + "\n"
}

// TestCallChainReproducer: formatting the call chain crashed; it is refused
// now (CheckNesting counts postfix calls and indexes as chain links, and
// ParseFile bounds the tree's depth), in a child so a crash fails the test.
func TestCallChainReproducer(t *testing.T) {
	out, err := runChildShape(t, "print", "calls", 2_100_000)
	if err != nil || !strings.HasPrefix(out, "refused: ") {
		t.Fatalf("the call-chain reproducer should be refused; exit %v, output %.300s", err, out)
	}
}

func TestCheckNesting(t *testing.T) {
	for _, c := range []struct {
		name string
		src  string
		ok   bool
	}{
		{"at the limit", deepList(MaxNesting), true},
		{"past the limit", deepList(MaxNesting + 1), false},
		{"blocks", strings.Repeat("a {\n", MaxNesting+1), false},
		{"calls", "x = " + strings.Repeat("f(", MaxNesting+1), false},
		{"prefix operators", "x = " + strings.Repeat("!", MaxNesting+1) + "true", false},
		{"spaced prefix operators", "x = " + strings.Repeat("- ", MaxNesting+1) + "1", false},
		{"brackets in a string", `x = "` + strings.Repeat("[", 10*MaxNesting) + `"`, true},
		{"an escaped quote in a string", `x = "\"` + strings.Repeat("[", 10*MaxNesting) + `"`, true},
		{"brackets in a raw string", "x = `\n" + strings.Repeat("[", 10*MaxNesting) + "\n`", true},
		{"brackets in a line comment", "// " + strings.Repeat("[", 10*MaxNesting) + "\nx = 1", true},
		{"brackets in a block comment", "/* " + strings.Repeat("{", 10*MaxNesting) + " */ x = 1", true},
		{"many balanced lists", strings.Repeat("x = [[1]]\n", 10*MaxNesting), true},
		{"a minus chain within MaxChain", "x = 1" + strings.Repeat(" - 1", MaxChain), true},
		{"a minus chain past MaxChain", "x = 1" + strings.Repeat(" - 1", MaxChain+1), false},
		{"an access chain past MaxChain", "x = a" + strings.Repeat(".b", MaxChain+1), false},
		{"chains inside chains add up", "x = " + strings.Repeat("a + (", 100) + strings.Repeat("b + ", MaxChain-50) + "c" + strings.Repeat(")", 100), false},
		{"many short chains", strings.Repeat("x = a.b.c + d\n", 10*MaxChain), true},
		{"a long list of short items", "x = [" + strings.Repeat("a.b, ", 10*MaxChain) + "]", true},
		{"a call chain past MaxChain", callChain(MaxChain + 1), false},
		{"an index chain past MaxChain", "x = a" + strings.Repeat("[0]", MaxChain+1), false},
		{"a call chain within MaxChain", callChain(100), true},
		{"many calls side by side", "x = [" + strings.Repeat("f(), ", 10*MaxChain) + "]", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := CheckNesting([]byte(c.src)); (err == nil) != c.ok {
				t.Fatalf("CheckNesting = %v, want ok=%v", err, c.ok)
			}
		})
	}
	var ne *NestingError
	if err := CheckNesting([]byte("a = 1\n" + deepList(MaxNesting+1))); !errors.As(err, &ne) || ne.Line != 2 {
		t.Fatalf("want a NestingError on line 2, got %v", err)
	}
}

// astDepth is the deepest nesting of nodes the parser recursed on.
func astDepth(f *ast.File) int {
	maxD := 0
	var walk func(n ast.Node, d int)
	walk = func(n ast.Node, d int) {
		if d > maxD {
			maxD = d
		}
		switch v := n.(type) {
		case *ast.File:
			for _, s := range v.Body {
				walk(s, d)
			}
		case *ast.BlockStmt:
			for _, s := range v.Body {
				walk(s, d+1)
			}
		case *ast.AttributeStmt:
			walk(v.Value, d)
		case *ast.ArrayExpr:
			for _, e := range v.Elements {
				walk(e, d+1)
			}
		case *ast.ObjectExpr:
			for _, f := range v.Fields {
				walk(f.Value, d+1)
			}
		case *ast.CallExpr:
			walk(v.Value, d)
			for _, e := range v.Args {
				walk(e, d+1)
			}
		case *ast.ParenExpr:
			walk(v.Inner, d+1)
		case *ast.UnaryExpr:
			walk(v.Value, d+1)
		case *ast.BinaryExpr:
			walk(v.Left, d)
			walk(v.Right, d)
		case *ast.AccessExpr:
			walk(v.Value, d)
		case *ast.IndexExpr:
			walk(v.Value, d)
			walk(v.Index, d+1)
		}
	}
	walk(f, 0)
	return maxD
}

// FuzzCheckNesting checks the prescan against the real parser: whatever it
// accepts the parser parses without overflowing (a fatal overflow would kill
// the fuzz worker and be reported), and a text the parser accepts with
// nesting well under the limit is never refused.
func FuzzCheckNesting(f *testing.F) {
	for _, s := range []string{deepList(10), deepList(MaxNesting), "a { b { c = [1, {d = (2)}] } }", "x = `[[[`", "x = \"\\\"[[\"", "// [[[\nx = !!!true", "/* {{ */ x = -(-1)"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 1<<20 {
			return
		}
		debug.SetMaxStack(256 << 20)
		if CheckNesting([]byte(src)) != nil {
			return
		}
		file, err := alloyparser.ParseFile("x", []byte(src))
		if err == nil && astDepth(file) > MaxNesting {
			t.Fatalf("CheckNesting accepted text the parser nests %d deep", astDepth(file))
		}
	})
}

// TestDeepestRepositoryConfig: no real configuration is refused, and
// the deepest one in the repository is far under MaxNesting.
func TestDeepestRepositoryConfig(t *testing.T) {
	root := repoRoot(t)
	deepest, where := 0, ""
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == ".claude") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".alloy") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := CheckNesting(src); err != nil {
			t.Errorf("%s: %v", path, err)
			return nil
		}
		if f, err := alloyparser.ParseFile(path, src); err == nil {
			if n := astDepth(f); n > deepest {
				deepest, where = n, path
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("deepest repository config: %d levels (%s)", deepest, where)
	if deepest >= 64 {
		t.Errorf("a repository config nests %d deep; MaxNesting's headroom assumption no longer holds", deepest)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test")
		}
		dir = parent
	}
}

// allowedAlloyImports are the Alloy packages that cannot parse text (the
// AST, positions, the printer, diagnostics). Everything else under
// github.com/grafana/alloy — syntax/parser, syntax/scanner, the root syntax
// package's Unmarshal/Decode, syntax/vm, and the Alloy module itself — may
// parse, and only internal/alloysyntax may import it.
var allowedAlloyImports = map[string]bool{
	"github.com/grafana/alloy/syntax/ast":     true,
	"github.com/grafana/alloy/syntax/token":   true,
	"github.com/grafana/alloy/syntax/printer": true,
	"github.com/grafana/alloy/syntax/diag":    true,
}

// TestNoDirectParserImports is the guard: outside this package (and tests),
// nothing imports github.com/grafana/alloy/syntax/parser, so no parse can
// skip CheckNesting.
func TestNoDirectParserImports(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == ".claude" || d.Name() == "web") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if filepath.Dir(path) == filepath.Join(root, "internal", "alloysyntax") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly|parser.ParseComments)
		if err != nil {
			return err
		}
		// A //go:build ignore file is never compiled into Shepherd (the
		// schema extractor, run inside an Alloy checkout).
		for _, c := range f.Comments {
			for _, l := range c.List {
				if l.Pos() < f.Package && strings.HasPrefix(l.Text, "//go:build ignore") {
					return nil
				}
			}
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(p, "github.com/grafana/alloy") && !allowedAlloyImports[p] {
				t.Errorf("%s imports %s, which can parse Alloy text; parse through alloysyntax.ParseFile", path, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// scannerDepth is the nesting the Alloy scanner's tokens open, computed here
// independently of CheckNesting: the deepest run of LBRACK/LBRACE/LPAREN
// over their closers, or of consecutive NOT/SUB.
func scannerDepth(src []byte) int {
	f := alloytoken.NewFile("x")
	sc := scanner.New(f, src, nil, 0)
	depth, run, deepest := 0, 0, 0
	for {
		_, tok, _ := sc.Scan()
		switch tok {
		case alloytoken.EOF:
			return deepest
		case alloytoken.LBRACK, alloytoken.LCURLY, alloytoken.LPAREN:
			depth++
			run = 0
		case alloytoken.RBRACK, alloytoken.RCURLY, alloytoken.RPAREN:
			if depth > 0 {
				depth--
			}
			run = 0
		case alloytoken.NOT, alloytoken.SUB:
			run++
		case alloytoken.ILLEGAL, alloytoken.LITERAL, alloytoken.COMMENT, alloytoken.IDENT, alloytoken.NUMBER,
			alloytoken.FLOAT, alloytoken.STRING, alloytoken.BOOL, alloytoken.NULL, alloytoken.OR, alloytoken.AND,
			alloytoken.ASSIGN, alloytoken.EQ, alloytoken.NEQ, alloytoken.LT, alloytoken.LTE, alloytoken.GT,
			alloytoken.GTE, alloytoken.ADD, alloytoken.MUL, alloytoken.DIV, alloytoken.MOD, alloytoken.POW,
			alloytoken.COMMA, alloytoken.DOT, alloytoken.TERMINATOR:
			run = 0
		default:
			run = 0
		}
		deepest = max(deepest, depth, run)
	}
}

// FuzzCheckNestingDifferential: CheckNesting never accepts text whose
// scanner-token nesting exceeds MaxNesting.
func FuzzCheckNestingDifferential(f *testing.F) {
	for _, s := range []string{quoteEscape(600), deepList(600), "x = `" + strings.Repeat("[", 600) + "`", "x = '\\'" + strings.Repeat("(", 600), "// [\n" + strings.Repeat("{", 600)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 1<<20 {
			return
		}
		if CheckNesting([]byte(src)) == nil && scannerDepth([]byte(src)) > MaxNesting {
			t.Fatalf("CheckNesting accepted text the scanner nests %d deep", scannerDepth([]byte(src)))
		}
	})
}

// deepTree builds an AST n levels deep without the parser, of each shape a
// walker follows.
func deepTree(shape string, n int) *ast.File {
	var e ast.Expr = &ast.LiteralExpr{Value: "1"}
	for range n {
		switch shape {
		case "array":
			e = &ast.ArrayExpr{Elements: []ast.Expr{e}}
		case "call":
			e = &ast.CallExpr{Value: e}
		case "index":
			e = &ast.IndexExpr{Value: e, Index: &ast.LiteralExpr{Value: "0"}}
		case "binary":
			e = &ast.BinaryExpr{Left: e, Right: &ast.LiteralExpr{Value: "1"}}
		case "access":
			e = &ast.AccessExpr{Value: e, Name: &ast.Ident{Name: "a"}}
		case "object":
			e = &ast.ObjectExpr{Fields: []*ast.ObjectField{{Name: &ast.Ident{Name: "a"}, Value: e}}}
		case "unary":
			e = &ast.UnaryExpr{Value: e}
		case "paren":
			e = &ast.ParenExpr{Inner: e}
		}
	}
	body := ast.Body{&ast.AttributeStmt{Name: &ast.Ident{Name: "x"}, Value: e}}
	if shape == "block" {
		body = nil
		for range n {
			body = ast.Body{&ast.BlockStmt{Name: []string{"b"}, Body: body}}
		}
	}
	return &ast.File{Body: body}
}

// TestTreeDepth: the iterative depth walk measures every shape, and a tree
// 2 million levels deep — far past what a recursive walk survives — costs it
// nothing but time.
func TestTreeDepth(t *testing.T) {
	for _, shape := range []string{"array", "call", "index", "binary", "access", "object", "unary", "paren", "block"} {
		if d := TreeDepth(deepTree(shape, 100)); d < 100 || d > 103 {
			t.Errorf("%s: depth %d, want about 100", shape, d)
		}
	}
	debug.SetMaxStack(8 << 20)
	defer debug.SetMaxStack(1 << 30)
	if d := TreeDepth(deepTree("call", 2_000_000)); d < 2_000_000 {
		t.Errorf("depth %d of a 2M-deep tree", d)
	}
}
