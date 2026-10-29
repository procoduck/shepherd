package pinnedimport

import (
	"context"
	"crypto/sha256"
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
// fails closed on it: a strict org is never served an import.git. It is also
// a served form that would be more than validate.MaxContentBytes, or that
// expands more than MaxImportInstances imports.
var ErrNotInlinable = errors.New("pinned import.git cannot be inlined")

// MaxImportInstances is how many import blocks (import.git and
// import.string) a strict org's pipeline may expand as served, nested ones
// included and a module imported twice counted twice. The pipelines in use
// have a handful; each instance is a module Alloy loads and runs.
const MaxImportInstances = 256

// Ref is one pinned module, as the cache keys it.
type Ref struct {
	Key, Revision, Path string
}

// moduleQuerier is how the walker reads the cache (*sqlc.Queries).
type moduleQuerier interface {
	GetPinnedImportModule(ctx context.Context, arg sqlc.GetPinnedImportModuleParams) (sqlc.PinnedImportModule, error)
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
// validate.MaxContentBytes (the pipeline contents cap). It is built in two
// walks: the first checks every import (what can be inlined, at most
// MaxImportInstances of them) and builds nothing; the second builds the
// served form directly — each level's modules inlined and drops inserted
// in one pass (validate.BuildServedLevel) — charging one
// validate.ServedBudget of the cap as it goes, so a pipeline whose served
// form would pass the cap is refused when it gets there, never built in
// full, and one whose served form fits is built exactly as before.
func Inline(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, repos []sqlc.OrgImportRepository, contents string) (string, error) {
	return inline(ctx, q, orgID, repos, contents)
}

func inline(ctx context.Context, q moduleQuerier, orgID pgtype.UUID, repos []sqlc.OrgImportRepository, contents string) (string, error) {
	w := newWalker(ctx, q, orgID, repos)
	if err := w.check(contents, 0, 0, false); err != nil {
		return "", err
	}
	out, err := w.build(contents, 0, 0)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrNotInlinable, err)
	}
	// A text the build leaves unchanged charges nothing (it builds
	// nothing), so this is where one over the cap is refused; a changed
	// one over it has already failed the budget.
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
	return references(ctx, q, orgID, repos, contents)
}

func references(ctx context.Context, q moduleQuerier, orgID pgtype.UUID, repos []sqlc.OrgImportRepository, contents string) ([]Ref, error) {
	w := newWalker(ctx, q, orgID, repos)
	w.collect(contents, 0, 0)
	return w.refs, nil
}

type walker struct {
	ctx   context.Context
	q     moduleQuerier
	orgID pgtype.UUID
	repos map[string]bool
	refs  []Ref
	// budget is the served form's (Inline); instances counts the imports
	// the check expands (MaxImportInstances).
	budget    *validate.ServedBudget
	instances int
	// checked are the modules the check has walked, by where: how many
	// imports walking them expanded and how many bytes of served form they
	// make at least. built are the same modules' served form, quoted (part
	// of what the budget is charged). One module imported many times is
	// walked and built once per place; its text is held only while its
	// level is walked or built, never between the walks. visited is
	// References' record of the same.
	checked map[moduleAt]checkedModule
	built   map[moduleAt]string
	visited map[moduleAt]bool
	// inlines records, for each text the check walked (by its SHA-256, so
	// no text is held), whether inlining rewrites it: it holds an
	// import.git, directly or in the content of an import.string it
	// follows. Exactly then the old inliner re-quoted an import.string
	// content (build reads the line ending accordingly).
	inlines map[[sha256.Size]byte]bool
	// atLeast is a lower bound on the served form's length, summed as the
	// check walks (see check): past the cap, the pipeline is refused
	// before anything more is read.
	atLeast int
}

// moduleAt is a module at the depths it is inlined at: what inlining it
// depends on, besides the cache.
type moduleAt struct {
	ref             Ref
	gitDepth, depth int
}

type checkedModule struct {
	instances, atLeast int
}

func newWalker(ctx context.Context, q moduleQuerier, orgID pgtype.UUID, repos []sqlc.OrgImportRepository) *walker {
	w := &walker{
		ctx: ctx, q: q, orgID: orgID, repos: map[string]bool{}, budget: validate.NewServedBudget(),
		checked: map[moduleAt]checkedModule{}, built: map[moduleAt]string{}, visited: map[moduleAt]bool{},
		inlines: map[[sha256.Size]byte]bool{},
	}
	for i := range repos {
		w.repos[repos[i].RepositoryKey] = true
	}
	return w
}

