package pinnedimport

// The Inline and References of 3ec5c9c (before the served-form budget),
// verbatim but for the names (ref...) and q, which is the interface the
// walker reads the cache through: what inline_budget_test.go compares against.

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/grafana/alloy/syntax/ast"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/alloysyntax"
	"shepherd/internal/gitrepo"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
)

// refInline is the old Inline. Its drop pass is today's
// validate.InsertTenantDrops, which validate's own differential test
// (served_budget_diff_test.go) holds to the old one.
func refInline(ctx context.Context, q moduleQuerier, orgID pgtype.UUID, repos []sqlc.OrgImportRepository, contents string) (string, error) {
	w := newRefWalker(ctx, q, orgID, repos)
	out, err := w.inline(contents, 0, 0)
	if err != nil {
		return "", err
	}
	if out, err = validate.InsertTenantDrops(out); err != nil {
		return "", fmt.Errorf("%w: %w", ErrNotInlinable, err)
	}
	if len(out) > validate.MaxContentBytes {
		return "", fmt.Errorf("%w: as served (pinned modules inlined, __tenant_id__ dropped after "+
			"each labelmap) the pipeline is %d bytes, more than the %d a "+
			"pipeline may be", ErrNotInlinable, len(out), validate.MaxContentBytes)
	}
	return out, nil
}

// References lists every pinned module contents reaches, through cached
// modules — what must stay cached while contents is enabled. A module that
// is not cached is listed, and what it would import is not.
func refReferences(ctx context.Context, q moduleQuerier, orgID pgtype.UUID, repos []sqlc.OrgImportRepository, contents string) ([]Ref, error) {
	w := newRefWalker(ctx, q, orgID, repos)
	w.collect = true
	if _, err := w.inline(contents, 0, 0); err != nil {
		return nil, err
	}
	return w.refs, nil
}

type refWalker struct {
	ctx     context.Context
	q       moduleQuerier
	orgID   pgtype.UUID
	repos   map[string]bool
	collect bool
	refs    []Ref
}

func newRefWalker(ctx context.Context, q moduleQuerier, orgID pgtype.UUID, repos []sqlc.OrgImportRepository) *refWalker {
	w := &refWalker{ctx: ctx, q: q, orgID: orgID, repos: map[string]bool{}}
	for i := range repos {
		w.repos[repos[i].RepositoryKey] = true
	}
	return w
}

// refMaxModuleDepth is how many modules (import.git or import.string) Inline
// follows into: the tenant lint's module depth.
const refMaxModuleDepth = 8

// refSplice is one rewrite of a byte range of a text.
type refSplice struct {
	start, end int // [start, end)
	text       string
}

// inline rewrites text's import.git blocks — found in the parse tree, so
// whatever spelling of the name the parser accepts — and descends into each
// import.string whose content is a plain literal, rewriting the module text
// it holds the same way. gitDepth is how many import.git modules enclose
// text, depth how many modules of either kind. Text with nothing to rewrite
// is returned unchanged; otherwise only the rewritten byte ranges change,
// located by the parsed nodes' positions.
func (w *refWalker) inline(text string, gitDepth, depth int) (string, error) {
	if depth > refMaxModuleDepth {
		return "", fmt.Errorf("%w: modules nest more than %d deep", ErrNotInlinable, refMaxModuleDepth)
	}
	file, err := alloysyntax.ParseFile("<pipeline>", []byte(text))
	if err != nil {
		if w.collect {
			return text, nil
		}
		return "", fmt.Errorf("%w: the text does not parse: %w", ErrNotInlinable, err)
	}
	var blocks []*ast.BlockStmt
	refCollectImports(file.Body, &blocks, 0)
	var splices []refSplice
	for _, b := range blocks {
		var sp *refSplice
		var err error
		if strings.Join(b.Name, ".") == "import.git" {
			sp, err = w.gitBlock(b, gitDepth, depth)
		} else {
			sp, err = w.stringBlock(b, gitDepth, depth)
		}
		if err != nil {
			if w.collect {
				continue
			}
			return "", err
		}
		if sp != nil {
			splices = append(splices, *sp)
		}
	}
	if len(splices) == 0 {
		return text, nil
	}
	// Apply from the last range back, so earlier offsets stay valid.
	slices.SortFunc(splices, func(a, b refSplice) int { return b.start - a.start })
	out := text
	for _, sp := range splices {
		out = out[:sp.start] + sp.text + out[sp.end:]
	}
	return out, nil
}

