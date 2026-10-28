// Package tenancy is strict tenant mode: the rule that an organisation's
// destinations, destination bindings, tenant routes and pipelines may only
// use tenants it owns (org_tenants, gateway.OrgOwnsTenant). It applies to an
// org when its own strict_tenants flag is on (0040_org_strict_tenants, set
// by an app admin) or when the server-wide tenants.require_owned setting is.
//
// Policy is the rule for one org as the write paths ask it; Preflight finds
// what of an org already breaks it, which is what turning strict mode on —
// per org (AdminService.SetOrgStrictTenants) or server-wide (boot) — is
// refused over.
package tenancy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/advisorylock"
	"shepherd/internal/config"
	"shepherd/internal/gateway"
	"shepherd/internal/pinnedimport"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
)

// Policy is strict tenant mode for one org.
type Policy struct {
	// Strict is whether the rule applies: OrgStrict or the server-wide
	// setting.
	Strict bool
	// OrgStrict is the org's own strict_tenants flag.
	OrgStrict bool
	// Owned is the org's tenants and tenant prefixes.
	Owned []gateway.OrgTenant
	// OrgID is the org's id; ImportRepos are the repositories its pipelines
	// may import.git from, pinned to a full commit SHA (0043, #390).
	OrgID       pgtype.UUID
	ImportRepos []sqlc.OrgImportRepository
	// AllowedComponents are the components an application administrator
	// allowed for the org although strict mode refuses them (0047, #421),
	// sorted: it lifts only that refusal.
	AllowedComponents []string
	// GatewayHosts are the tenant-route gateway's hosts (ConfigureGateway),
	// normalised; Routes are the org's tenant routes. In strict mode an
	// endpoint on the gateway must be under one of the org's own routes
	// (OwnsRoute).
	GatewayHosts []string
	Routes       []Route
	// RoutesInUse is whether the installation has a live tenant route: an
	// endpoint the lint cannot read is then refused.
	RoutesInUse bool
}

// Route is one of an org's tenant routes as strict tenant mode reads it.
type Route struct {
	Kind, Segment, Tenant, Status string
}

// OwnsRoute reports whether the org owns the route of kind with segment:
// one of its routes that still routes (active, or deprecated during its
// rotation overlap; a revoked route does not), whose tenant the org owns.
// A segment is unique per kind and never reused (0009_tenant_routes'
// UNIQUE (kind, segment) over every row, revoked ones included).
func (p Policy) OwnsRoute(kind, segment string) bool {
	return routeOwner(p.Routes, p.Owns)(kind, segment)
}

// routeUnusable says why the org's own route of kind with segment does not
// route for it (revoked, or on a tenant it no longer owns), or "" when it
// does, or when segment is not one of the org's routes (another org's, or
// none: the answer reveals nothing about which).
func routeUnusable(routes []Route, ownsTenant func(string) bool) func(kind, segment string) string {
	return func(kind, segment string) string {
		for _, r := range routes {
			if r.Kind != kind || r.Segment != segment {
				continue
			}
			st, err := gateway.ParseRouteStatus(r.Status)
			if err != nil {
				return ""
			}
			switch st {
			case gateway.RouteRevoked:
				return "it is revoked"
			case gateway.RouteActive, gateway.RouteDeprecated:
				if !ownsTenant(r.Tenant) {
					return fmt.Sprintf("its tenant %q is no longer one the organisation owns", r.Tenant)
				}
			}
			return ""
		}
		return ""
	}
}

func routeOwner(routes []Route, ownsTenant func(string) bool) func(kind, segment string) bool {
	return func(kind, segment string) bool {
		for _, r := range routes {
			if r.Kind != kind || r.Segment != segment {
				continue
			}
			st, err := gateway.ParseRouteStatus(r.Status)
			if err != nil {
				return false
			}
			switch st {
			case gateway.RouteActive, gateway.RouteDeprecated:
				return ownsTenant(r.Tenant)
			case gateway.RouteRevoked:
				return false
			}
			return false
		}
		return false
	}
}

// orgRoutes is orgID's tenant routes, in a stable order.
func orgRoutes(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID) ([]Route, error) {
	rows, err := q.ListTenantRoutesByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("loading the org's tenant routes: %w", err)
	}
	out := make([]Route, len(rows))
	for i := range rows {
		out[i] = Route{Kind: rows[i].Kind, Segment: rows[i].Segment, Tenant: rows[i].TenantID, Status: rows[i].Status}
	}
	slices.SortFunc(out, func(a, b Route) int {
		return strings.Compare(a.Kind+"\x00"+a.Segment, b.Kind+"\x00"+b.Segment)
	})
	return out, nil
}

var (
	gatewayMu    sync.RWMutex
	gatewayHosts []string
)

