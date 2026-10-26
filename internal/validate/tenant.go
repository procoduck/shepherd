package validate

import (
	"context"
	"errors"
	"fmt"
	pathpkg "path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/grafana/alloy/syntax/ast"
	"github.com/grafana/alloy/syntax/token"

	"shepherd/internal/alloysyntax"
)

// TenantPolicy is strict tenant mode as the validation gate sees it. The zero
// value is "not strict": TenantLint finds nothing and ValidatePipeline is
// exactly Stages 1-2.
type TenantPolicy struct {
	// Strict turns the tenant lint on (the org's strict_tenants flag, or the
	// server-wide tenants.require_owned setting).
	Strict bool
	// Owns reports whether the org owns tenant (gateway.OrgOwnsTenant over
	// the org's tenants). Required when Strict is set; a nil Owns owns
	// nothing.
	Owns func(tenant string) bool
	// Git is where the lint reads import.git modules from: a strict org may
	// use import.git pinned to a full commit SHA of a repository on its
	// allowlist, and the fetched module is linted with the same rules
	// (#390). Nil refuses every import.git.
	Git GitModules
	// Inline builds what a strict org is served for a pipeline's text: each
	// pinned import.git rewritten into an import.string of the module text
	// the lint checked (pinnedimport.Inline). ValidatePipeline validates
	// that form too. Nil: no served form is checked (a non-strict policy, or
	// a caller with nothing to inline from).
	Inline func(ctx context.Context, contents string) (string, error)
	// servedDrops is set by ValidatePipeline and WithTenantLint, and only
	// there, on the policy they lint the stored text with when they lint the
	// served form too (Strict and Inline set): a labelmap that may write
	// __tenant_id__ is then left to the served form, where Inline inserts
	// the drop after it (InsertTenantDrops) and the lint, without this flag,
	// refuses it unless the drop is there (#410).
	servedDrops bool
}

// storedTextPolicy is tenants as ValidatePipeline and WithTenantLint lint the
// stored text with (servedDrops).
func storedTextPolicy(tenants TenantPolicy) TenantPolicy {
	tenants.servedDrops = tenants.Strict && tenants.Inline != nil
	return tenants
}

// GitModule is one import.git module: its repository, revision and path as
// the pipeline spells them.
type GitModule struct {
	Repository, Revision, Path string
}

// GitModules is where strict tenant mode's lint reads import.git modules
// from (internal/pinnedimport). The lint itself never waits on it: between
// runs, TenantLintChecked fetches every module the previous run needed.
type GitModules interface {
	// Allowed returns nil when repository is on the org's allowlist, else
	// the refusal.
	Allowed(repository string) error
	// Fetch returns the module's text. A non-empty warning is reported as a
	// warning diagnostic on the import (the text was checked against a cached
	// copy because its source is unreachable); err refuses the import.
	Fetch(ctx context.Context, m GitModule) (content, warning string, err error)
}

// MaxGitImportDepth is how deep import.git may nest: a pipeline's import is
// depth 1, an import.git inside that module depth 2, and so on.
const MaxGitImportDepth = 4

// maxGitModules is how many distinct import.git modules one lint run may
// fetch, across every depth.
const maxGitModules = 16

// fullCommitSHA is the only revision strict mode accepts for import.git.
var fullCommitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// gitFetch is one fetched module, or why it could not be.
type gitFetch struct {
	content, warning string
	err              error
}

// gitState is what one TenantLintChecked call has fetched so far, shared by
// every run and every module it lints; needed is what the current run asked
// for that it does not have yet.
type gitState struct {
	fetched map[GitModule]gitFetch
	needed  []GitModule
	asked   map[GitModule]bool
}

func (g *gitState) need(m GitModule) {
	if g.asked[m] {
		return
	}
	g.asked[m] = true
	g.needed = append(g.needed, m)
}

func (p TenantPolicy) owns(tenant string) bool {
	return p.Owns != nil && p.Owns(tenant)
}

// tenantHeader is the header Grafana Mimir, Loki, Tempo and Pyroscope read a
// tenant from. Header names are case-insensitive.
const tenantHeader = "X-Scope-OrgID"

// tenantLabel is Loki's reserved label that overrides loki.write's tenant_id
// for an entry (Alloy v1.20.1 internal/component/common/loki/client:
// ReservedLabelTenantID, read by getTenantID). Label names are
// case-sensitive. prometheus.remote_write has no such label: Mimir takes the
// tenant from the X-Scope-OrgID header only.
const tenantLabel = "__tenant_id__"

// tenantAttrs are the places, besides headers, otelcol.auth.* and the
// __tenant_id__ label, where an Alloy component is given a tenant to write
// (or sync rules) under: component name -> the LAST block of the path inside
// the component ("" for the component's own body; any nesting above it, such
// as loki.process's stage.match, is allowed) -> attribute. Azure AD's
// tenant_id attributes name an identity tenant, not a data tenant
// (tenant_schema_test.go classifies every attribute named like a tenant or a
// header).
var tenantAttrs = map[string]map[string]string{
	"loki.write":             {"endpoint": "tenant_id"},
	"loki.rules.kubernetes":  {"": "tenant_id"},
	"mimir.rules.kubernetes": {"": "tenant_id"},
	"loki.process":           {"stage.tenant": "value"},
}

// tenantFromData are attributes that pick a tenant at run time from the data
// itself (a log line's label or extracted field). In strict mode they are
// refused: data pushed by a client (loki.source.api, a receiver) can carry
// any tenant.
var tenantFromData = map[string]map[string][]string{
	"loki.process": {"stage.tenant": {"label", "source"}},
}

// clientTenantHeader is how a push source below gets the client's tenant.
const clientTenantHeader = "forwards the tenant a client sends (X-Scope-OrgID, as " + tenantLabel + ")"

// pushSources are the components whose output may carry a __tenant_id__
// label the client chose, with how. The Loki push sources turn a client's
// X-Scope-OrgID into it (Alloy v1.20.1: loki/source/api/routes.go,
// heroku/routes.go, aws_firehose/routes.go,
// gcplog/internal/gcplogtarget/push_translation.go). otelcol.exporter.loki
// turns the OTLP resource and log attributes the loki.resource.labels and
// loki.attribute.labels hints name into labels, keeping a name such as
// __tenant_id__ as it is (otel-collector-contrib pkg/translator/loki,
// LogToLokiEntry; checked against Alloy v1.21.2, whose loki.echo shows the
// label). A loki.write then sends each entry under the CLIENT's tenant unless
// the label is overwritten or dropped on the way (pushSourcePaths).
// otelcol.exporter.prometheus is not one: Mimir reads the tenant only from
// the X-Scope-OrgID header, never a label.
var pushSources = map[string]string{
	"loki.source.api":         clientTenantHeader,
	"loki.source.heroku":      clientTenantHeader,
	"loki.source.awsfirehose": clientTenantHeader,
	"loki.source.gcplog":      clientTenantHeader,
	"otelcol.exporter.loki": "turns OTLP attributes a client sends into labels, which may include " + tenantLabel +
		" (the loki.resource.labels and loki.attribute.labels hints), and forwards them",
}

// rulesExporters are the components whose `rules` export a relabel rules
// attribute (RelabelRuleAttributes) may refer to in strict tenant mode: their
// rule blocks are linted like any other, and the served form drops
// __tenant_id__ after their labelmaps (InsertTenantDrops).
var rulesExporters = map[string]bool{"loki.relabel": true, "discovery.relabel": true}

// corsHeaderLists are attributes ending in "headers" that list header NAMES
// a receiver allows (CORS), not headers sent: they carry no tenant.
var corsHeaderLists = map[string]bool{"allowed_headers": true, "exposed_headers": true}

// isHeadersMapAttr reports whether an attribute called name is a map of
// request headers the lint reads X-Scope-OrgID from: any attribute whose
// name ends in "headers" (headers, http_headers, …) except the CORS lists,
// and proxy_connect_header.
func isHeadersMapAttr(name string) bool {
	n := strings.ToLower(name)
	return (strings.HasSuffix(n, "headers") && !corsHeaderLists[n]) || n == "proxy_connect_header"
}