// maxModuleDepth is how many modules (import.git or import.string) Inline
// follows into: the tenant lint's module depth.
const maxModuleDepth = 8

// check walks text's import blocks — found in the parse tree, so whatever
// spelling of the name the parser accepts: each import.git must be
// inlinable (a literal, allowlisted, cached module, within the depths) and
// each import.string with plain literal content is followed into, the same
// way. gitDepth is how many import.git modules enclose text, depth how many
// modules of either kind. It builds nothing; its first refusal is Inline's,
// in the order Inline has always reported them.
//
// It also sums atLeast, a lower bound on the served form's length: every
// level that is served as a text of its own — the pipeline and each
// content it rewrites, and each module instance — adds its bytes outside
// every import block (importBytes). Those bytes appear in the served form,
// once per instance, re-quoted (never shorter); the drops only add to them
// (a forward_to they replace is served again in the loki.process they
// insert); and different levels' counted bytes are disjoint. A pipeline
// whose bound passes the cap is refused at once.
func (w *walker) check(text string, gitDepth, depth int, module bool) error {
	if depth > maxModuleDepth {
		return fmt.Errorf("%w: modules nest more than %d deep", ErrNotInlinable, maxModuleDepth)
	}
	file, err := alloysyntax.ParseFile("<pipeline>", []byte(text))
	if err != nil {
		return fmt.Errorf("%w: the text does not parse: %w", ErrNotInlinable, err)
	}
	var blocks []*ast.BlockStmt
	collectImports(file.Body, &blocks, 0)
	inlines := false
	for _, b := range blocks {
		if err := w.expand(1); err != nil {
			return err
		}
		if strings.Join(b.Name, ".") != "import.git" {
			if a := firstContentAttr(b); a != nil {
				if content, ok := validate.PlainStringLiteral(a.Value); ok {
					if err := w.check(content, gitDepth, depth+1, false); err != nil {
						return err
					}
					inlines = inlines || w.inlines[sha256.Sum256([]byte(content))]
				}
			}
			continue
		}
		inlines = true
		if gitDepth >= validate.MaxGitImportDepth {
			return fmt.Errorf("%w: import.git nests more than %d deep", ErrNotInlinable, validate.MaxGitImportDepth)
		}
		ref, err := w.ref(b)
		if err != nil {
			return err
		}
		at := moduleAt{ref: ref, gitDepth: gitDepth, depth: depth}
		if m, ok := w.checked[at]; ok {
			if err := w.expand(m.instances); err != nil {
				return err
			}
			if err := w.atLeastMore(m.atLeast); err != nil {
				return err
			}
			continue
		}
		content, err := w.module(ref)
		if err != nil {
			return err
		}
		before, beforeBytes := w.instances, w.atLeast
		if err := w.check(content, gitDepth+1, depth+1, true); err != nil {
			return err
		}
		w.checked[at] = checkedModule{instances: w.instances - before, atLeast: w.atLeast - beforeBytes}
	}
	w.inlines[sha256.Sum256([]byte(text))] = inlines
	// A content the inliner leaves alone is part of its parent's kept
	// bytes; the pipeline left alone is held to the cap by Inline.
	if module || inlines {
		return w.atLeastMore(len(text) - importBytes(file.Body, 0))
	}
	return nil
}

// importBytes is how many bytes of a text its import blocks span, at any
// block depth: what the served form may rewrite there (a content the drops
// re-quote may come out shorter than written). The rest is served as it
// is, or with only drops added.
func importBytes(body ast.Body, nesting int) int {
	if nesting > alloysyntax.MaxNesting {
		return 0
	}
	n := 0
	for _, st := range body {
		b, ok := st.(*ast.BlockStmt)
		if !ok {
			continue
		}
		if len(b.Name) > 0 && b.Name[0] == "import" {
			n += ast.EndPos(b).Position().Offset + 1 - ast.StartPos(b).Position().Offset
			continue
		}
		n += importBytes(b.Body, nesting+1)
	}
	return n
}

// atLeastMore adds n bytes to the served form's lower bound, refusing it
// past the cap.
func (w *walker) atLeastMore(n int) error {
	w.atLeast += n
	if w.atLeast > validate.MaxContentBytes {
		return fmt.Errorf("%w: %w", ErrNotInlinable, validate.ErrServedFormTooLarge)
	}
	return nil
}