// ConfigureGateway sets the tenant-route gateway's hosts from the server
// configuration (gateway.routes.public_base_url's host and
// gateway.routes.hosts) for every policy this process loads. The server and
// each CLI command that checks pipelines call it once, at start. A host that
// is not one (a URL, a port, a path) is an error. With no host, strict tenant
// mode checks no endpoint against the gateway: it cannot know which host
// serves the routes.
func ConfigureGateway(c config.GatewayRoutesConfig) error {
	hosts := c.GatewayHosts()
	norm := make([]string, 0, len(hosts))
	for _, h := range hosts {
		n, err := validate.NormalizeGatewayHost(h)
		if err != nil {
			return fmt.Errorf("gateway.routes.hosts: %w", err)
		}
		if !slices.Contains(norm, n) {
			norm = append(norm, n)
		}
	}
	gatewayMu.Lock()
	gatewayHosts = norm
	gatewayMu.Unlock()
	return nil
}

// SetGatewayHosts replaces the gateway's hosts until restore is called: for
// tests.
func SetGatewayHosts(hosts []string) (restore func(), err error) {
	gatewayMu.RLock()
	saved := gatewayHosts
	gatewayMu.RUnlock()
	if err := ConfigureGateway(config.GatewayRoutesConfig{Hosts: hosts}); err != nil {
		return func() {}, err
	}
	return func() {
		gatewayMu.Lock()
		gatewayHosts = saved
		gatewayMu.Unlock()
	}, nil
}

// GatewayHosts is the gateway's hosts as ConfigureGateway set them.
func GatewayHosts() []string {
	gatewayMu.RLock()
	defer gatewayMu.RUnlock()
	return slices.Clone(gatewayHosts)
}

// Load reads orgID's policy through q. requireOwned is the server-wide
// tenants.require_owned setting. A write that then checks a tenant against
// it must hold the shared ownership lock in q's transaction, so neither the
// org's tenants nor its flag (both changed under the exclusive lock) move
// before it commits.
func Load(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, requireOwned bool) (Policy, error) {
	org, err := q.GetOrgByID(ctx, orgID)
	if err != nil {
		return Policy{}, fmt.Errorf("loading org: %w", err)
	}
	return LoadOrg(ctx, q, org, requireOwned)
}

// LoadOrg is Load for an org already read.
func LoadOrg(ctx context.Context, q *sqlc.Queries, org sqlc.Org, requireOwned bool) (Policy, error) {
	rows, err := q.ListOrgTenants(ctx, org.ID)
	if err != nil {
		return Policy{}, fmt.Errorf("loading org tenants: %w", err)
	}
	repos, err := q.ListOrgImportRepositories(ctx, org.ID)
	if err != nil {
		return Policy{}, fmt.Errorf("loading org import repositories: %w", err)
	}
	components, err := allowedComponents(ctx, q, org.ID)
	if err != nil {
		return Policy{}, err
	}
	routes, err := orgRoutes(ctx, q, org.ID)
	if err != nil {
		return Policy{}, err
	}
	inUse, err := q.AnyLiveTenantRoute(ctx)
	if err != nil {
		return Policy{}, fmt.Errorf("checking for live tenant routes: %w", err)
	}
	return Policy{
		RoutesInUse: inUse,
		Strict:      org.StrictTenants || requireOwned, OrgStrict: org.StrictTenants, Owned: Rules(rows),
		OrgID: org.ID, ImportRepos: repos, AllowedComponents: components,
		GatewayHosts: GatewayHosts(), Routes: routes,
	}, nil
}

// allowedComponents is the components an application administrator allowed
// for orgID (0047), sorted by name.
func allowedComponents(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID) ([]string, error) {
	rows, err := q.ListOrgStrictComponents(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("loading the org's allowed components: %w", err)
	}
	out := make([]string, len(rows))
	for i := range rows {
		out[i] = rows[i].Component
	}
	return out, nil
}

// componentSet is names as validate.TenantPolicy takes them (nil when empty).
func componentSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

// Rules converts org_tenants rows to gateway's form.
func Rules(rows []sqlc.OrgTenant) []gateway.OrgTenant {
	out := make([]gateway.OrgTenant, len(rows))
	for i := range rows {
		out[i] = gateway.OrgTenant{Tenant: rows[i].Tenant, Kind: gateway.TenantKind(rows[i].Kind)}
	}
	return out
}

// Owns reports whether the org owns tenant.
func (p Policy) Owns(tenant string) bool { return gateway.OrgOwnsTenant(p.Owned, tenant) }

// ErrNotOwned is the strict-mode refusal of a tenant the org does not own.
var ErrNotOwned = errors.New("strict tenant mode: the tenant is not one this organisation owns; " +
	"use one of its tenants (or a tenant under one of its prefixes), which an application administrator assigns")