// The lint's bounds. Past any of them a value is unreadable: refused in
// strict mode, "incomplete" for TenantLiterals. They exist because values
// fold: a concatenation of declare arguments, each passed down another
// level, grows exponentially with the nesting — 4 KB of text once built a
// 200 MB string.
const (
	// maxFolded is how many values one expression may fold to.
	maxFolded = 64
	// maxValueBytes is the longest value a concatenation may build; a string
	// literal of the text itself is not limited (the text bounds it).
	maxValueBytes = 4 << 10
	// maxFoldedBytes is the most a concatenation's values may total.
	maxFoldedBytes = 16 << 10
	// maxDepth is how many declare argument hops and nested import.string
	// modules the lint follows.
	maxDepth = 8
	// maxNesting is the deepest block, expression or forward_to chain the
	// lint walks.
	maxNesting = 64
	// maxSteps is the ONE budget of a lint run, shared with the modules it
	// imports: every unit of work charges it (each statement and block, each
	// resolution and cache hit, each value and tenant piece, each byte a
	// concatenation builds per 64, each diagnostic, each path step). Worst
	// case about 10 ms of CPU and a few MB.
	maxSteps = 100_000
	// maxContentBytes is the largest text the lint reads at all — the
	// pipeline contents cap (mgmtapi.MaxPipelineContentsBytes), so a larger
	// one is never saved; parsing is linear in the text.
	maxContentBytes = MaxContentBytes
	// maxLiterals is how many distinct tenants one pipeline may be recorded
	// as writing to (pipeline_tenants); more is "incomplete".
	maxLiterals = 256
	// maxDiagnostics is how many refusals one run reports, plus one summary.
	maxDiagnostics = 100
)

// MaxContentBytes is the largest pipeline text the tenant lint reads, and the
// most a strict org's pipeline may be with its pinned modules inlined
// (pinnedimport.Inline): the pipeline contents cap.
const MaxContentBytes = 1 << 20

// lintDeadline is the wall-clock guard of one run. It is a crash guard, not
// a verdict: a run that hits it is "busy" (ErrLintBusy — retry), never a
// refusal and never an "incomplete" index row, so CPU throttling or a GC
// pause cannot decide whether a pipeline is accepted. Step exhaustion
// (maxSteps) is the deterministic bound.
var lintDeadline = 50 * time.Millisecond

// SetTenantLintDeadline replaces lintDeadline until the returned restore is
// called. For tests of the busy path (a deadline of 0 times out every run).
func SetTenantLintDeadline(d time.Duration) (restore func()) {
	saved := lintDeadline
	lintDeadline = d
	return func() { lintDeadline = saved }
}

// ErrLintBusy is a tenant check that hit its wall-clock guard: no verdict.
// A write answers it with a retryable Unavailable; the pipeline tenant index
// keeps the row it had.
var ErrLintBusy = errors.New("validation busy: the tenant check did not finish in time; try again")

// TenantLint is strict tenant mode's pipeline check (spec §8). With p.Strict
// it refuses, wherever Alloy sets a tenant, every tenant content can write
// to that the org does not own, and everything there it cannot verify (see
// spec §8 for the list). A non-strict policy finds nothing. Content that
// does not parse yields nothing — Stage 1 reports it. Positions are
// content's own. Stage is 1. The lint is defence in depth: the backend's own
// tenant authentication is the boundary. It is bounded (maxSteps and the
// other caps), so no text makes it slow.
func TenantLint(content string, p TenantPolicy) []Diagnostic {
	diags, err := TenantLintChecked(context.Background(), content, p)
	if err != nil {
		return []Diagnostic{{Line: 1, Col: 1, Stage: 1, Message: err.Error()}}
	}
	return diags
}

// TenantLintChecked is TenantLint that reports a run which hit the
// wall-clock guard as ErrLintBusy, with no diagnostics, instead of a
// refusal: the gate turns it into a retryable "busy".
//
// With p.Git set it reads pinned import.git modules: each run of the lint
// (bounded as always) lists the modules it needs and has not got; they are
// fetched (p.Git.Fetch, under ctx) between runs and the lint runs again, at
// most once per level of nesting (MaxGitImportDepth) and for at most
// maxGitModules modules in all. The fetch is the only part that waits on
// the network, and the module source bounds it.
func TenantLintChecked(ctx context.Context, content string, p TenantPolicy) ([]Diagnostic, error) {
	if !p.Strict {
		return nil, nil
	}
	l, err := runLint(ctx, content, p)
	if err != nil || l == nil {
		return nil, err
	}
	return l.diags, nil
}

// runLint is the lint with its fetch rounds (see TenantLintChecked): nil
// for content that does not parse.
func runLint(ctx context.Context, content string, p TenantPolicy) (*tenantLinter, error) {
	// Read the component schemas (once per process) before the wall-clock
	// guard starts: the first read parses every shipped schema, which is
	// not the lint's work. Without them there is no verdict (fail closed).
	if _, err := RelabelRuleAttributes(); err != nil {
		return nil, fmt.Errorf("the tenant check cannot read the component schemas: %w", err)
	}
	if _, err := labelMapAttributes(); err != nil {
		return nil, fmt.Errorf("the tenant check cannot read the component schemas: %w", err)
	}
	git := &gitState{fetched: map[GitModule]gitFetch{}}
	for round := 0; ; round++ {
		git.needed, git.asked = nil, map[GitModule]bool{}
		b := newBudget()
		l := newTenantLinter(content, p, nil, 0, 0, git, b)
		if b.timedOut {
			return nil, ErrLintBusy
		}
		if l == nil {
			return nil, nil
		}
		if len(git.needed) == 0 || p.Git == nil || round > MaxGitImportDepth {
			return l, nil
		}
		for _, m := range git.needed {
			if len(git.fetched) >= maxGitModules {
				git.fetched[m] = gitFetch{err: fmt.Errorf("the pipeline imports more than %d modules, more than "+
					"the tenant check fetches", maxGitModules)}
				continue
			}
			content, warning, err := p.Git.Fetch(ctx, m)
			git.fetched[m] = gitFetch{content: content, warning: warning, err: err}
		}
	}
}

// TenantLiterals lists every tenant content writes to that the lint can
// read, whatever the org's policy — what pipeline_tenants records for an
// enabled pipeline — and whether that is all of them: complete is false when
// something there may set a tenant the lint cannot read (a computed value, an
// external import, a cap reached). Content that does not parse lists nothing
// and is complete.
//
// err is ErrLintBusy when the run hit the wall-clock guard: the result means
// nothing, and the caller keeps what it had.
func TenantLiterals(content string) (literals []string, complete bool, err error) {
	return TenantLiteralsWith(context.Background(), content, nil)
}

// TenantLiteralsWith is TenantLiterals reading pinned import.git modules from
// git (nil: none, so a pipeline with one is incomplete): a strict org's
// pipeline whose modules are cached is recorded with their tenants.
func TenantLiteralsWith(ctx context.Context, content string, git GitModules) (literals []string, complete bool, err error) {
	l, err := runLint(ctx, content, TenantPolicy{Strict: true, Owns: func(string) bool { return true }, Git: git})
	if err != nil {
		return nil, false, err
	}
	if l == nil {
		return nil, true, nil
	}
	return l.literals, len(l.diags) == 0 && !l.incomplete, nil
}

// resolved is a value as the lint read it: its possible strings, or ok=false.
type resolved struct {
	values []string
	ok     bool
}

// budget is the work left to one lint run and the modules it imports
// (maxSteps, lintDeadline). Once exhausted it stays exhausted, and every
// value read after that is unreadable.
type budget struct {
	steps    int
	calls    int
	deadline time.Time
	// exhausted is maxSteps spent: deterministic, the rest is unreadable.
	exhausted bool
	// timedOut is the wall-clock guard: the run has no verdict at all.
	timedOut bool
}

func newBudget() *budget {
	return &budget{steps: maxSteps, deadline: time.Now().Add(lintDeadline)}
}

// charge spends n units, reporting whether any are left.
func (b *budget) charge(n int) bool {
	if b.exhausted || b.timedOut {
		return false
	}
	b.steps -= n
	b.calls++
	switch {
	case b.steps < 0:
		b.exhausted = true
	case b.calls%256 == 0 && time.Now().After(b.deadline):
		b.timedOut = true
	}
	return !b.exhausted && !b.timedOut
}

func (b *budget) take() bool { return b.charge(1) }

