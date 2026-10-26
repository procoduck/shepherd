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
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/advisorylock"
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
	return Policy{
		Strict: org.StrictTenants || requireOwned, OrgStrict: org.StrictTenants, Owned: Rules(rows),
		OrgID: org.ID, ImportRepos: repos,
	}, nil
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
// a validation, .Preflight() for a check of what is stored, and
// pinnedimport.Cached inside a write transaction.
func (p Policy) Validation(q *sqlc.Queries, src pinnedimport.Source) validate.TenantPolicy {
	if !p.Strict {
		return validate.TenantPolicy{}
	}
	return validate.TenantPolicy{Strict: true, Owns: p.Owns, Git: src.Modules(q, p.OrgID, p.ImportRepos), Inline: inliner(q, p.OrgID, p.ImportRepos)}
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
	p := Policy{Strict: true, OrgStrict: org.StrictTenants, Owned: Rules(rows), OrgID: org.ID, ImportRepos: repos}
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
		msgs, err := lintErrors(ctx, pipelines[i].Contents, p.Validation(q, src))
		if err != nil {
			return nil, err
		}
		if len(msgs) == 0 {
			continue
		}
		v := base
		v.Kind, v.Name = KindPipeline, pipelines[i].Name
		v.Message = fmt.Sprintf("enabled pipeline %q sets a tenant the organisation does not own (%s); "+
			"edit or disable the pipeline, or assign the tenant to the organisation",
			pipelines[i].Name, strings.Join(msgs, "; "))
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

// lintErrors is TenantLint's refusals in contents, as "line:col message";
// validate.ErrLintBusy (wrapped) when the lint hit its wall-clock guard —
// no verdict, so callers answer "busy, try again" rather than refuse.
func lintErrors(ctx context.Context, contents string, policy validate.TenantPolicy) ([]string, error) {
	// WithTenantLint lints the served form too (policy.Inline).
	r := validate.WithTenantLint(ctx, validate.Result{Valid: true}, contents, policy)
	if r.Busy {
		return nil, fmt.Errorf("tenant check: %w", validate.ErrLintBusy)
	}
	var msgs []string
	for _, d := range r.Diagnostics {
		if !d.Warning {
			msgs = append(msgs, fmt.Sprintf("%d:%d %s", d.Line, d.Col, d.Message))
		}
	}
	return msgs, nil
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
	policy := validate.TenantPolicy{
		Strict: true, Owns: func(t string) bool { return gateway.OrgOwnsTenant(remaining, t) },
		Git: pinnedimport.Cached.Modules(q, orgID, repos), Inline: inliner(q, orgID, repos),
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
// with requireOwned, tenants.require_owned), and every org with an enabled
// pipeline that may hold an import.git — decided by parsing
// (validate.ServedFormProblem), never by searching the text — since it was
// served inlined if tenants.require_owned was on before. No other org's
// collectors are recomputed.
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
				if validate.ServedFormProblem(pipelines[j].Contents) != nil {
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