// Check refuses (ErrNotOwned) a non-empty tenant the org does not own when
// the policy is strict. An empty tenant — a destination that sends none —
// always passes.
func (p Policy) Check(tenant string) error {
	if !p.Strict || tenant == "" || p.Owns(tenant) {
		return nil
	}
	return fmt.Errorf("%w (tenant %q)", ErrNotOwned, tenant)
}

// Validation is the policy as the validation gate takes it
// (validate.ValidatePipeline's tenant lint), reading pinned import.git
// modules from src through q: pinnedimport.Resolver.Saving() for a save or
// a validation by a caller who could save, .Preflight() for a check of what
// is stored, and pinnedimport.Cached inside a write transaction or for a
// read-only validation.
func (p Policy) Validation(q *sqlc.Queries, src pinnedimport.Source) validate.TenantPolicy {
	if !p.Strict {
		return validate.TenantPolicy{}
	}
	return validate.TenantPolicy{
		Strict: true, Owns: p.Owns, Git: src.Modules(q, p.OrgID, p.ImportRepos), Inline: inliner(q, p.OrgID, p.ImportRepos),
		AllowedComponents: componentSet(p.AllowedComponents),
		Gateway: validate.GatewayRoutes{
			Hosts: p.GatewayHosts, Owns: p.OwnsRoute, Unusable: routeUnusable(p.Routes, p.Owns), RoutesInUse: p.RoutesInUse,
		},
	}
}

// inliner is the served form of an org's pipeline text (pinnedimport.Inline),
// read from the cache through q.
func inliner(q *sqlc.Queries, orgID pgtype.UUID, repos []sqlc.OrgImportRepository) func(context.Context, string) (string, error) {
	return func(ctx context.Context, contents string) (string, error) {
		return pinnedimport.Inline(ctx, q, orgID, repos, contents)
	}
}

// The kinds of object a Violation names.
const (
	KindOrg      = "org"
	KindPipeline = "pipeline"
)

// Violation is one thing that breaks the strict tenant rule.
type Violation struct {
	OrgID, OrgName string
	// Kind is "tenant route", "destination", "destination binding",
	// "pipeline" or "org".
	Kind   string
	Name   string
	Tenant string
	// Message says what is wrong and how to fix it.
	Message string
}

func (v Violation) String() string {
	return fmt.Sprintf("org %q: %s", v.OrgName, v.Message)
}

// Preflight lists what of org breaks the strict tenant rule: each live
// tenant route, destination and destination binding on a tenant the org
// does not own, and each enabled pipeline the tenant lint refuses. With
// requireTenant, an org that owns no tenant at all is a violation too
// (per-org strict mode needs one; the server-wide setting does not).
// Pipelines' pinned import.git modules are read from src, as a save reads
// them (src is pinnedimport.Resolver.Preflight(), or pinnedimport.Cached
// under the exclusive ownership lock).
func Preflight(ctx context.Context, q *sqlc.Queries, org sqlc.Org, requireTenant bool, src pinnedimport.Source) ([]Violation, error) {
	rows, err := q.ListOrgTenants(ctx, org.ID)
	if err != nil {
		return nil, fmt.Errorf("loading org %q's tenants: %w", org.Name, err)
	}
	repos, err := q.ListOrgImportRepositories(ctx, org.ID)
	if err != nil {
		return nil, fmt.Errorf("loading org %q's import repositories: %w", org.Name, err)
	}
	components, err := allowedComponents(ctx, q, org.ID)
	if err != nil {
		return nil, err
	}
	routes, err := orgRoutes(ctx, q, org.ID)
	if err != nil {
		return nil, err
	}
	inUse, err := q.AnyLiveTenantRoute(ctx)
	if err != nil {
		return nil, fmt.Errorf("checking for live tenant routes: %w", err)
	}
	p := Policy{
		RoutesInUse: inUse,
		Strict:      true, OrgStrict: org.StrictTenants, Owned: Rules(rows), OrgID: org.ID, ImportRepos: repos,
		AllowedComponents: components, GatewayHosts: GatewayHosts(), Routes: routes,
	}
	base := Violation{OrgID: org.ID.String(), OrgName: org.Name}
	var out []Violation
	if requireTenant && len(rows) == 0 {
		v := base
		v.Kind = KindOrg
		v.Name = org.Name
		v.Message = "the organisation owns no tenant; an application administrator assigns one " +
			"(Admin → Organisations → tenants) before strict tenant mode can be turned on"
		out = append(out, v)
	}
	uses, err := q.ListOrgTenantUses(ctx, org.ID)
	if err != nil {
		return nil, fmt.Errorf("listing org %q's tenant uses: %w", org.Name, err)
	}
	for i := range uses {
		if p.Owns(uses[i].Tenant) {
			continue
		}
		v := base
		v.Kind, v.Name, v.Tenant = uses[i].What, uses[i].Name, uses[i].Tenant
		v.Message = fmt.Sprintf("%s %q uses tenant %q, which the organisation does not own; assign the "+
			"tenant to the organisation or change the %s", uses[i].What, uses[i].Name, uses[i].Tenant, uses[i].What)
		out = append(out, v)
	}
	pipelines, err := q.ListEnabledPipelinesByOrg(ctx, org.ID)
	if err != nil {
		return nil, fmt.Errorf("listing org %q's enabled pipelines: %w", org.Name, err)
	}
	for i := range pipelines {
		refusals, err := lintRefusals(ctx, pipelines[i].Contents, p.Validation(q, src))
		if err != nil {
			return nil, err
		}
		if len(refusals) == 0 {
			continue
		}
		msgs := make([]string, len(refusals))
		for j, d := range refusals {
			msgs[j] = preflightRefusalText(d, pipelines[i].Contents)
		}
		v := base
		v.Kind, v.Name = KindPipeline, pipelines[i].Name
		v.Message = fmt.Sprintf("enabled pipeline %q fails the strict tenant check (%s); in strict tenant mode it "+
			"is left out of every collector's served config until it is fixed — %s",
			pipelines[i].Name, strings.Join(msgs, "; "), fixHint(refusals))
		out = append(out, v)
	}
	return out, nil
}