func newTenantLinter(content string, p TenantPolicy, external map[string]map[string][]resolved, depth, gitDepth int, git *gitState, b *budget) *tenantLinter {
	l := &tenantLinter{
		policy: p, declares: map[string]*declareInfo{}, depth: depth, gitDepth: gitDepth, git: git, budget: b,
		memo: map[string]*resolved{}, components: map[string]map[string]*ast.BlockStmt{},
		seenLiteral: map[string]bool{},
	}
	if len(content) > maxContentBytes {
		l.diags = append(l.diags, Diagnostic{Line: 1, Col: 1, Stage: 1, Message: fmt.Sprintf(
			"the pipeline is %d bytes, more than the %d the tenant check reads — strict tenant mode refuses it",
			len(content), maxContentBytes)})
		return l
	}
	// The parse does not count against the wall-clock guard: it is bounded
	// on its own (alloysyntax's nesting prescan, maxContentBytes) and linear,
	// but a 1 MiB expression chain takes tens of milliseconds, which would
	// make such a pipeline "busy" on every run. The guard is moved on by
	// however long this parse took (a module's too).
	parseStart := time.Now()
	file, err := alloysyntax.ParseFile("<pipeline>", []byte(content))
	b.deadline = b.deadline.Add(time.Since(parseStart))
	if err != nil {
		return nil
	}
	l.collectDeclares(file.Body, "", external)
	l.collectComponents(file.Body, "", 0)
	l.body(file.Body, scope{})
	l.pushSourcePaths()
	if depth == 0 && !b.timedOut && time.Now().After(b.deadline) {
		b.timedOut = true
	}
	if depth == 0 && (b.exhausted || l.tooMany) {
		msg := "the pipeline is too large or complex for the tenant check to verify within its limits — strict " +
			"tenant mode refuses it; simplify the tenant settings"
		if !b.exhausted {
			msg = fmt.Sprintf("more than %d tenant findings; the first %d are shown", maxDiagnostics, maxDiagnostics)
		}
		l.diags = append(l.diags, Diagnostic{Line: 1, Col: 1, Stage: 1, Message: msg})
	}
	return l
}

type tenantLinter struct {
	policy   TenantPolicy
	diags    []Diagnostic
	literals []string
	// declares are keyed by scope: "<label>" at the top level,
	// "<outer>/<label>" inside declare <outer>.
	declares map[string]*declareInfo
	// components are each scope's component blocks by "<name>.<label>".
	components map[string]map[string]*ast.BlockStmt
	// pushes are the push sources found, with their scope.
	pushes []pushSource
	memo   map[string]*resolved
	depth  int
	budget *budget
	// gitDepth is how many import.git modules enclose this text; git is
	// what the TenantLintChecked call has fetched (nil: none can be).
	gitDepth int
	git      *gitState
	// exprNesting is how deep resolve is in one expression.
	exprNesting int
	// seenLiteral is literals as a set; incomplete is set when there were
	// more than maxLiterals; tooMany when there were more than
	// maxDiagnostics refusals.
	seenLiteral map[string]bool
	incomplete  bool
	tooMany     bool
}

// addLiteral records one tenant the text writes to, up to maxLiterals.
func (l *tenantLinter) addLiteral(t string) {
	if l.seenLiteral[t] {
		return
	}
	if len(l.literals) >= maxLiterals {
		l.incomplete = true
		return
	}
	l.seenLiteral[t] = true
	l.literals = append(l.literals, t)
}

type pushSource struct {
	block *ast.BlockStmt
	scope string
}

// declareInfo is one declare block's arguments: each one's default, and
// every value an instantiation gives it, with the declare it is given in.
type declareInfo struct {
	defaults map[string]ast.Expr
	given    map[string][]givenValue
	// external are values an instantiation outside this text gives (an
	// import.string module's declare, instantiated by the importing text).
	external map[string][]resolved
}

type givenValue struct {
	expr    ast.Expr
	declare string
}

// scope is where the walk is: the nearest enclosing component the lint has
// rules for and the block path inside it, and the key of the nearest
// enclosing declare.
type scope struct {
	component string
	path      *pathNode
	declare   string
	// declDepth is how many declares enclose the scope.
	declDepth int
	// nested is set inside any block other than a declare (a foreach
	// template, a component), declares within it included: an import
	// block there is refused.
	nested bool
	// owner is the component whose body the walk is in (any component, not
	// only one the lint has rules for), and ownerDepth the path depth of
	// that body: labelMapKind looks attributes up by them.
	owner      string
	ownerDepth int
	// foreachBody is set directly in a foreach block's body, and template
	// in its template block's: blocks there are components, as at the top
	// level.
	foreachBody, template bool
}

// ownerBlock is the component the walk is in and the last block of the
// path inside it ("" for the component's own body).
func (s scope) ownerBlock() (owner, block string) {
	owner = s.owner
	if s.component != "" {
		owner = s.component
	}
	if owner == "" || s.path == nil || s.path.depth <= s.ownerDepth {
		return owner, ""
	}
	return owner, s.path.name
}

// componentPosition reports whether a block in this scope is a component:
// at the top level, directly in a declare, or in a foreach template.
func (s scope) componentPosition() bool {
	return s.component == "" && (s.path == nil || s.template)
}

// pathNode is one block of a scope's path, pointing at its parent: a child
// scope costs one node, not a copy of the path, and the path is only spelt
// out (where) for a refusal.
type pathNode struct {
	name   string
	parent *pathNode
	depth  int
}

func (s scope) last() string {
	if s.path == nil {
		return ""
	}
	return s.path.name
}

func (s scope) nesting() int {
	if s.path == nil {
		return 0
	}
	return s.path.depth
}

func (s scope) child(name string) scope {
	return scope{
		component: s.component, path: &pathNode{name: name, parent: s.path, depth: s.nesting() + 1},
		declare: s.declare, declDepth: s.declDepth, nested: s.nested, owner: s.owner, ownerDepth: s.ownerDepth,
	}
}

// where spells out "<component> <block>… <attr>" for a refusal.
func (s scope) where(attr string) string {
	var parts []string
	for n := s.path; n != nil; n = n.parent {
		parts = append(parts, n.name)
	}
	out := []string{s.component}
	for i := len(parts) - 1; i >= 0; i-- {
		out = append(out, parts[i])
	}
	return strings.Join(append(out, attr), " ")
}

func declareKey(parent, label string) string {
	if parent == "" {
		return label
	}
	return parent + "/" + label
}

// lookupDeclare finds the declare an instantiation named name in scope
// refers to: the innermost one of that label visible there.
func (l *tenantLinter) lookupDeclare(scopeKey, name string) *declareInfo {
	for k := scopeKey; ; {
		if info, ok := l.declares[declareKey(k, name)]; ok {
			return info
		}
		if k == "" {
			return nil
		}
		if i := strings.LastIndex(k, "/"); i >= 0 {
			k = k[:i]
		} else {
			k = ""
		}
	}
}

// collectDeclares records every declare block's arguments (keyed by scope),
// then every instantiation of a declare and the attributes it sets, with the
// declare the instantiation sits in.
func (l *tenantLinter) collectDeclares(stmts ast.Body, parent string, external map[string]map[string][]resolved) {
	l.collectDeclaresAt(stmts, parent, external, 0)
}

