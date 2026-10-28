package merge

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHeaderListsAnExclusionWithoutKind: an Exclusion that reaches the
// header without a Kind (a future constructor that forgot it) must still be
// listed — dropping it would hide the exclusion and change the served bytes.
// It falls into the role section, where an exclusion without a section of its
// own has always been listed.
func TestHeaderListsAnExclusionWithoutKind(t *testing.T) {
	header := buildHeader("c1", "prod/metrics", nil, []Exclusion{
		{PipelineName: "kindless", Reason: "some reason"},
	})
	want := "// Excluded (1) - signal/role mismatch (docs/gateway-tier-plan.md W1, gate G6):\n//   - kindless: some reason\n"
	if !strings.Contains(header, want) {
		t.Fatalf("an exclusion without a Kind is missing from the header:\n%s", header)
	}
}

// TestEveryExclusionLiteralSetsKind fails when any non-test file of this
// package builds a non-empty Exclusion literal without a Kind. The zero
// value Exclusion{} is the "not excluded" sentinel RoleCheck returns and is
// never listed.
func TestEveryExclusionLiteralSetsKind(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			id, ok := lit.Type.(*ast.Ident)
			if !ok || id.Name != "Exclusion" || len(lit.Elts) == 0 {
				return true
			}
			checked++
			for _, e := range lit.Elts {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Kind" {
						return true
					}
				}
			}
			t.Errorf("%s: Exclusion literal without Kind", fset.Position(lit.Pos()))
			return true
		})
	}
	if checked == 0 {
		t.Fatal("found no Exclusion literal to check; the scan is not looking at this package")
	}
}