// firstContent is the value of an import.string's first content attribute,
// when that is a plain literal: what Inline follows into.
func firstContent(b *ast.BlockStmt) (string, bool) {
	if a := firstContentAttr(b); a != nil {
		return validate.PlainStringLiteral(a.Value)
	}
	return "", false
}

// firstContentAttr is an import.string's first content attribute, or nil.
func firstContentAttr(b *ast.BlockStmt) *ast.AttributeStmt {
	for _, st := range b.Body {
		if a, ok := st.(*ast.AttributeStmt); ok && a.Name.Name == "content" {
			return a
		}
	}
	return nil
}

// expand counts n more import instances, refusing more than
// MaxImportInstances.
func (w *walker) expand(n int) error {
	w.instances += n
	if w.instances > MaxImportInstances {
		return fmt.Errorf("%w: as served the pipeline expands more than %d imports (import.git and "+
			"import.string blocks, nested ones and repeats included)", ErrNotInlinable, MaxImportInstances)
	}
	return nil
}

// build is the served form of one checked level, text: the drop pass
// (validate.BuildServedLevel) with the level's import.git blocks served as
// import.string of their module's served form, and its literal
// import.string contents as their own served form, re-quoted when that
// differs — byte for byte what inlining text and then inserting the drops
// made, without building the inlined text. Every piece is charged to the
// budget before it is built.
func (w *walker) build(text string, gitDepth, depth int) (string, error) {
	return validate.BuildServedLevel(text, depth, w.budget, func(file *ast.File) validate.ServedImports {
		var blocks []*ast.BlockStmt
		collectImports(file.Body, &blocks, 0)
		owned := make(map[*ast.BlockStmt]bool, len(blocks))
		var replaced [][2]int
		// The drop pass reads the level's line ending from the text the
		// old inliner left: without each import.git block, and without
		// each import.string content it re-quoted (QuoteString writes no
		// raw CR), which is the first content, a plain literal, exactly
		// when inlining rewrites it (inlines).
		for _, b := range blocks {
			owned[b] = true
			if strings.Join(b.Name, ".") == "import.git" {
				replaced = append(replaced, [2]int{ast.StartPos(b).Position().Offset, ast.EndPos(b).Position().Offset + 1})
				continue
			}
			if a := firstContentAttr(b); a != nil {
				if content, ok := validate.PlainStringLiteral(a.Value); ok && w.inlines[sha256.Sum256([]byte(content))] {
					replaced = append(replaced, [2]int{
						ast.StartPos(a.Value).Position().Offset, ast.EndPos(a.Value).Position().Offset + 1,
					})
				}
			}
		}
		return validate.ServedImports{
			Replaced: replaced,
			Serve: func(b *ast.BlockStmt) ([]validate.Splice, bool, error) {
				if !owned[b] {
					return nil, false, nil
				}
				var splices []validate.Splice
				var err error
				if strings.Join(b.Name, ".") == "import.git" {
					splices, err = w.gitSplice(b, gitDepth, depth)
				} else {
					splices, err = w.stringSplices(b, gitDepth, depth)
				}
				return splices, true, err
			},
		}
	})
}

// quoted is the splice of a nested level's served form, served, in place
// of [start, end): quoted. served is charged; what is served is it quoted,
// at least as long, charged instead before it is built.
func (w *walker) quoted(served string, changed bool) (string, error) {
	if changed {
		w.budget.Give(len(served))
	}
	if err := w.budget.Take(validate.QuotedLen(served)); err != nil {
		return "", err
	}
	return alloysyntax.QuoteString(served), nil
}