// collectDeclaresAt is collectDeclares at a nesting level: it stops at
// maxNesting (the body walk refuses deeper blocks) and charges each key it
// builds by its length, since a key spells out every enclosing label.
func (l *tenantLinter) collectDeclaresAt(stmts ast.Body, parent string, external map[string]map[string][]resolved, nesting int) {
	if nesting > maxNesting {
		return
	}
	for _, st := range stmts {
		if !l.budget.take() {
			return
		}
		blk, ok := st.(*ast.BlockStmt)
		if !ok || strings.Join(blk.Name, ".") != "declare" || blk.Label == "" {
			continue
		}
		if !l.budget.charge(1 + (len(parent)+len(blk.Label))/64) {
			return
		}
		key := declareKey(parent, blk.Label)
		info := &declareInfo{defaults: map[string]ast.Expr{}, given: map[string][]givenValue{}}
		if parent == "" {
			info.external = external[blk.Label]
		}
		for _, inner := range blk.Body {
			arg, ok := inner.(*ast.BlockStmt)
			if !ok || strings.Join(arg.Name, ".") != "argument" {
				continue
			}
			info.defaults[arg.Label] = attrsOf(arg)["default"]
		}
		l.declares[key] = info
		l.collectDeclaresAt(blk.Body, key, nil, nesting+1)
	}
	if parent != "" {
		return
	}
	// inst records every instantiation. One inside a foreach template
	// (opaque) runs once per collection item, with the template's var in
	// scope, so what it gives an argument is recorded as unreadable (a nil
	// expression): an argument it sets cannot be read, and strict mode
	// refuses what depends on it.
	var inst func(ast.Body, string, int, bool)
	inst = func(b ast.Body, declare string, nesting int, opaque bool) {
		if nesting > maxNesting {
			return
		}
		for _, st := range b {
			if !l.budget.take() {
				return
			}
			blk, ok := st.(*ast.BlockStmt)
			if !ok {
				continue
			}
			name := strings.Join(blk.Name, ".")
			if name == "declare" {
				if !l.budget.charge(1 + (len(declare)+len(blk.Label))/64) {
					return
				}
				inst(blk.Body, declareKey(declare, blk.Label), nesting+1, opaque)
				continue
			}
			if name == "foreach" {
				for _, t := range blk.Body {
					if tb, ok := t.(*ast.BlockStmt); ok && strings.Join(tb.Name, ".") == "template" {
						inst(tb.Body, declare, nesting+2, true)
					}
				}
				continue
			}
			if !l.budget.charge(1 + len(declare)/64) { // lookupDeclare slices the key
				return
			}
			if info := l.lookupDeclare(declare, name); info != nil {
				for _, a := range blk.Body {
					if at, ok := a.(*ast.AttributeStmt); ok {
						g := givenValue{expr: at.Value, declare: declare}
						if opaque {
							g.expr = nil
						}
						info.given[at.Name.Name] = append(info.given[at.Name.Name], g)
					}
				}
			}
		}
	}
	inst(stmts, "", 0, false)
}

// collectComponents indexes each scope's top-level component blocks by
// "<name>.<label>", for following forward_to.
//
// Components in a foreach template are indexed into the scope the foreach is
// in, since Alloy lets them refer to it. A name two components there share
// (one in a template, one beside it) is indexed as nil, which no lookup
// follows: a forward_to to it is refused, never guessed.
func (l *tenantLinter) collectComponents(stmts ast.Body, scopeKey string, nesting int) {
	m := map[string]*ast.BlockStmt{}
	l.components[scopeKey] = m
	l.collectComponentsInto(stmts, scopeKey, m, nesting)
}

func (l *tenantLinter) collectComponentsInto(stmts ast.Body, scopeKey string, m map[string]*ast.BlockStmt, nesting int) {
	if nesting > maxNesting {
		return
	}
	for _, st := range stmts {
		if !l.budget.take() {
			return
		}
		blk, ok := st.(*ast.BlockStmt)
		if !ok {
			continue
		}
		name := strings.Join(blk.Name, ".")
		if name == "declare" {
			l.collectComponents(blk.Body, declareKey(scopeKey, blk.Label), nesting+1)
			continue
		}
		if name == "foreach" {
			for _, inner := range blk.Body {
				if t, ok := inner.(*ast.BlockStmt); ok && strings.Join(t.Name, ".") == "template" {
					l.collectComponentsInto(t.Body, scopeKey, m, nesting+2)
				}
			}
			continue
		}
		if blk.Label != "" {
			key := name + "." + blk.Label
			if _, dup := m[key]; dup {
				m[key] = nil
			} else {
				m[key] = blk
			}
		}
		if _, ok := pushSources[name]; ok {
			l.pushes = append(l.pushes, pushSource{block: blk, scope: scopeKey})
		}
	}
}

func (l *tenantLinter) body(stmts ast.Body, sc scope) {
	for i, st := range stmts {
		if !l.budget.take() {
			return
		}
		switch n := st.(type) {
		case *ast.BlockStmt:
			name := strings.Join(n.Name, ".")
			atTop := sc.component == "" && sc.path == nil
			importable := atTop && !sc.nested
			// A component: at the top level, in a declare, or in a foreach
			// template, where Alloy runs it like any other.
			compPos := sc.componentPosition()
			if sc.nesting() >= maxNesting || sc.declDepth >= maxNesting {
				l.refuse(n, fmt.Sprintf("blocks nested more than %d deep cannot be verified — strict tenant mode refuses them", maxNesting))
				continue
			}
			l.rulesAttributes(n, name, sc)
			if compPos {
				l.enrichLabels(n, name)
				l.secretfilterOrigin(n, name, sc)
			}
			l.groupLabels(n, name, sc)
			switch {
			case name == "declare":
				if !l.budget.charge(1 + (len(sc.declare)+len(n.Label))/64) {
					return
				}
				l.body(n.Body, scope{declare: declareKey(sc.declare, n.Label), declDepth: sc.declDepth + 1, nested: !importable})
			case importable && strings.HasPrefix(name, "import."):
				l.importBlock(n, name, sc, stmts)
			case strings.HasPrefix(name, "import."):
				// Alloy loads an import block inside another block too (a
				// foreach template); the lint follows modules only from the
				// top level and a declare body, so any other is refused.
				l.refuse(n, fmt.Sprintf("%s %q is nested in another block, where strict tenant mode cannot verify "+
					"what it loads — strict tenant mode refuses it; import at the top level or in a declare", name, n.Label))
			case compPos && (isTenantComponent(name) || name == "otelcol.auth.headers"):
				l.body(n.Body, scope{component: name, declare: sc.declare, declDepth: sc.declDepth, nested: sc.nested})
			case compPos && name == "otelcol.auth.bearer":
				l.bearer(n, sc)
				l.body(n.Body, scope{component: name, declare: sc.declare, declDepth: sc.declDepth, nested: sc.nested})
			default:
				if sc.component == "otelcol.auth.headers" && sc.path == nil && name == "header" {
					l.authHeader(n, sc)
				}
				if name == "rule" {
					l.relabelRule(n, sc, nextRule(stmts, i))
				}
				child := sc.child(name)
				switch {
				case compPos && name == "foreach":
					child.foreachBody = true
				case sc.foreachBody && name == "template":
					child.template = true
				case compPos:
					child.owner, child.ownerDepth = name, child.nesting()
				}
				l.body(n.Body, child)
			}
		case *ast.AttributeStmt:
			l.attribute(n, sc)
		}
	}
}

func isTenantComponent(name string) bool {
	_, a := tenantAttrs[name]
	_, b := tenantFromData[name]
	return a || b
}

func (l *tenantLinter) attribute(a *ast.AttributeStmt, sc scope) {
	name := a.Name.Name
	// The block the attribute is directly in: the last block of the path, or
	// the tenant component itself. A relabel rules attribute is checked with
	// its block (rulesAttributes).
	enclosing := sc.last()
	if sc.path == nil {
		enclosing = sc.component
	}
	if isRulesAttribute(enclosing, name) {
		return
	}
	if isHeadersMapAttr(name) {
		l.headers(a.Value, sc)
		return
	}
	l.labelMaps(a.Value, sc)
	l.labelMapAttribute(a, sc)
	if sc.component == "" {
		return
	}
	if attr, ok := tenantAttrs[sc.component][sc.last()]; ok && attr == name {
		l.tenantValue(a.Value, func() string { return sc.where(name) }, sc)
		return
	}
	for _, dyn := range tenantFromData[sc.component][sc.last()] {
		if dyn == name {
			l.refuse(a.Value, sc.where(name)+" picks the tenant from the data at run time, which a client can set — "+
				"strict tenant mode refuses it")
		}
	}
}

// isRulesAttribute reports whether attribute attr of a block named block is
// a relabel rules attribute (RelabelRuleAttributes). runLint has read the
// schemas before any lint runs; were they unreadable, every attribute whose
// name ends in "rules" would be taken for one.
func isRulesAttribute(block, attr string) bool {
	rules, err := RelabelRuleAttributes()
	if err != nil {
		return strings.HasSuffix(attr, "rules")
	}
	return rules[block][attr]
}