// gitBlock rewrites one import.git block into an import.string of its
// cached module, itself inlined.
func (w *refWalker) gitBlock(b *ast.BlockStmt, gitDepth, depth int) (*refSplice, error) {
	if gitDepth >= validate.MaxGitImportDepth {
		return nil, fmt.Errorf("%w: import.git nests more than %d deep", ErrNotInlinable, validate.MaxGitImportDepth)
	}
	ref, err := w.ref(b)
	if err != nil {
		return nil, err
	}
	if w.collect && !slices.Contains(w.refs, ref) {
		w.refs = append(w.refs, ref)
	}
	row, err := w.q.GetPinnedImportModule(w.ctx, sqlc.GetPinnedImportModuleParams{
		OrgID: w.orgID, RepositoryKey: ref.Key, Revision: ref.Revision, Path: ref.Path,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: module %s at %s (%s) is not cached", ErrNotInlinable, ref.Path, ref.Revision, ref.Key)
	}
	module, err := w.inline(row.Content, gitDepth+1, depth+1)
	if err != nil {
		return nil, err
	}
	start := ast.StartPos(b).Position()
	indent := strings.Repeat(" ", max(start.Column-1, 0))
	return &refSplice{
		start: start.Offset,
		end:   ast.EndPos(b).Position().Offset + 1,
		text: "import.string " + alloysyntax.QuoteString(b.Label) + " {\n" +
			indent + "  content = " + alloysyntax.QuoteString(module) + "\n" + indent + "}",
	}, nil
}

// stringBlock rewrites the content of one import.string whose module text
// holds an import.git (at any depth): only its content literal is replaced,
// re-quoted. Content that is not a plain literal is left alone (the served
// form's bound, validate.ServedFormProblem, refuses it in a strict org).
func (w *refWalker) stringBlock(b *ast.BlockStmt, gitDepth, depth int) (*refSplice, error) {
	for _, st := range b.Body {
		a, ok := st.(*ast.AttributeStmt)
		if !ok || a.Name.Name != "content" {
			continue
		}
		content, ok := validate.PlainStringLiteral(a.Value)
		if !ok {
			return nil, nil //nolint:nilnil // nothing this refWalker can rewrite
		}
		inner, err := w.inline(content, gitDepth, depth+1)
		if err != nil {
			return nil, err
		}
		if inner == content {
			return nil, nil //nolint:nilnil // unchanged: the bytes stay as written
		}
		return &refSplice{
			start: ast.StartPos(a.Value).Position().Offset,
			end:   ast.EndPos(a.Value).Position().Offset + 1,
			text:  alloysyntax.QuoteString(inner),
		}, nil
	}
	return nil, nil //nolint:nilnil // no content attribute
}

// refCollectImports finds the import blocks Alloy loads — import.git and
// import.string — at the top level and in a declare body, at any declare
// depth, by their parsed names.
func refCollectImports(body ast.Body, out *[]*ast.BlockStmt, nesting int) {
	if nesting > alloysyntax.MaxNesting {
		return
	}
	for _, st := range body {
		b, ok := st.(*ast.BlockStmt)
		if !ok {
			continue
		}
		switch strings.Join(b.Name, ".") {
		case "import.git", "import.string":
			*out = append(*out, b)
		case "declare":
			refCollectImports(b.Body, out, nesting+1)
		}
	}
}

// ref reads a block's module reference under the lint's rules.
func (w *refWalker) ref(b *ast.BlockStmt) (Ref, error) {
	attrs := map[string]string{}
	for _, st := range b.Body {
		a, ok := st.(*ast.AttributeStmt)
		if !ok {
			continue
		}
		if s, ok := validate.PlainStringLiteral(a.Value); ok {
			attrs[a.Name.Name] = s
		}
	}
	repo, rev, path := attrs["repository"], attrs["revision"], attrs["path"]
	if repo == "" || path == "" || !gitrepo.IsFullCommitSHA(rev) || !validate.CleanModulePath(path) {
		return Ref{}, fmt.Errorf("%w: import.git %q needs literal repository, a full commit SHA revision and a "+
			"clean path", ErrNotInlinable, b.Label)
	}
	key, err := NormalizeRepository(repo)
	if err != nil || !w.repos[key] {
		return Ref{}, fmt.Errorf("%w: import.git %q's repository is not on the allowlist", ErrNotInlinable, b.Label)
	}
	return Ref{Key: key, Revision: rev, Path: path}, nil
}