// PreflightAll is Preflight over every org without requireTenant: what the
// server-wide tenants.require_owned setting refuses to start over.
func PreflightAll(ctx context.Context, q *sqlc.Queries, src pinnedimport.Source) ([]Violation, error) {
	orgs, err := q.ListOrgs(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing orgs: %w", err)
	}
	var out []Violation
	for i := range orgs {
		v, err := Preflight(ctx, q, orgs[i], false, src)
		if err != nil {
			return nil, err
		}
		out = append(out, v...)
	}
	return out, nil
}

// Summarize renders violations as one line each, for an error message.
func Summarize(vs []Violation) string {
	lines := make([]string, len(vs))
	for i := range vs {
		lines[i] = vs[i].String()
	}
	return strings.Join(lines, "; ")
}

// ErrRequireOwnedViolations is the boot refusal when tenants.require_owned
// is on and something already stored breaks the rule.
var ErrRequireOwnedViolations = errors.New("tenants.require_owned is on, but stored objects use tenants their " +
	"organisation does not own")

// CheckRequireOwned is the boot preflight for tenants.require_owned: nil
// when nothing breaks the rule, else an error listing every violation and
// how to clear them.
func CheckRequireOwned(ctx context.Context, q *sqlc.Queries, src pinnedimport.Source) error {
	vs, err := PreflightAll(ctx, q, src)
	if err != nil {
		return fmt.Errorf("tenant preflight: %w", err)
	}
	if len(vs) == 0 {
		return nil
	}
	lines := make([]string, len(vs))
	for i := range vs {
		lines[i] = "  - " + vs[i].String()
	}
	return fmt.Errorf("%w (%d):\n%s\nAssign the tenants to their organisations (Admin → Organisations), or edit, "+
		"disable or delete the objects listed, then start again; `shepherd admin tenant-preflight` lists them "+
		"without starting the server. Or set tenants.require_owned back to false",
		ErrRequireOwnedViolations, len(vs), strings.Join(lines, "\n"))
}

// Pipeline is a pipeline's name and text, as LintLocked checks it.
type Pipeline struct{ Name, Contents string }

// ErrPipelineNotOwned is LintLocked's refusal.
var ErrPipelineNotOwned = errors.New("strict tenant mode refuses pipeline(s)")