// gitSplice serves one checked import.git block as an import.string of its
// cached module's served form.
func (w *walker) gitSplice(b *ast.BlockStmt, gitDepth, depth int) ([]validate.Splice, error) {
	ref, err := w.ref(b)
	if err != nil {
		return nil, err
	}
	at := moduleAt{ref: ref, gitDepth: gitDepth, depth: depth}
	// A module built for an earlier instance is reused; each instance
	// charges its copy below.
	quoted, ok := w.built[at]
	if !ok {
		content, err := w.module(ref)
		if err != nil {
			return nil, err
		}
		served, err := w.build(content, gitDepth+1, depth+1)
		if err != nil {
			return nil, fmt.Errorf("import.string %q: %w", b.Label, err)
		}
		// The quoted module is built once, here, and is part of the first
		// instance's import.string: it must fit before it is built (the
		// instance charges it with the rest of the block below).
		if quoted, err = w.quoted(served, served != content); err != nil {
			return nil, fmt.Errorf("import.string %q: %w", b.Label, err)
		}
		w.budget.Give(len(quoted))
		w.built[at] = quoted
	}
	start := ast.StartPos(b).Position()
	columns := max(start.Column-1, 0)
	const head, open, content, closing = "import.string ", " {\n", "  content = ", "\n"
	n := len(head) + validate.QuotedLen(b.Label) + len(open) + columns + len(content) + len(quoted) +
		len(closing) + columns + 1
	if err := w.budget.Take(n); err != nil {
		return nil, err
	}
	indent := strings.Repeat(" ", columns)
	var sb strings.Builder
	sb.Grow(n)
	for _, s := range []string{head, alloysyntax.QuoteString(b.Label), open, indent, content, quoted, closing, indent, "}"} {
		sb.WriteString(s)
	}
	return []validate.Splice{{
		Start: start.Offset,
		End:   ast.EndPos(b).Position().Offset + 1,
		Text:  sb.String(),
	}}, nil
}

// stringSplices serves one import.string block: each content attribute
// that is a plain literal, up to the first that is not, is replaced by its
// served form, re-quoted, when that differs — the first as a level of its
// own (inlined and dropped), any later one with the drops only, as the drop
// pass always treated them. Other content is left alone (the served form's
// bound, validate.ServedFormProblem, refuses it in a strict org).
func (w *walker) stringSplices(b *ast.BlockStmt, gitDepth, depth int) ([]validate.Splice, error) {
	var splices []validate.Splice
	first := true
	for _, st := range b.Body {
		a, ok := st.(*ast.AttributeStmt)
		if !ok || a.Name.Name != "content" {
			continue
		}
		content, ok := validate.PlainStringLiteral(a.Value)
		if !ok {
			break
		}
		var served string
		var err error
		if first {
			served, err = w.build(content, gitDepth, depth+1)
		} else {
			served, err = validate.BuildServedLevel(content, depth+1, w.budget, nil)
		}
		first = false
		if err != nil {
			return nil, fmt.Errorf("import.string %q: %w", b.Label, err)
		}
		if served == content {
			continue
		}
		quoted, err := w.quoted(served, true)
		if err != nil {
			return nil, fmt.Errorf("import.string %q: %w", b.Label, err)
		}
		splices = append(splices, validate.Splice{
			Start: ast.StartPos(a.Value).Position().Offset,
			End:   ast.EndPos(a.Value).Position().Offset + 1,
			Text:  quoted,
		})
	}
	return splices, nil
}

// collect is References' walk: every import.git reached through cached
// modules and literal import.string contents, recorded in order; what
// cannot be followed is skipped.
func (w *walker) collect(text string, gitDepth, depth int) {
	if depth > maxModuleDepth {
		return
	}
	file, err := alloysyntax.ParseFile("<pipeline>", []byte(text))
	if err != nil {
		return
	}
	var blocks []*ast.BlockStmt
	collectImports(file.Body, &blocks, 0)
	for _, b := range blocks {
		if strings.Join(b.Name, ".") != "import.git" {
			if content, ok := firstContent(b); ok {
				w.collect(content, gitDepth, depth+1)
			}
			continue
		}
		if gitDepth >= validate.MaxGitImportDepth {
			continue
		}
		ref, err := w.ref(b)
		if err != nil {
			continue
		}
		if !slices.Contains(w.refs, ref) {
			w.refs = append(w.refs, ref)
		}
		// A second visit at the same depths lists nothing new.
		at := moduleAt{ref: ref, gitDepth: gitDepth, depth: depth}
		if w.visited[at] {
			continue
		}
		w.visited[at] = true
		content, err := w.module(ref)
		if err != nil {
			continue
		}
		w.collect(content, gitDepth+1, depth+1)
	}
}

// module is ref's cached text.
func (w *walker) module(ref Ref) (string, error) {
	row, err := w.q.GetPinnedImportModule(w.ctx, sqlc.GetPinnedImportModuleParams{
		OrgID: w.orgID, RepositoryKey: ref.Key, Revision: ref.Revision, Path: ref.Path,
	})
	if err != nil {
		return "", fmt.Errorf("%w: module %s at %s (%s) is not cached", ErrNotInlinable, ref.Path, ref.Revision, ref.Key)
	}
	return row.Content, nil
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
