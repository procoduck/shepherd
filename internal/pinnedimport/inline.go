package pinnedimport

import (
	"context"
	"errors"
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

// ErrNotInlinable is a pinned import.git Inline cannot rewrite: its
// repository, revision or path is not a string literal, the revision is not a
// full commit SHA, the repository is not on the allowlist, the module is not
// cached, or it nests deeper than validate.MaxGitImportDepth. The serve path
// fails closed on it: a strict org is never served an import.git.
var ErrNotInlinable = errors.New("pinned import.git cannot be inlined")

// Ref is one pinned module, as the cache keys it.
type Ref struct {
	Key, Revision, Path string
}

// Inline is what a strict org is served for contents (#390): every
// import.git block — at the top level or inside a declare — rewritten into
// `import.string "<same label>" { content = "<module>" }`, where module is
// the cached text the tenant lint checked, itself inlined the same way
// (nested imports to validate.MaxGitImportDepth). The label is kept, so
// `<label>.<declare>` instances are unchanged; the block's other attributes
// and its auth blocks are not served. An import.git inside an import.string's
// content is rewritten there, the content re-quoted. Every other byte of
// contents is kept: text without an import.git block (found by parsing,
// never by searching) is returned unchanged, so a pipeline that uses none
// serves exactly what it did before. The content is quoted by
// alloysyntax.QuoteString (the Alloy syntax package's encoder).
//
// The inlined text then gets validate.InsertTenantDrops (#410): a rule
// dropping __tenant_id__ directly after every labelmap that may write it,
// in the modules too. Text with neither an import.git nor such a labelmap
// is returned unchanged.
//
// Only the cache is read, through q. The result is at most
// validate.MaxContentBytes (the pipeline contents cap).
func Inline(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, repos []sqlc.OrgImportRepository, contents string) (string, error) {
	w := newWalker(ctx, q, orgID, repos)
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

// InlineOrg is Inline with orgID's allowlist read through q.
func InlineOrg(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, contents string) (string, error) {
	repos, err := q.ListOrgImportRepositories(ctx, orgID)
	if err != nil {
		return "", fmt.Errorf("loading the org's import repositories: %w", err)
	}
	return Inline(ctx, q, orgID, repos, contents)
}

// References lists every pinned module contents reaches, through cached
// modules — what must stay cached while contents is enabled. A module that
// is not cached is listed, and what it would import is not.
func References(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, repos []sqlc.OrgImportRepository, contents string) ([]Ref, error) {
	w := newWalker(ctx, q, orgID, repos)
	w.collect = true
	if _, err := w.inline(contents, 0, 0); err != nil {
		return nil, err
	}
	return w.refs, nil
}

type walker struct {
	ctx     context.Context
	q       *sqlc.Queries
	orgID   pgtype.UUID
	repos   map[string]bool
	collect bool
	refs    []Ref
}

func newWalker(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, repos []sqlc.OrgImportRepository) *walker {
	w := &walker{ctx: ctx, q: q, orgID: orgID, repos: map[string]bool{}}
	for i := range repos {
		w.repos[repos[i].RepositoryKey] = true
	}
	return w
}

// maxModuleDepth is how many modules (import.git or import.string) Inline
// follows into: the tenant lint's module depth.
const maxModuleDepth = 8

// splice is one rewrite of a byte range of a text.
type splice struct {
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
func (w *walker) inline(text string, gitDepth, depth int) (string, error) {
	if depth > maxModuleDepth {
		return "", fmt.Errorf("%w: modules nest more than %d deep", ErrNotInlinable, maxModuleDepth)
	}
	file, err := alloysyntax.ParseFile("<pipeline>", []byte(text))
	if err != nil {
		if w.collect {
			return text, nil
		}
		return "", fmt.Errorf("%w: the text does not parse: %w", ErrNotInlinable, err)
	}
	var blocks []*ast.BlockStmt
	collectImports(file.Body, &blocks, 0)
	var splices []splice
	for _, b := range blocks {
		var sp *splice
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
	slices.SortFunc(splices, func(a, b splice) int { return b.start - a.start })
	out := text
	for _, sp := range splices {
		out = out[:sp.start] + sp.text + out[sp.end:]
	}
	return out, nil
}

// gitBlock rewrites one import.git block into an import.string of its
// cached module, itself inlined.
func (w *walker) gitBlock(b *ast.BlockStmt, gitDepth, depth int) (*splice, error) {
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
	return &splice{
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
func (w *walker) stringBlock(b *ast.BlockStmt, gitDepth, depth int) (*splice, error) {
	for _, st := range b.Body {
		a, ok := st.(*ast.AttributeStmt)
		if !ok || a.Name.Name != "content" {
			continue
		}
		content, ok := validate.PlainStringLiteral(a.Value)
		if !ok {
			return nil, nil //nolint:nilnil // nothing this walker can rewrite
		}
		inner, err := w.inline(content, gitDepth, depth+1)
		if err != nil {
			return nil, err
		}
		if inner == content {
			return nil, nil //nolint:nilnil // unchanged: the bytes stay as written
		}
		return &splice{
			start: ast.StartPos(a.Value).Position().Offset,
			end:   ast.EndPos(a.Value).Position().Offset + 1,
			text:  alloysyntax.QuoteString(inner),
		}, nil
	}
	return nil, nil //nolint:nilnil // no content attribute
}

// collectImports finds the import blocks Alloy loads — import.git and
// import.string — at the top level and in a declare body, at any declare
// depth, by their parsed names.
func collectImports(body ast.Body, out *[]*ast.BlockStmt, nesting int) {
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
			collectImports(b.Body, out, nesting+1)
		}
	}
}

// ref reads a block's module reference under the lint's rules.
func (w *walker) ref(b *ast.BlockStmt) (Ref, error) {
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
