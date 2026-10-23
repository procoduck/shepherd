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
	return Policy{Strict: org.StrictTenants || requireOwned, OrgStrict: org.StrictTenants, Owned: Rules(rows)}, nil
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
// (validate.ValidatePipeline's tenant lint).
func (p Policy) Validation() validate.TenantPolicy {
	if !p.Strict {
		return validate.TenantPolicy{}
	}
	return validate.TenantPolicy{Strict: true, Owns: p.Owns}
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
func Preflight(ctx context.Context, q *sqlc.Queries, org sqlc.Org, requireTenant bool) ([]Violation, error) {
	rows, err := q.ListOrgTenants(ctx, org.ID)
	if err != nil {
		return nil, fmt.Errorf("loading org %q's tenants: %w", org.Name, err)
	}
	p := Policy{Strict: true, OrgStrict: org.StrictTenants, Owned: Rules(rows)}
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
		var msgs []string
		for _, d := range validate.TenantLint(pipelines[i].Contents, p.Validation()) {
			if !d.Warning {
				msgs = append(msgs, fmt.Sprintf("%d:%d %s", d.Line, d.Col, d.Message))
			}
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
func PreflightAll(ctx context.Context, q *sqlc.Queries) ([]Violation, error) {
	orgs, err := q.ListOrgs(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing orgs: %w", err)
	}
	var out []Violation
	for i := range orgs {
		v, err := Preflight(ctx, q, orgs[i], false)
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
func CheckRequireOwned(ctx context.Context, q *sqlc.Queries) error {
	vs, err := PreflightAll(ctx, q)
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
// strict tenant mode, beyond the lock.
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
		if msgs := lintErrors(pipelines[i].Contents, p.Validation()); len(msgs) > 0 {
			failures = append(failures, fmt.Sprintf("%s: %s", pipelines[i].Name, strings.Join(msgs, "; ")))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%w: %s", ErrPipelineNotOwned, strings.Join(failures, " | "))
	}
	return nil
}

// lintErrors is TenantLint's refusals in contents, as "line:col message".
func lintErrors(contents string, policy validate.TenantPolicy) []string {
	var msgs []string
	for _, d := range validate.TenantLint(contents, policy) {
		if !d.Warning {
			msgs = append(msgs, fmt.Sprintf("%d:%d %s", d.Line, d.Col, d.Message))
		}
	}
	return msgs
}

// OrphanedPipelines names org's enabled pipelines that would fail the tenant
// lint if the org owned only remaining — what removing a tenant rule from a
// strict org must not leave behind. q should hold the exclusive ownership
// lock.
func OrphanedPipelines(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, remaining []gateway.OrgTenant) ([]string, error) {
	pipelines, err := q.ListEnabledPipelinesByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("listing enabled pipelines: %w", err)
	}
	policy := validate.TenantPolicy{Strict: true, Owns: func(t string) bool { return gateway.OrgOwnsTenant(remaining, t) }}
	var out []string
	for i := range pipelines {
		if msgs := lintErrors(pipelines[i].Contents, policy); len(msgs) > 0 {
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
func OtherOrgPipelinesCovered(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, rule gateway.OrgTenant) ([]string, error) {
	stale, err := q.ListStalePipelineTenantIndexOfOtherOrgs(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("listing stale pipeline tenants: %w", err)
	}
	for i := range stale {
		if err := RecordPipelineTenants(ctx, q, stale[i]); err != nil {
			return nil, err
		}
	}
	if n, err := q.CountIncompletePipelineTenantIndexOfOtherOrgs(ctx, orgID); err == nil && n > 0 {
		slog.Warn("strict tenants: other orgs' enabled pipelines set tenants the lint cannot read; the tenant "+
			"assignment check does not see them", "pipelines", n, "tenant", rule.Tenant)
	}
	rows, err := q.ListPipelineTenantsCoveredByRule(ctx, sqlc.ListPipelineTenantsCoveredByRuleParams{
		OrgID: orgID, Tenant: rule.Tenant, IsPrefix: rule.Kind == gateway.TenantPrefix,
	})
	if err != nil {
		return nil, fmt.Errorf("listing other orgs' pipeline tenants: %w", err)
	}
	var out []string
	for i := range rows {
		if gateway.RuleCovers(rule, rows[i].Tenant) {
			out = append(out, fmt.Sprintf("pipeline %q of org %q (tenant %q)", rows[i].PipelineName, rows[i].OrgName, rows[i].Tenant))
		}
	}
	return out, nil
}

// RecordPipelineTenants keeps pipeline_tenants current for p as just
// written: its rows are replaced by the tenants its text writes to
// (validate.TenantLiterals, bounded) when it is enabled, with an index row
// naming the contents they describe and whether that is all of them; both
// are removed when it is not enabled. A pipeline whose tenants the lint
// cannot all read is recorded as incomplete and logged — never a failed
// write. Every pipeline write calls it in its writing transaction.
func RecordPipelineTenants(ctx context.Context, q *sqlc.Queries, p sqlc.Pipeline) error {
	if err := q.DeletePipelineTenants(ctx, p.ID); err != nil {
		return fmt.Errorf("clearing the pipeline's tenants: %w", err)
	}
	if !p.Enabled {
		if err := q.DeletePipelineTenantIndex(ctx, p.ID); err != nil {
			return fmt.Errorf("clearing the pipeline's tenant index: %w", err)
		}
		return nil
	}
	lits, complete := validate.TenantLiterals(p.Contents)
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