// rulesAttributes checks every relabel rules attribute of block b (named
// name): in strict tenant mode it must be a plain reference to the `rules`
// export of a loki.relabel or discovery.relabel (rulesExporters) in the same
// scope — the pipeline's top level or the same declare — whose rule blocks
// the lint checks and the served form drops __tenant_id__ after. Rules
// written in place (a list or object literal, a function call, a
// concatenation, a declare argument), another component's export, and a
// component it cannot find there are refused: the lint cannot check them.
func (l *tenantLinter) rulesAttributes(b *ast.BlockStmt, name string, sc scope) {
	for _, st := range b.Body {
		a, ok := st.(*ast.AttributeStmt)
		if !ok || !isRulesAttribute(name, a.Name.Name) {
			continue
		}
		if !l.budget.take() {
			return
		}
		where := fmt.Sprintf("%s %q %s", name, b.Label, a.Name.Name)
		parts, ok := accessChain(a.Value)
		if !ok || len(parts) != 4 || parts[3] != "rules" || !rulesExporters[parts[0]+"."+parts[1]] {
			l.refuse(a.Value, where+" is not a reference to a loki.relabel component's rules, so strict tenant mode "+
				"cannot check the rules and refuses it; move the rules into a loki.relabel component's rule blocks "+
				"and set "+a.Name.Name+" = loki.relabel.<label>.rules")
			continue
		}
		if l.components[sc.declare][strings.Join(parts[:3], ".")] == nil {
			l.refuse(a.Value, fmt.Sprintf("%s refers to %s, which is not in the same pipeline or declare, so strict "+
				"tenant mode cannot check its rules and refuses it; move the rules into a loki.relabel component's "+
				"rule blocks beside it", where, strings.Join(parts[:3], ".")))
		}
	}
}

// headers checks a headers attribute's value. A string or list literal is
// not a header map (beyla's policy action, kafka's header list); an object
// literal is checked key by key, a list value element by element
// (http_headers); anything else may carry the header and cannot be read.
func (l *tenantLinter) headers(v ast.Expr, sc scope) {
	switch e := unparen(v).(type) {
	case *ast.LiteralExpr, *ast.ArrayExpr:
		return
	case *ast.ObjectExpr:
		for _, f := range e.Fields {
			if !strings.EqualFold(f.Name.Name, tenantHeader) {
				continue
			}
			if arr, ok := unparen(f.Value).(*ast.ArrayExpr); ok {
				for _, el := range arr.Elements {
					l.tenantValue(el, fixed(tenantHeader+" header"), sc)
				}
				continue
			}
			l.tenantValue(f.Value, fixed(tenantHeader+" header"), sc)
		}
	default:
		// A declare argument: every map it is given, each read the same way.
		maps, ok := l.foldMaps(v, sc, 0)
		if !ok {
			l.refuse(v, "headers set from an expression may carry "+tenantHeader+" and cannot be verified — "+
				"strict tenant mode refuses it; write the headers as a literal map")
			return
		}
		for _, m := range maps {
			l.headers(m.obj, m.sc)
		}
	}
}

// labelMaps finds every object literal in v (labels, values, external_labels,
// targets, …) that sets Loki's __tenant_id__ label. stage.labels' values map
// names an extracted field, so the tenant comes from the data: refused.
func (l *tenantLinter) labelMaps(v ast.Expr, sc scope) {
	l.labelMapsAt(v, sc, 0)
}

func (l *tenantLinter) labelMapsAt(v ast.Expr, sc scope, nesting int) {
	if nesting > maxNesting || !l.budget.take() {
		return
	}
	switch e := unparen(v).(type) {
	case *ast.ObjectExpr:
		for _, f := range e.Fields {
			if f.Name.Name == tenantLabel {
				if sc.last() == "stage.labels" {
					l.refuse(f.Value, "stage.labels sets "+tenantLabel+" from the log data, which a client can set — "+
						"strict tenant mode refuses it")
				} else {
					l.tenantValue(f.Value, fixed(tenantLabel+" label"), sc)
				}
			}
			l.labelMapsAt(f.Value, sc, nesting+1)
		}
	case *ast.ArrayExpr:
		for _, el := range e.Elements {
			l.labelMapsAt(el, sc, nesting+1)
		}
	}
}

// relabelRule checks a relabel `rule` block (loki.relabel,
// discovery.relabel, prometheus.relabel, a source's rule block). Alloy
// v1.20.1 matches the action exactly (common/relabel Action.UnmarshalText,
// lowercase names, default "replace"), and for replace expands regex
// references in target_label as well as replacement (relabel.go). A rule
// that writes __tenant_id__ — its target_label is, may be, or may expand to
// __tenant_id__ for replace, lowercase, uppercase or hashmod; a labelmap,
// whose replacement names every label it writes — is checked: a readable
// replacement without a regex reference is a tenant; anything else comes
// from the data and is refused.
func (l *tenantLinter) relabelRule(b *ast.BlockStmt, sc scope, next *ast.BlockStmt) {
	attrs := attrsOf(b)
	target, replacement, action := attrs["target_label"], attrs["replacement"], attrs["action"]
	act := "replace"
	if action != nil {
		acts, ok := l.resolve(action, sc, 0)
		if !ok || len(acts) != 1 {
			l.refuse(action, "a relabel rule's action cannot be verified — strict tenant mode refuses it")
			return
		}
		act = acts[0]
	}
	fromData := "a relabel rule sets " + tenantLabel + " from its source labels at run time, which a client " +
		"can set — strict tenant mode refuses it"
	switch act {
	case "labelmap":
		l.labelmapRule(b, replacement, sc, next)
		return
	case "replace", "lowercase", "uppercase", "hashmod":
	default:
		return // keep, drop, labeldrop, labelkeep, keepequal, dropequal write no label value
	}
	if target == nil {
		return
	}
	targets, ok := l.resolve(target, sc, 0)
	if !ok {
		l.refuse(target, "a relabel rule's target_label cannot be verified and may be "+tenantLabel+
			" — strict tenant mode refuses it")
		return
	}
	writes := false
	for _, t := range targets {
		if act == "replace" && strings.Contains(t, "$") {
			l.refuse(target, "a relabel rule's target_label expands a regex reference and may become "+tenantLabel+
				" — strict tenant mode refuses it")
			return
		}
		writes = writes || t == tenantLabel
	}
	if !writes {
		return
	}
	if act != "replace" || replacement == nil {
		l.refuse(target, fromData)
		return
	}
	if reps, ok := l.resolve(replacement, sc, 0); ok {
		for _, s := range reps {
			if strings.Contains(s, "$") {
				l.refuse(replacement, fromData)
				return
			}
		}
	}
	l.tenantValue(replacement, fixed("relabel rule "+tenantLabel), sc)
}

// labelmapRule checks a labelmap rule. Every label a labelmap writes is
// named by its replacement (default "$1", a capture of an existing label
// name). A literal replacement of __tenant_id__ is refused whatever
// follows. A literal without a reference names one other label: allowed.
// Any other replacement (the default, a reference, a value the lint cannot
// read) may write __tenant_id__ from the data, so the rule is allowed only
// when the very next rule drops __tenant_id__ (isTenantDrop) — what the
// served form of a strict org inserts there (InsertTenantDrops, #410) — or,
// in the stored text, when the caller lints the served form as well
// (TenantPolicy.servedDrops), which then decides.
func (l *tenantLinter) labelmapRule(b *ast.BlockStmt, replacement ast.Expr, sc scope, next *ast.BlockStmt) {
	dropped := isTenantDrop(next) || l.policy.servedDrops
	if replacement == nil {
		if !dropped {
			l.refuse(b, "a labelmap with the default replacement ($1) may write "+tenantLabel+
				" and is not followed by a rule dropping it — strict tenant mode refuses it; give it a literal "+
				"replacement, or follow it with a labeldrop of "+tenantLabel)
		}
		return
	}
	reps, ok := l.resolve(replacement, sc, 0)
	if !ok {
		if !dropped {
			l.refuse(replacement, "a labelmap replacement cannot be verified and is not followed by a rule dropping "+
				tenantLabel+" — strict tenant mode refuses it")
		}
		return
	}
	for _, r := range reps {
		if r == tenantLabel {
			l.refuse(replacement, "a labelmap replacement that is "+tenantLabel+" writes it from the data — strict "+
				"tenant mode refuses it")
			return
		}
	}
	for _, r := range reps {
		if strings.Contains(r, "$") && !dropped {
			l.refuse(replacement, "a labelmap replacement that expands a regex reference may write "+tenantLabel+
				" and is not followed by a rule dropping it — strict tenant mode refuses it")
			return
		}
	}
}

// authHeader checks one otelcol.auth.headers `header` block: when its key
// is (or may be) X-Scope-OrgID, its value is a tenant, and from_context /
// from_attribute (read from the request) are refused.
func (l *tenantLinter) authHeader(b *ast.BlockStmt, sc scope) {
	attrs := attrsOf(b)
	key := attrs["key"]
	if key == nil {
		return
	}
	if !l.mayBeTenantHeader(key, sc, "otelcol.auth.headers header key") {
		return
	}
	if v := attrs["value"]; v != nil {
		l.tenantValue(v, fixed("otelcol.auth.headers "+tenantHeader+" header"), sc)
	}
	for _, k := range []string{"from_context", "from_attribute"} {
		if v := attrs[k]; v != nil {
			l.refuse(v, "otelcol.auth.headers takes "+tenantHeader+" from the request at run time — strict "+
				"tenant mode refuses it")
		}
	}
}