// LintLocked takes the shared ownership lock in q's transaction, reads
// orgID's policy under it and refuses (ErrPipelineNotOwned, naming each
// pipeline and its diagnostics) any of pipelines the tenant lint refuses.
// Every pipeline write runs it in its writing transaction, so it serialises
// with SetOrgStrictTenants and ownership changes (both exclusive): neither
// can commit between this check and the write. A no-op for an org not in
// strict tenant mode, beyond the lock. Pinned import.git modules are read
// from the cache only (pinnedimport.Cached): the write's own validation
// fetched them moments before, outside the transaction.
func LintLocked(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, requireOwned bool, pipelines ...Pipeline) error {
	if err := q.LockOrgTenantsShared(ctx, advisorylock.TenantID); err != nil {
		return fmt.Errorf("taking the tenant ownership lock: %w", err)
	}
	p, err := Load(ctx, q, orgID, requireOwned)
	if err != nil {
		return err
	}
	if !p.Strict {
		return nil
	}
	var failures []string
	for i := range pipelines {
		msgs, err := lintErrors(ctx, pipelines[i].Contents, p.Validation(q, pinnedimport.Cached))
		if err != nil {
			return err
		}
		if len(msgs) > 0 {
			failures = append(failures, fmt.Sprintf("%s: %s", pipelines[i].Name, strings.Join(msgs, "; ")))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%w: %s", ErrPipelineNotOwned, strings.Join(failures, " | "))
	}
	return nil
}

// LintPipeline is the strict tenant check of one pipeline's stored text
// under p, reading pinned import.git modules from src: the tenant lint on
// the text and on the form a strict org is served (pinned modules inlined,
// __tenant_id__ dropped after labelmaps and otelcol.exporter.loki). It is the
// one check a save, Preflight (`shepherd admin tenant-preflight`) and the
// serve-time check (internal/serve.Serving) run, so the preflight predicts
// exactly what serving excludes. The result is the refusals as "line:col
// message"; validate.ErrLintBusy (wrapped) when the lint hit its wall-clock
// guard.
func LintPipeline(ctx context.Context, q *sqlc.Queries, p Policy, src pinnedimport.Source, contents string) ([]string, error) {
	return lintErrors(ctx, contents, p.Validation(q, src))
}

// lintErrors is TenantLint's refusals in contents, as "line:col message";
// validate.ErrLintBusy (wrapped) when the lint hit its wall-clock guard —
// no verdict, so callers answer "busy, try again" rather than refuse.
func lintErrors(ctx context.Context, contents string, policy validate.TenantPolicy) ([]string, error) {
	refusals, err := lintRefusals(ctx, contents, policy)
	if err != nil {
		return nil, err
	}
	var msgs []string
	for _, d := range refusals {
		msgs = append(msgs, refusalText(d))
	}
	return msgs, nil
}

// lintRefusals is TenantLint's refusals (not its warnings) in contents.
func lintRefusals(ctx context.Context, contents string, policy validate.TenantPolicy) ([]validate.Diagnostic, error) {
	// WithTenantLint lints the served form too (policy.Inline).
	r := validate.WithTenantLint(ctx, validate.Result{Valid: true}, contents, policy)
	if r.Busy {
		return nil, fmt.Errorf("tenant check: %w", validate.ErrLintBusy)
	}
	var out []validate.Diagnostic
	for _, d := range r.Diagnostics {
		if !d.Warning {
			out = append(out, d)
		}
	}
	return out, nil
}

func refusalText(d validate.Diagnostic) string {
	return fmt.Sprintf("%d:%d %s", d.Line, d.Col, d.Message)
}

// preflightRefusalText is refusalText for the preflight's report, which
// names a pipeline whose stored text does not parse as exactly that, with
// the parser's error. The served header and a save keep refusalText's
// wording: it is what v0.20.0 already serves as the exclusion reason.
func preflightRefusalText(d validate.Diagnostic, contents string) string {
	if d.Finding == validate.FindingUnparsable {
		if err := validate.ParseError(contents); err != nil {
			return "the pipeline text does not parse: " + err.Error()
		}
	}
	return refusalText(d)
}

// fixHint is how a pipeline refused for refusals is fixed: edit or disable
// it, or whatever lifts each kind of finding it has — assigning a tenant
// fixes a tenant finding only, allowing a component only a refused shipped
// one. Text that does not parse has one fix only: nothing else can be judged
// until it parses.
func fixHint(refusals []validate.Diagnostic) string {
	var tenant, component, route, unparsable bool
	for _, d := range refusals {
		switch d.Finding {
		case validate.FindingTenant:
			tenant = true
		case validate.FindingComponent:
			component = true
		case validate.FindingRoute:
			route = true
		case validate.FindingUnknownComponent:
			// Nothing to allow: only an edit fixes it.
		case validate.FindingUnparsable:
			unparsable = true
		}
	}
	if unparsable {
		return "fix the pipeline text"
	}
	options := []string{"edit or disable the pipeline"}
	if tenant {
		options = append(options, "assign the tenant to the organisation")
	}
	if route {
		options = append(options, "use one of this organisation's tenant routes")
	}
	if component {
		options = append(options, "ask an application administrator to allow the component for this organisation")
	}
	if len(options) == 1 {
		return options[0]
	}
	return strings.Join(options[:len(options)-1], ", ") + ", or " + options[len(options)-1]
}

// OrphanedPipelines names org's enabled pipelines that would fail the tenant
// lint if the org owned only remaining — what removing a tenant rule from a
// strict org must not leave behind. q should hold the exclusive ownership
// lock. Pinned import.git modules are read from the cache only.
func OrphanedPipelines(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, remaining []gateway.OrgTenant) ([]string, error) {
	pipelines, err := q.ListEnabledPipelinesByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("listing enabled pipelines: %w", err)
	}
	repos, err := q.ListOrgImportRepositories(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("listing import repositories: %w", err)
	}
	components, err := allowedComponents(ctx, q, orgID)
	if err != nil {
		return nil, err
	}
	routes, err := orgRoutes(ctx, q, orgID)
	if err != nil {
		return nil, err
	}
	inUse, err := q.AnyLiveTenantRoute(ctx)
	if err != nil {
		return nil, fmt.Errorf("checking for live tenant routes: %w", err)
	}
	ownsTenant := func(t string) bool { return gateway.OrgOwnsTenant(remaining, t) }
	policy := validate.TenantPolicy{
		Strict: true, Owns: ownsTenant,
		Git: pinnedimport.Cached.Modules(q, orgID, repos), Inline: inliner(q, orgID, repos),
		AllowedComponents: componentSet(components),
		// A route whose tenant is removed is no longer one of the org's.
		Gateway: validate.GatewayRoutes{
			Hosts: GatewayHosts(), Owns: routeOwner(routes, ownsTenant), Unusable: routeUnusable(routes, ownsTenant),
			RoutesInUse: inUse,
		},
	}
	var out []string
	for i := range pipelines {
		msgs, err := lintErrors(ctx, pipelines[i].Contents, policy)
		if err != nil {
			return nil, err
		}
		if len(msgs) > 0 {
			out = append(out, fmt.Sprintf("pipeline %q (%s)", pipelines[i].Name, strings.Join(msgs, "; ")))
		}
	}
	return out, nil
}

