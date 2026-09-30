package mgmtapi

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestEveryWriteProcedureIsAudited backs the docs' promise that every
// mutating action is written to the audit log (#199). A 2026-09-30 walkthrough
// found fourteen write handlers with no audit row — org edit/delete, cluster
// claim/unclaim, agent tokens, group assignments, git credentials and repo
// links, tenant routes. This test fails the moment a write procedure
// (capabilityApply in capabilityRequirements) has a handler that never calls
// an audit function, directly or through a same-receiver helper one call deep
// (EnablePipeline → setEnabled).
//
// It is structural: it proves a call site exists, not that every path writes
// one. The per-feature specs cover behaviour.
func TestEveryWriteProcedureIsAudited(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	// Handler bodies keyed "Receiver.Method".
	methods := map[string]*ast.BlockStmt{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil || len(fn.Recv.List) != 1 {
				continue
			}
			star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			recv, ok := star.X.(*ast.Ident)
			if !ok {
				continue
			}
			methods[recv.Name+"."+fn.Name.Name] = fn.Body
		}
	}

	callsAudit := func(body *ast.BlockStmt) bool {
		found := false
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var name string
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				name = fn.Name
			case *ast.SelectorExpr:
				name = fn.Sel.Name
			}
			if strings.HasPrefix(strings.ToLower(name), "audit") || name == "InsertAuditLog" {
				found = true
			}
			return !found
		})
		return found
	}

	var missing []string
	for proc, capability := range capabilityRequirements {
		if capability != capabilityApply {
			continue
		}
		// "/shepherd.mgmt.v1.PipelineService/EnablePipeline"
		parts := strings.Split(strings.TrimPrefix(proc, "/shepherd.mgmt.v1."), "/")
		if len(parts) != 2 {
			t.Fatalf("unexpected procedure name %q", proc)
		}
		recv, method := parts[0], parts[1]
		body, ok := methods[recv+"."+method]
		if !ok {
			t.Errorf("%s: no handler %s.%s found in this package", proc, recv, method)
			continue
		}
		if callsAudit(body) {
			continue
		}
		// One call deep: a same-receiver helper that audits.
		helperAudits := false
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "s" {
				if hb, ok := methods[recv+"."+sel.Sel.Name]; ok && callsAudit(hb) {
					helperAudits = true
				}
			}
			return !helperAudits
		})
		if !helperAudits {
			missing = append(missing, proc)
		}
	}
	sort.Strings(missing)
	for _, proc := range missing {
		t.Errorf("%s is a write procedure (capabilityApply) whose handler writes no audit row — every mutating action must be audited", proc)
	}
}