// bearer checks an otelcol.auth.bearer component: with header set to (or
// possibly) X-Scope-OrgID it sends "<scheme> <token>" there, so with an
// empty scheme the token is the tenant; any other scheme is refused.
func (l *tenantLinter) bearer(b *ast.BlockStmt, sc scope) {
	attrs := attrsOf(b)
	header := attrs["header"]
	if header == nil {
		return // Authorization
	}
	if !l.mayBeTenantHeader(header, sc, "otelcol.auth.bearer header") {
		return
	}
	scheme := attrs["scheme"]
	if scheme == nil {
		l.refuse(header, "otelcol.auth.bearer sends \"Bearer <token>\" as "+tenantHeader+" — strict tenant mode "+
			"refuses it; use otelcol.auth.headers")
		return
	}
	schemes, ok := l.resolve(scheme, sc, 0)
	if !ok || len(schemes) != 1 || schemes[0] != "" {
		l.refuse(scheme, "otelcol.auth.bearer sends a scheme with its token as "+tenantHeader+" — strict tenant "+
			"mode refuses it; use otelcol.auth.headers")
		return
	}
	token := attrs["token"]
	if token == nil {
		l.refuse(header, "otelcol.auth.bearer sends an empty "+tenantHeader+" — strict tenant mode refuses it")
		return
	}
	l.tenantValue(token, fixed("otelcol.auth.bearer "+tenantHeader+" token"), sc)
}

// mayBeTenantHeader reports whether a header-name expression is
// X-Scope-OrgID, refusing (and reporting false) one it cannot read.
func (l *tenantLinter) mayBeTenantHeader(v ast.Expr, sc scope, what string) bool {
	names, ok := l.resolve(v, sc, 0)
	if !ok {
		l.refuse(v, what+" cannot be verified and may be "+tenantHeader+" — strict tenant mode refuses it")
		return false
	}
	for _, n := range names {
		if strings.EqualFold(n, tenantHeader) {
			return true
		}
	}
	return false
}

// importBlock handles an import.* block. import.string with readable content
// is parsed and linted with the same rules (its declares' arguments include
// what the importing text gives them); every other import, content it cannot
// read, and content that does not parse are refused: what it loads cannot be
// verified.
func (l *tenantLinter) importBlock(b *ast.BlockStmt, name string, sc scope, siblings ast.Body) {
	where := fmt.Sprintf("%s %q", name, b.Label)
	switch name {
	case "import.string":
	case "import.git":
		l.gitImport(b, where, siblings)
		return
	default:
		l.refuse(b, where+" loads configuration from outside the pipeline, which cannot be verified — strict "+
			"tenant mode refuses it; inline the module (declare, or import.string with literal content)")
		return
	}
	content := attrsOf(b)["content"]
	if content == nil {
		return
	}
	texts, ok := l.resolve(content, sc, 0)
	if !ok || len(texts) != 1 {
		l.refuse(content, where+" content cannot be verified — strict tenant mode refuses it")
		return
	}
	l.lintModule(content, where, texts[0], l.moduleArguments(b.Label, siblings), l.gitDepth)
}

// lintModule lints an imported module's text with the same rules, at the
// next depth (gitDepth is the module's import.git depth), and reports its
// findings at the import (at).
func (l *tenantLinter) lintModule(at ast.Node, where, text string, args map[string]map[string][]resolved, gitDepth int) {
	if l.depth >= maxDepth {
		l.refuse(at, where+" nests modules too deeply to verify — strict tenant mode refuses it")
		return
	}
	inner := newTenantLinter(text, l.policy, args, l.depth+1, gitDepth, l.git, l.budget)
	if inner == nil {
		l.refuse(at, where+" content does not parse, so it cannot be verified — strict tenant mode refuses it")
		return
	}
	for _, t := range inner.literals {
		l.addLiteral(t)
	}
	l.incomplete = l.incomplete || inner.incomplete
	for _, d := range inner.diags {
		msg := fmt.Sprintf("%s line %d: %s", where, d.Line, d.Message)
		if d.Warning {
			l.warn(at, msg)
		} else {
			l.refuse(at, msg)
		}
	}
}

// gitImport handles import.git in strict mode (#390): allowed only when
// repository, revision and path are literals, the revision is a full
// lower-case commit SHA (Alloy follows a moving branch or tag; only a commit
// cannot change), the path is a clean relative path, the repository is on
// the org's allowlist (policy.Git) and the import nests at most
// MaxGitImportDepth deep. The module is then fetched and linted with the
// same rules, nested imports included.
func (l *tenantLinter) gitImport(b *ast.BlockStmt, where string, siblings ast.Body) {
	if l.policy.Git == nil || l.git == nil {
		l.refuse(b, where+" loads configuration from outside the pipeline, which cannot be verified — strict "+
			"tenant mode refuses it; inline the module (declare, or import.string with literal content)")
		return
	}
	attrs := attrsOf(b)
	literal := func(attr string) (string, bool) {
		v := attrs[attr]
		if v == nil {
			if attr == "revision" {
				l.refuse(b, where+" has no revision, so Alloy follows the branch it defaults to (main) — strict "+
					"tenant mode refuses it; pin it to a full 40-character commit SHA")
			} else {
				l.refuse(b, fmt.Sprintf("%s has no %s — strict tenant mode refuses it", where, attr))
			}
			return "", false
		}
		// A plain string literal only: the serve path inlines the module
		// (pinnedimport.Inline), reading the block exactly this way.
		val, ok := PlainStringLiteral(v)
		if !ok || !l.budget.take() {
			l.refuse(v, fmt.Sprintf("%s %s cannot be verified — strict tenant mode refuses it; use a string literal", where, attr))
			return "", false
		}
		return val, true
	}
	repo, ok1 := literal("repository")
	rev, ok2 := literal("revision")
	path, ok3 := literal("path")
	if !ok1 || !ok2 || !ok3 {
		return
	}
	if msg := revisionProblem(rev); msg != "" {
		l.refuse(attrs["revision"], where+" "+msg)
		return
	}
	if !CleanModulePath(path) {
		l.refuse(attrs["path"], fmt.Sprintf("%s path %q is not a clean relative path (ASCII only) to one file in "+
			"the repository — strict tenant mode refuses it", where, path))
		return
	}
	if l.gitDepth >= MaxGitImportDepth {
		l.refuse(b, fmt.Sprintf("%s nests import.git more than %d deep — strict tenant mode refuses it",
			where, MaxGitImportDepth))
		return
	}
	if err := l.policy.Git.Allowed(repo); err != nil {
		l.refuse(attrs["repository"], fmt.Sprintf("%s: %v — strict tenant mode refuses it", where, err))
		return
	}
	m := GitModule{Repository: repo, Revision: rev, Path: path}
	got, ok := l.git.fetched[m]
	if !ok {
		l.git.need(m)
		l.refuse(b, where+" has not been fetched yet, so it cannot be verified — strict tenant mode refuses it")
		return
	}
	if got.err != nil {
		l.refuse(b, fmt.Sprintf("%s could not be fetched for the tenant check (%v) — strict tenant mode refuses it",
			where, got.err))
		return
	}
	if got.warning != "" {
		l.warn(b, where+": "+got.warning)
	}
	l.lintModule(b, where, got.content, l.moduleArguments(b.Label, siblings), l.gitDepth+1)
}

// revisionProblem says what is wrong with an import.git revision for strict
// mode, or "".
func revisionProblem(rev string) string {
	const pin = " — strict tenant mode refuses it; pin it to a full 40-character commit SHA"
	switch {
	case fullCommitSHA.MatchString(rev):
		return ""
	case len(rev) == 40 && fullCommitSHA.MatchString(strings.ToLower(rev)):
		return fmt.Sprintf("revision %q is not lower-case; write the commit SHA in lower-case", rev)
	case len(rev) >= 4 && len(rev) < 40 && fullCommitSHA.MatchString(strings.ToLower(rev)+strings.Repeat("0", 40-len(rev))):
		return fmt.Sprintf("revision %q is a short commit SHA, which a branch or tag of that name would shadow%s", rev, pin)
	default:
		return fmt.Sprintf("revision %q is not a full 40-character commit SHA; Alloy follows a branch or tag as it moves%s", rev, pin)
	}
}