// OtherOrgPipelinesCovered names every other org's enabled pipeline that
// writes to a tenant rule would cover — what an org must not be given,
// whatever either org's mode: the pipeline would then be writing into the
// new owner's tenant. It first re-records every other org's enabled pipeline
// whose pipeline_tenants rows do not describe its current contents (a pod
// that predates the index wrote it, during a rolling upgrade), then asks one
// indexed query. A pipeline whose tenants the lint could not all read is
// logged, not refused (fail soft: its own org is not strict, or it would not
// be enabled). q should hold the exclusive ownership lock. Org names are
// included: only an app admin calls it.
//
// warnings name the other orgs with enabled pipelines whose tenants the
// lint could not all read (incomplete index rows), with how many: the check
// does not see what those write to, so the app admin is told rather than
// refused (owner decision).
func OtherOrgPipelinesCovered(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, rule gateway.OrgTenant) (covered, warnings []string, err error) {
	stale, err := q.ListStalePipelineTenantIndexOfOtherOrgs(ctx, orgID)
	if err != nil {
		return nil, nil, fmt.Errorf("listing stale pipeline tenants: %w", err)
	}
	for i := range stale {
		if err := RecordPipelineTenants(ctx, q, stale[i]); err != nil {
			return nil, nil, err
		}
	}
	// A pipeline still stale after that could not be read in time (the
	// lint's wall-clock guard): the check does not see it, so it is a
	// warning like an incomplete one, never silently left out.
	stillStale, err := q.ListStalePipelineTenantIndexOfOtherOrgs(ctx, orgID)
	if err != nil {
		return nil, nil, fmt.Errorf("listing stale pipeline tenants: %w", err)
	}
	if len(stillStale) > 0 {
		perOrg := map[pgtype.UUID]int{}
		var order []pgtype.UUID
		for i := range stillStale {
			if perOrg[stillStale[i].OrgID] == 0 {
				order = append(order, stillStale[i].OrgID)
			}
			perOrg[stillStale[i].OrgID]++
		}
		for _, id := range order {
			name := id.String()
			if o, err := q.GetOrgByID(ctx, id); err == nil {
				name = o.Name
			}
			warnings = append(warnings, fmt.Sprintf("org %q has %d enabled pipeline(s) the check could not read in "+
				"time (busy); it cannot tell whether they write to %q — try again", name, perOrg[id], rule.Tenant))
		}
	}
	incomplete, err := q.ListIncompletePipelineTenantOrgs(ctx, orgID)
	if err != nil {
		return nil, nil, fmt.Errorf("listing incomplete pipeline tenants: %w", err)
	}
	for i := range incomplete {
		warnings = append(warnings, fmt.Sprintf("org %q has %d enabled pipeline(s) setting tenants the check cannot "+
			"read (computed values, external modules, or too large); it cannot tell whether they write to %q",
			incomplete[i].OrgName, incomplete[i].Pipelines, rule.Tenant))
	}
	rows, err := q.ListPipelineTenantsCoveredByRule(ctx, sqlc.ListPipelineTenantsCoveredByRuleParams{
		OrgID: orgID, Tenant: rule.Tenant, IsPrefix: rule.Kind == gateway.TenantPrefix,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("listing other orgs' pipeline tenants: %w", err)
	}
	for i := range rows {
		if gateway.RuleCovers(rule, rows[i].Tenant) {
			covered = append(covered, fmt.Sprintf("pipeline %q of org %q (tenant %q)", rows[i].PipelineName, rows[i].OrgName, rows[i].Tenant))
		}
	}
	return covered, warnings, nil
}

// RecordPipelineTenants keeps pipeline_tenants current for p as just
// written: its rows are replaced by the tenants its text writes to
// (validate.TenantLiterals, bounded) when it is enabled, with an index row
// naming the contents they describe and whether that is all of them; both
// are removed when it is not enabled. A pipeline whose tenants the lint
// cannot all read is recorded as incomplete and logged — never a failed
// write. Every pipeline write calls it in its writing transaction.
func RecordPipelineTenants(ctx context.Context, q *sqlc.Queries, p sqlc.Pipeline) error {
	if !p.Enabled {
		if err := q.DeletePipelineTenants(ctx, p.ID); err != nil {
			return fmt.Errorf("clearing the pipeline's tenants: %w", err)
		}
		if err := q.DeletePipelineTenantIndex(ctx, p.ID); err != nil {
			return fmt.Errorf("clearing the pipeline's tenant index: %w", err)
		}
		return nil
	}
	// Pinned import.git modules are read from the cache, so a strict org's
	// pipeline using them is recorded with their tenants.
	repos, err := q.ListOrgImportRepositories(ctx, p.OrgID)
	if err != nil {
		return fmt.Errorf("loading the org's import repositories: %w", err)
	}
	lits, complete, err := validate.TenantLiteralsWith(ctx, p.Contents, pinnedimport.Cached.Modules(q, p.OrgID, repos))
	if err != nil {
		// The lint's wall-clock guard (validate.ErrLintBusy): no verdict.
		// The previous rows stay, now stale by their sha, so AddOrgTenant
		// or the next boot records the pipeline again.
		slog.Warn("strict tenants: the tenant check was busy; the pipeline's tenant index stays stale until it is re-read",
			"pipeline_id", p.ID.String(), "org_id", p.OrgID.String(), "err", err)
		return nil
	}
	if err := q.DeletePipelineTenants(ctx, p.ID); err != nil {
		return fmt.Errorf("clearing the pipeline's tenants: %w", err)
	}
	if !complete {
		slog.Warn("strict tenants: the pipeline sets a tenant the lint cannot read; recorded as incomplete",
			"pipeline_id", p.ID.String(), "org_id", p.OrgID.String())
	}
	seen := map[string]bool{}
	for _, t := range lits {
		if seen[t] {
			continue
		}
		seen[t] = true
		if err := q.InsertPipelineTenant(ctx, sqlc.InsertPipelineTenantParams{PipelineID: p.ID, OrgID: p.OrgID, Tenant: t}); err != nil {
			return fmt.Errorf("recording the pipeline's tenants: %w", err)
		}
	}
	sum := sha256.Sum256([]byte(p.Contents))
	if err := q.UpsertPipelineTenantIndex(ctx, sqlc.UpsertPipelineTenantIndexParams{
		PipelineID: p.ID, OrgID: p.OrgID, ContentsSha256: hex.EncodeToString(sum[:]), Complete: complete,
	}); err != nil {
		return fmt.Errorf("recording the pipeline's tenant index: %w", err)
	}
	return nil
}

// RebuildPipelineTenants brings pipeline_tenants up to date for every
// enabled pipeline whose rows do not describe its current contents — the
// backfill of 0040 (SQL cannot parse Alloy) and the repair of anything
// written outside the API — each in its own transaction under the shared
// ownership lock. It never fails: the server runs it at every boot, and one
// pipeline it cannot record (logged) must not stop the server. The lint is
// bounded, so no pipeline makes it slow.
func RebuildPipelineTenants(ctx context.Context, st *store.Store) {
	// The zero UUID is no org, so this is every org's pipelines.
	stale, err := st.Queries.ListStalePipelineTenantIndexOfOtherOrgs(ctx, pgtype.UUID{Valid: true})
	if err != nil {
		slog.Error("strict tenants: listing pipelines to record; AddOrgTenant re-records them on demand", "err", err)
		return
	}
	for i := range stale {
		if err := recordOne(ctx, st, stale[i]); err != nil {
			slog.Error("strict tenants: recording a pipeline's tenants; AddOrgTenant re-records it on demand",
				"err", err, "pipeline_id", stale[i].ID.String())
		}
	}
}

func recordOne(ctx context.Context, st *store.Store, p sqlc.Pipeline) error {
	tx, err := st.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // no-op once committed
	q := st.Queries.WithTx(tx)
	if err := q.LockOrgTenantsShared(ctx, advisorylock.TenantID); err != nil {
		return err
	}
	if err := RecordPipelineTenants(ctx, q, p); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ImportRepositoryUsers names org's enabled pipelines that import.git from
// the allowlist entry whose normal form is key, directly or through a
// cached module — what taking the entry off a strict org's allowlist must
// not leave behind. repos is the org's allowlist as it stands (key still
// on it); modules are read from the cache only. q should hold the
// exclusive ownership lock.
func ImportRepositoryUsers(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, key string, repos []sqlc.OrgImportRepository) ([]string, error) {
	pipelines, err := q.ListEnabledPipelinesByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("listing enabled pipelines: %w", err)
	}
	var out []string
	for i := range pipelines {
		rec := &recordingModules{GitModules: pinnedimport.Cached.Modules(q, orgID, repos), key: key}
		policy := validate.TenantPolicy{Strict: true, Owns: func(string) bool { return true }, Git: rec}
		if _, err := validate.TenantLintChecked(ctx, pipelines[i].Contents, policy); err != nil {
			return nil, fmt.Errorf("tenant check: %w", err)
		}
		if rec.used {
			out = append(out, fmt.Sprintf("pipeline %q", pipelines[i].Name))
		}
	}
	return out, nil
}

// StrictComponentUsers names org's enabled pipelines that use component —
// strict tenant mode would refuse them without it on the org's list
// (0047) — directly or in a cached pinned module: what taking component off
// a strict org's list must not leave behind. p is the org's policy as it
// stands (component still on it); modules are read from the cache only. q
// should hold the exclusive ownership lock.
func StrictComponentUsers(ctx context.Context, q *sqlc.Queries, p Policy, component string) ([]string, error) {
	pipelines, err := q.ListEnabledPipelinesByOrg(ctx, p.OrgID)
	if err != nil {
		return nil, fmt.Errorf("listing enabled pipelines: %w", err)
	}
	without := p
	without.Strict = true
	without.AllowedComponents = slices.DeleteFunc(slices.Clone(p.AllowedComponents), func(c string) bool { return c == component })
	var out []string
	for i := range pipelines {
		msgs, err := LintPipeline(ctx, q, without, pinnedimport.Cached, pipelines[i].Contents)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(msgs, func(m string) bool { return validate.RefusesComponent(m, component) }) {
			out = append(out, fmt.Sprintf("pipeline %q", pipelines[i].Name))
		}
	}
	return out, nil
}

// recordingModules notes whether the lint asked about the repository key.
type recordingModules struct {
	validate.GitModules
	key  string
	used bool
}

func (r *recordingModules) Allowed(repository string) error {
	if k, err := pinnedimport.NormalizeRepository(repository); err == nil && k == r.key {
		r.used = true
	}
	return r.GitModules.Allowed(repository)
}

// MarkPinnedImportOrgsDirty marks dirty, at boot, the served configs of
// every org whose served form may differ from what was computed under the
// last boot's settings: every org in strict tenant mode now (all of them
// with requireOwned, tenants.require_owned), and every other org with an
// enabled pipeline whose strict served form differs from its stored text,
// since it was served in that form if tenants.require_owned was on before.
// The strict served form differs from the stored text in exactly two ways,
// both read from the stored text alone by parsing, never by searching it: an
// import.git inlined (validate.ServedFormProblem) and a __tenant_id__ drop
// after a labelmap (validate.InsertTenantDrops changes the text, #410). So
// no record of the last boot's settings is needed, and an org whose
// pipelines have neither is served the same either way: its collectors are
// not recomputed.
//
// A strict org's own flag cannot change between boots without a write
// (SetOrgStrictTenants), which marks its served configs dirty itself.
func MarkPinnedImportOrgsDirty(ctx context.Context, q *sqlc.Queries, requireOwned bool) error {
	orgs, err := q.ListOrgs(ctx)
	if err != nil {
		return fmt.Errorf("listing orgs: %w", err)
	}
	for i := range orgs {
		mark := requireOwned || orgs[i].StrictTenants
		if !mark {
			pipelines, err := q.ListEnabledPipelinesByOrg(ctx, orgs[i].ID)
			if err != nil {
				return fmt.Errorf("listing an org's enabled pipelines: %w", err)
			}
			for j := range pipelines {
				if strictServedFormDiffers(pipelines[j].Contents) {
					mark = true
					break
				}
			}
		}
		if !mark {
			continue
		}
		if err := q.MarkServeCacheDirtyByOrg(ctx, orgs[i].ID); err != nil {
			return fmt.Errorf("marking an org's served configs dirty: %w", err)
		}
	}
	return nil
}

// strictServedFormDiffers reports whether contents may be served differently
// in strict tenant mode (see MarkPinnedImportOrgsDirty). Text either check
// cannot read counts as differing.
func strictServedFormDiffers(contents string) bool {
	if validate.ServedFormProblem(contents) != nil {
		return true
	}
	dropped, err := validate.InsertTenantDrops(contents)
	return err != nil || dropped != contents
}
