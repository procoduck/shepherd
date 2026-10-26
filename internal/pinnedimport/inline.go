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
// and its auth blocks are not served. Every other byte of contents is kept:
// text without an import.git block is returned unchanged, so a pipeline that
// uses none serves exactly what it did before. The content is quoted by
// alloysyntax.QuoteString (the Alloy syntax package's encoder).
//
// Only the cache is read, through q. The result is at most
// validate.MaxContentBytes (the pipeline contents cap).
func Inline(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, repos []sqlc.OrgImportRepository, contents string) (string, error) {
	w := newWalker(ctx, q, orgID, repos)
	out, err := w.inline(contents, 0)
	if err != nil {
		return "", err
	}
	if len(out) > validate.MaxContentBytes {
		return "", fmt.Errorf("%w: with its pinned modules inlined the pipeline is %d bytes, more than the %d a "+
			"pipeline may be", ErrNotInlinable, len(out), validate.MaxContentBytes)
	}
	return out, nil
}

// InlineOrg is Inline with orgID's allowlist read through q.
func InlineOrg(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, contents string) (string, error) {
	if !strings.Contains(contents, "import.git") {
		return contents, nil
	}
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
	if _, err := w.inline(contents, 0); err != nil {
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

// inline rewrites text's import.git blocks; depth is how many enclose it.
func (w *walker) inline(text string, depth int) (string, error) {
	if !strings.Contains(text, "import.git") {
		return text, nil
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
	if len(blocks) == 0 {
		return text, nil
	}
	if depth >= validate.MaxGitImportDepth {
		return "", fmt.Errorf("%w: import.git nests more than %d deep", ErrNotInlinable, validate.MaxGitImportDepth)
	}
	// Replace from the last block back, so earlier offsets stay valid.
	slices.SortFunc(blocks, func(a, b *ast.BlockStmt) int {
		return ast.StartPos(b).Position().Offset - ast.StartPos(a).Position().Offset
	})
	out := text
	for _, b := range blocks {
		ref, err := w.ref(b)
		if err != nil {
			if w.collect {
				continue
			}
			return "", err
		}
		if w.collect && !slices.Contains(w.refs, ref) {
			w.refs = append(w.refs, ref)
		}
		row, err := w.q.GetPinnedImportModule(w.ctx, sqlc.GetPinnedImportModuleParams{
			OrgID: w.orgID, RepositoryKey: ref.Key, Revision: ref.Revision, Path: ref.Path,
		})
		if err != nil {
			if w.collect {
				continue
			}
			return "", fmt.Errorf("%w: module %s at %s (%s) is not cached", ErrNotInlinable, ref.Path, ref.Revision, ref.Key)
		}
		module, err := w.inline(row.Content, depth+1)
		if err != nil {
			return "", err
		}
		start := ast.StartPos(b).Position()
		end := ast.EndPos(b).Position().Offset + 1
		indent := strings.Repeat(" ", max(start.Column-1, 0))
		replacement := "import.string " + alloysyntax.QuoteString(b.Label) + " {\n" +
			indent + "  content = " + alloysyntax.QuoteString(module) + "\n" + indent + "}"
		out = out[:start.Offset] + replacement + out[end:]
	}
	return out, nil
}

// collectImports finds the import.git blocks Alloy loads: top-level ones and
// those in a declare body, at any declare depth.
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
		case "import.git":
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