// CleanModulePath reports whether p may name a pinned import.git module: a
// clean relative path — not empty, not absolute, no "." or ".." element, no
// backslash — of printable ASCII only. Non-ASCII is refused outright, so no
// two spellings (NFC and NFD forms of one name) can name the module; a
// case-insensitive collision with another entry is refused when the module
// is fetched (gitrepo.FileAtCommit).
func CleanModulePath(p string) bool {
	if p == "" || p == "." || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || pathpkg.Clean(p) != p {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] < 0x20 || p[i] > 0x7e {
			return false
		}
	}
	return p != ".." && !strings.HasPrefix(p, "../")
}

// PlainStringLiteral returns v's value when v is a string literal itself —
// not parenthesised, not an expression. The only form strict mode reads an
// import.git's repository, revision and path in.
func PlainStringLiteral(v ast.Expr) (string, bool) {
	lit, ok := v.(*ast.LiteralExpr)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// moduleArguments resolves, in this text, every value an instantiation of
// one of module's declares (a block named "<module>.<declare>") gives.
func (l *tenantLinter) moduleArguments(module string, body ast.Body) map[string]map[string][]resolved {
	out := map[string]map[string][]resolved{}
	// An instantiation inside a foreach template (opaque) gives values the
	// lint cannot read (see collectDeclares).
	var walk func(ast.Body, string, int, bool)
	walk = func(b ast.Body, declare string, nesting int, opaque bool) {
		if nesting > maxNesting {
			return
		}
		for _, st := range b {
			if !l.budget.take() {
				return
			}
			blk, ok := st.(*ast.BlockStmt)
			if !ok {
				continue
			}
			name := strings.Join(blk.Name, ".")
			if name == "declare" {
				walk(blk.Body, declareKey(declare, blk.Label), nesting+1, opaque)
				continue
			}
			if name == "foreach" {
				for _, t := range blk.Body {
					if tb, ok := t.(*ast.BlockStmt); ok && strings.Join(tb.Name, ".") == "template" {
						walk(tb.Body, declare, nesting+2, true)
					}
				}
				continue
			}
			decl, ok := strings.CutPrefix(name, module+".")
			if !ok || decl == "" {
				continue
			}
			if out[decl] == nil {
				out[decl] = map[string][]resolved{}
			}
			for _, a := range blk.Body {
				if at, ok := a.(*ast.AttributeStmt); ok {
					r := resolved{}
					if !opaque {
						r.values, r.ok = l.resolve(at.Value, scope{declare: declare}, 0)
					}
					out[decl][at.Name.Name] = append(out[decl][at.Name.Name], r)
				}
			}
		}
	}
	walk(body, "", 0, false)
	return out
}

// pushSourcePaths checks every push source (pushSources): on every path
// from it, through forward_to, to every loki.write it reaches, the
// __tenant_id__ label the client set must be overwritten or dropped by an
// unconditional step (clearsClientTenant) first. A path the lint cannot
// follow — a forward_to that is not a list of receivers of components in the
// same scope, a component it does not know — is refused: the analysis is
// only sound over what it can see.
func (l *tenantLinter) pushSourcePaths() {
	for _, ps := range l.pushes {
		name := strings.Join(ps.block.Name, ".")
		where := fmt.Sprintf("%s %q", name, ps.block.Label)
		if name == exporterLoki {
			// The strict served form routes it through a __tenant_id__ drop
			// (InsertTenantDrops), which it can do only for a plain list.
			if fwd := attrsOf(ps.block)["forward_to"]; fwd != nil && !plainReceiverList(fwd) {
				l.refuse(fwd, where+" forward_to is not a list of component receivers (such as "+
					"[loki.write.<label>.receiver]), so strict tenant mode cannot route it through the "+tenantLabel+
					" drop it serves and refuses it; write forward_to as such a list")
				continue
			}
			if l.policy.servedDrops {
				continue // the served form, which carries the drop, decides
			}
		}
		if problem := l.followClientTenant(ps.block, ps.scope, map[*ast.BlockStmt]bool{}, 0); problem != "" {
			l.refuse(ps.block, where+" "+pushSources[name]+" "+problem+" — strict tenant mode refuses it; "+
				"overwrite "+tenantLabel+" (stage.static_labels) or drop it (stage.label_drop) on every path to the writer")
		}
	}
}

// followClientTenant follows blk's forward_to with the client's tenant still
// on the entries and returns what is wrong, or "".
func (l *tenantLinter) followClientTenant(blk *ast.BlockStmt, scopeKey string, seen map[*ast.BlockStmt]bool, nesting int) string {
	if nesting > maxNesting {
		return "along a chain too long to follow"
	}
	fwd := attrsOf(blk)["forward_to"]
	if fwd == nil {
		return ""
	}
	arr, ok := unparen(fwd).(*ast.ArrayExpr)
	if !ok {
		return "to a forward_to the lint cannot follow"
	}
	for _, el := range arr.Elements {
		if !l.budget.take() {
			return "along paths too many to follow"
		}
		target, ok := l.receiverTarget(el, scopeKey)
		if !ok {
			return "to a receiver the lint cannot follow"
		}
		name := strings.Join(target.Name, ".")
		switch name {
		case "loki.write":
			return fmt.Sprintf("to loki.write %q without overwriting or dropping it", target.Label)
		case "loki.echo":
			continue
		case "loki.process", "loki.relabel":
			if seen[target] {
				continue
			}
			seen[target] = true
			if clearsClientTenant(target, l.budget) {
				continue
			}
			if p := l.followClientTenant(target, scopeKey, seen, nesting+1); p != "" {
				return p
			}
		default:
			return fmt.Sprintf("to %s %q, which the lint cannot follow", name, target.Label)
		}
	}
	return ""
}

// receiverTarget resolves `<component>.<label>.receiver` to its block in
// scopeKey.
func (l *tenantLinter) receiverTarget(e ast.Expr, scopeKey string) (*ast.BlockStmt, bool) {
	parts, ok := accessChain(e)
	if !ok || len(parts) < 3 || parts[len(parts)-1] != "receiver" {
		return nil, false
	}
	blk := l.components[scopeKey][strings.Join(parts[:len(parts)-1], ".")]
	return blk, blk != nil
}

// clearsClientTenant reports whether a loki.process or loki.relabel
// overwrites or drops __tenant_id__ for every entry: a top-level (not inside
// stage.match) stage.static_labels setting it, stage.tenant with a value,
// stage.label_drop listing it, or stage.label_keep not listing it; or a
// relabel rule that replaces it unconditionally (no source_labels, no
// regex) or a labeldrop whose regex is exactly __tenant_id__. Whatever value
// it is overwritten with is checked by the lint where it is set.
func clearsClientTenant(blk *ast.BlockStmt, b *budget) bool {
	for _, st := range blk.Body {
		if !b.take() {
			return false
		}
		stage, ok := st.(*ast.BlockStmt)
		if !ok {
			continue
		}
		if !b.charge(1 + len(stage.Body)) { // attrsOf walks the stage's body
			return false
		}
		attrs := attrsOf(stage)
		switch strings.Join(stage.Name, ".") {
		case "stage.static_labels":
			if obj, ok := unparen(attrs["values"]).(*ast.ObjectExpr); ok {
				for _, f := range obj.Fields {
					if f.Name.Name == tenantLabel {
						return true
					}
				}
			}
		case "stage.tenant":
			if attrs["value"] != nil && attrs["label"] == nil && attrs["source"] == nil {
				return true
			}
		case "stage.label_drop":
			if names, ok := literalList(attrs["values"]); ok && hasTenantLabel(names) {
				return true
			}
		case "stage.label_keep":
			if names, ok := literalList(attrs["values"]); ok && !hasTenantLabel(names) {
				return true
			}
		case "rule":
			if relabelClears(attrs) {
				return true
			}
		}
	}
	return false
}

// relabelClears reports whether one relabel rule overwrites or drops
// __tenant_id__ for every entry.
func relabelClears(attrs map[string]ast.Expr) bool {
	act := "replace"
	if a := attrs["action"]; a != nil {
		s, ok := stringLiteral(a)
		if !ok {
			return false
		}
		act = s
	}
	switch act {
	case "replace":
		t, ok := stringLiteral(attrs["target_label"])
		return ok && t == tenantLabel && attrs["replacement"] != nil && attrs["source_labels"] == nil && attrs["regex"] == nil
	case "labeldrop":
		r, ok := stringLiteral(attrs["regex"])
		return ok && r == tenantLabel
	}
	return false
}

func literalList(v ast.Expr) ([]string, bool) {
	if v == nil {
		return nil, false
	}
	arr, ok := unparen(v).(*ast.ArrayExpr)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr.Elements))
	for _, el := range arr.Elements {
		s, ok := stringLiteral(el)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// hasTenantLabel reports whether xs names __tenant_id__.
func hasTenantLabel(xs []string) bool {
	for _, s := range xs {
		if s == tenantLabel {
			return true
		}
	}
	return false
}

// accessChain flattens `a.b.c` to ["a", "b", "c"].
func accessChain(e ast.Expr) ([]string, bool) {
	var rev []string
	for range maxNesting {
		switch v := unparen(e).(type) {
		case *ast.IdentifierExpr:
			out := []string{v.Ident.Name}
			for i := len(rev) - 1; i >= 0; i-- {
				out = append(out, rev[i])
			}
			return out, true
		case *ast.AccessExpr:
			rev = append(rev, v.Name.Name)
			e = v.Value
		default:
			return nil, false
		}
	}
	return nil, false
}

// tenantValue refuses each tenant v can be that the org does not own, and v
// when it cannot read it. X-Scope-OrgID may name several tenants joined by
// "|" (a federated read); each must be owned.
func (l *tenantLinter) tenantValue(v ast.Expr, where func() string, sc scope) {
	values, ok := l.resolve(v, sc, 0)
	if !ok {
		l.refuse(v, where()+": the tenant is set from an expression that cannot be verified — strict tenant mode "+
			"refuses it; write the tenant as a literal")
		return
	}
	for _, s := range values {
		for rest := s; ; {
			// One piece at a time, charged: a value of many "|"-joined
			// tenants never becomes a slice of them.
			if !l.budget.take() {
				return
			}
			t, more, found := strings.Cut(rest, "|")
			l.addLiteral(t)
			if !l.policy.owns(t) {
				l.refuse(v, fmt.Sprintf("%s: tenant %q is not one this organisation owns; in strict tenant "+
					"mode a pipeline may only write to the organisation's own tenants", where(), t))
			}
			if !found {
				break
			}
			rest = more
		}
	}
}

// fixed is a where for tenantValue that is already a string.
func fixed(s string) func() string { return func() string { return s } }

// resolve returns every string v can be, when the lint can read it within
// its bounds: a string literal; a concatenation of readable values (each at
// most maxValueBytes, together at most maxFoldedBytes); or
// `argument.<name>.value` of the enclosing declare (resolveArgument).
func (l *tenantLinter) resolve(v ast.Expr, sc scope, depth int) ([]string, bool) {
	if v == nil || depth > maxDepth || l.exprNesting > maxNesting || !l.budget.take() {
		return nil, false
	}
	l.exprNesting++
	defer func() { l.exprNesting-- }()
	switch e := unparen(v).(type) {
	case *ast.LiteralExpr:
		s, ok := stringLiteral(e)
		if !ok {
			return nil, false
		}
		return []string{s}, true
	case *ast.BinaryExpr:
		if e.Kind != token.ADD {
			return nil, false
		}
		left, ok := l.resolve(e.Left, sc, depth)
		if !ok {
			return nil, false
		}
		right, ok := l.resolve(e.Right, sc, depth)
		if !ok || len(left)*len(right) > maxFolded {
			return nil, false
		}
		out := make([]string, 0, len(left)*len(right))
		total := 0
		for _, a := range left {
			for _, b := range right {
				n := len(a) + len(b)
				total += n
				if n > maxValueBytes || total > maxFoldedBytes || !l.budget.charge(1+n/64) {
					return nil, false
				}
				out = append(out, a+b)
			}
		}
		return out, true
	case *ast.AccessExpr:
		return l.resolveArgument(e, sc, depth)
	}
	return nil, false
}

// resolveArgument resolves `argument.<name>.value` of the enclosing
// declare: its default (read inside the declare) and every value an
// instantiation gives it (read in the declare that instantiation sits in,
// so a value passed down several declare levels is followed). It is
// readable only when every one of those is and there is at least one. Each
// (declare, argument) is resolved once (memo), and one that depends on
// itself is unreadable.
func (l *tenantLinter) resolveArgument(e *ast.AccessExpr, sc scope, depth int) ([]string, bool) {
	name, ok := argumentRef(e)
	if !ok || sc.declare == "" {
		return nil, false
	}
	key := sc.declare + "\x00" + name
	if r, ok := l.memo[key]; ok {
		// A cache hit is charged for what it hands back: a reference read
		// many times costs as many times.
		if !l.budget.charge(1 + len(r.values)) {
			return nil, false
		}
		return r.values, r.ok // an in-progress entry is {nil, false}: a cycle
	}
	l.memo[key] = &resolved{}
	vals, ok := l.resolveArgumentUncached(name, sc.declare, depth)
	l.memo[key] = &resolved{values: vals, ok: ok}
	return vals, ok
}

func (l *tenantLinter) resolveArgumentUncached(name, declare string, depth int) ([]string, bool) {
	info := l.declares[declare]
	if info == nil {
		return nil, false
	}
	var out []string
	read, total := 0, 0
	add := func(vals []string) bool {
		if len(out)+len(vals) > maxFolded {
			return false
		}
		for _, v := range vals {
			total += len(v)
		}
		out = append(out, vals...)
		read++
		return true
	}
	if def := info.defaults[name]; def != nil {
		vals, ok := l.resolve(def, scope{declare: declare}, depth+1)
		if !ok || !add(vals) {
			return nil, false
		}
	}
	for _, g := range info.given[name] {
		vals, ok := l.resolve(g.expr, scope{declare: g.declare}, depth+1)
		if !ok || !add(vals) {
			return nil, false
		}
	}
	for _, r := range info.external[name] {
		if !r.ok || !add(r.values) {
			return nil, false
		}
	}
	if read == 0 || total > 4*maxFoldedBytes {
		return nil, false
	}
	return out, true
}

// argumentRef reports the argument name of `argument.<name>.value`.
func argumentRef(e *ast.AccessExpr) (string, bool) {
	if e.Name.Name != "value" {
		return "", false
	}
	mid, ok := e.Value.(*ast.AccessExpr)
	if !ok {
		return "", false
	}
	root, ok := mid.Value.(*ast.IdentifierExpr)
	if !ok || root.Ident.Name != "argument" {
		return "", false
	}
	return mid.Name.Name, true
}

// attrsOf maps a block's own attributes by name.
func attrsOf(b *ast.BlockStmt) map[string]ast.Expr {
	out := map[string]ast.Expr{}
	for _, st := range b.Body {
		if a, ok := st.(*ast.AttributeStmt); ok {
			out[a.Name.Name] = a.Value
		}
	}
	return out
}

// stringLiteral returns v's value when it is a plain string literal.
func stringLiteral(v ast.Expr) (string, bool) {
	if v == nil {
		return "", false
	}
	lit, ok := unparen(v).(*ast.LiteralExpr)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

func unparen(v ast.Expr) ast.Expr {
	for {
		p, ok := v.(*ast.ParenExpr)
		if !ok {
			return v
		}
		v = p.Inner
	}
}

func (l *tenantLinter) refuse(at ast.Node, msg string) {
	l.budget.take()
	if len(l.diags) >= maxDiagnostics {
		l.tooMany = true
		return
	}
	pos := ast.StartPos(at).Position()
	l.diags = append(l.diags, Diagnostic{Line: pos.Line, Col: pos.Column, Message: msg, Stage: 1})
}

// warn records a warning at at: reported, but the pipeline is not refused.
func (l *tenantLinter) warn(at ast.Node, msg string) {
	l.budget.take()
	if len(l.diags) >= maxDiagnostics {
		return
	}
	pos := ast.StartPos(at).Position()
	l.diags = append(l.diags, Diagnostic{Line: pos.Line, Col: pos.Column, Message: msg, Stage: 1, Warning: true})
}

// hasErrors reports whether diags holds a refusal (a non-warning
// diagnostic).
func hasErrors(diags []Diagnostic) bool {
	for _, d := range diags {
		if !d.Warning {
			return true
		}
	}
	return false
}
