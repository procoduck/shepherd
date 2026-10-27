package serve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/merge"
	"shepherd/internal/pinnedimport"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/tenancy"
	"shepherd/internal/validate"
)

// Strict tenant mode's serve-time check. Every save of a strict org's
// pipeline runs the tenant check, but a pipeline saved before a check was
// added is served as it was stored. So when a strict org's pipelines are
// loaded to be served (LoadPipelines, both serve paths) or modelled (the
// Stage 3 dry runs, the reconciliation, the pipeline page), each one is
// checked as the save checks it — the tenant lint on the stored text and on
// the served form (tenancy.LintPipeline, the check `shepherd admin
// tenant-preflight` runs too) — and one it refuses carries the reason in
// merge.Pipeline.TenantProblem: merge.Assemble leaves it out of every
// collector's config and lists it in the header. It stays stored and
// enabled; saving a fix includes it again.
//
// A check that cannot finish (the lint's wall-clock guard, tried
// busyAttempts times) has no verdict: Form fails, so a serve path records
// the failure and the collector keeps its last good config — never a config
// without the pipeline, and never an unchecked one. Only a refusal excludes.
// An org not in strict tenant mode is not checked at all, so its served
// bytes cannot change.

// busyAttempts is how many times the check runs a lint that hits its
// wall-clock guard before giving up (ErrLintBusy).
const busyAttempts = 3

// maxProblemFindings is how many of the lint's findings a TenantProblem
// names; the rest are counted.
const maxProblemFindings = 3

// Serving is how one org's pipelines are served. A nil *Serving is an org
// not in strict tenant mode: its pipelines are served as stored.
type Serving struct {
	q      *sqlc.Queries
	policy tenancy.Policy
	inline Inliner
	// key is the policy's part of a verdict's cache key.
	key string
}

// OrgServing is orgID's Serving, read through q: nil when strict tenant
// mode does not apply to it (its strict_tenants flag, or requireOwned —
// tenants.require_owned).
func OrgServing(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, requireOwned bool) (*Serving, error) {
	p, err := tenancy.Load(ctx, q, orgID, requireOwned)
	if err != nil {
		return nil, err
	}
	return NewServing(q, p), nil
}

// NewServing is the Serving of an org whose policy is p, read through q (nil
// for an org not in strict tenant mode).
func NewServing(q *sqlc.Queries, p tenancy.Policy) *Serving {
	if !p.Strict {
		return nil
	}
	orgID, repos := p.OrgID, p.ImportRepos
	var b strings.Builder
	b.WriteString(orgID.String() + "\x00")
	for _, t := range p.Owned {
		b.WriteString("t" + string(t.Kind) + "\x00" + t.Tenant + "\x00")
	}
	for i := range repos {
		b.WriteString("r" + repos[i].RepositoryKey + "\x00" + repos[i].CredentialID.String() + "\x00")
	}
	// The components an app admin allowed (0047): allowing or removing one
	// changes the verdict on the same text.
	for _, c := range p.AllowedComponents {
		b.WriteString("c" + c + "\x00")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return &Serving{
		q: q, policy: p, key: hex.EncodeToString(sum[:]),
		inline: func(ctx context.Context, contents string) (string, error) {
			return pinnedimport.Inline(ctx, q, orgID, repos, contents)
		},
	}
}

// Form is pipelines as they are served (ServedForm with the org's inliner),
// each one the strict tenant check refuses carrying the reason in
// TenantProblem. A check that cannot finish fails the call (wrapping
// validate.ErrLintBusy). The input is not modified; a nil s returns it as
// given.
func (s *Serving) Form(ctx context.Context, pipelines []merge.Pipeline) ([]merge.Pipeline, error) {
	if s == nil {
		return pipelines, nil
	}
	out, err := ServedForm(ctx, s.inline, pipelines)
	if err != nil {
		return nil, err
	}
	for i := range out {
		// The check reads the stored text, as a save does: it lints that
		// and the served form it builds from it.
		problem, err := s.TenantProblem(ctx, pipelines[i].Contents)
		if err != nil {
			return nil, fmt.Errorf("pipeline %q: %w", pipelines[i].Name, err)
		}
		out[i].TenantProblem = problem
	}
	return out, nil
}

// TenantProblem is why the strict tenant check refuses a pipeline whose
// stored text is contents, or "" when it does not; err (wrapping
// validate.ErrLintBusy) when the check could not finish. Verdicts are cached
// per policy and text when CacheableVerdict: the lint is deterministic for
// both. A verdict that read pinned modules is not, since the module cache
// can change under the same text and policy, and neither is a check that
// could not finish.
func (s *Serving) TenantProblem(ctx context.Context, contents string) (string, error) {
	sum := sha256.Sum256([]byte(contents))
	key := s.key + hex.EncodeToString(sum[:])
	cacheable := CacheableVerdict(contents)
	if cacheable {
		if v, ok := verdicts.get(key); ok {
			return v, nil
		}
	}
	var msgs []string
	var err error
	for range busyAttempts {
		msgs, err = tenancy.LintPipeline(ctx, s.q, s.policy, pinnedimport.Cached, contents)
		if !errors.Is(err, validate.ErrLintBusy) {
			break
		}
	}
	if err != nil {
		return "", err
	}
	problem := summarizeFindings(msgs)
	if cacheable {
		verdicts.put(key, problem)
	}
	return problem, nil
}

// CacheableVerdict reports whether the check's verdict on contents depends
// only on the text and the policy: true when the text, parsed, holds no
// import.git anywhere (validate.ServedFormProblem), so the lint reads no
// pinned module from the cache.
func CacheableVerdict(contents string) bool {
	return validate.ServedFormProblem(contents) == nil
}

func summarizeFindings(msgs []string) string {
	if len(msgs) == 0 {
		return ""
	}
	shown := msgs[:min(len(msgs), maxProblemFindings)]
	out := strings.Join(shown, "; ")
	if rest := len(msgs) - len(shown); rest > 0 {
		out += fmt.Sprintf("; and %d more", rest)
	}
	return out
}

// maxVerdicts bounds the verdict cache; past it the cache starts over.
const maxVerdicts = 4096

type verdictCache struct {
	mu sync.Mutex
	m  map[string]string
}

var verdicts = &verdictCache{m: map[string]string{}}

func (c *verdictCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[key]
	return v, ok
}

func (c *verdictCache) put(key, v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= maxVerdicts {
		c.m = map[string]string{}
	}
	c.m[key] = v
}

// The audit_log actions RecordTenantExclusions writes.
const (
	AuditActionTenantCheckExcluded = "pipeline.tenant_check_excluded"
	AuditActionTenantCheckCleared  = "pipeline.tenant_check_cleared"
)

// RecordTenantExclusions brings pipeline_tenant_exclusions (0045) in line
// with pipelines — an org's enabled pipelines as Serving.Form returned them:
// a row for each one with a TenantProblem, none for any other. Each change is
// a compare-and-swap, and only the write that lands is audited, so an
// exclusion is audited once per change however many recomputes see it,
// never per recompute. A recompute that changes nothing reads one query.
func RecordTenantExclusions(ctx context.Context, st *store.Store, orgID pgtype.UUID, pipelines []merge.Pipeline) error {
	rows, err := st.Queries.ListPipelineTenantExclusionsByOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("listing tenant exclusions: %w", err)
	}
	stored := make(map[string]string, len(rows))
	for i := range rows {
		stored[rows[i].PipelineID.String()] = rows[i].Reason
	}
	want := map[string]string{}
	for i := range pipelines {
		if pipelines[i].TenantProblem != "" {
			want[pipelines[i].ID] = pipelines[i].TenantProblem
		}
	}
	var ids []string
	for id, reason := range want {
		if stored[id] != reason {
			ids = append(ids, id)
		}
	}
	for id := range stored {
		if _, ok := want[id]; !ok {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := recordTenantExclusion(ctx, st, orgID, id, want[id], stored[id]); err != nil {
			return err
		}
	}
	return nil
}

func recordTenantExclusion(ctx context.Context, st *store.Store, orgID pgtype.UUID, id, reason, previous string) error {
	var pid pgtype.UUID
	if err := pid.Scan(id); err != nil {
		return fmt.Errorf("pipeline id %q: %w", id, err)
	}
	tx, err := st.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	q := st.Queries.WithTx(tx)
	action := AuditActionTenantCheckExcluded
	detail := map[string]any{"reason": reason}
	if reason == "" {
		action = AuditActionTenantCheckCleared
		detail = map[string]any{"previous_reason": previous}
		_, err = q.ClearPipelineTenantExclusion(ctx, pid)
	} else {
		if previous != "" {
			detail["previous_reason"] = previous
		}
		_, err = q.SetPipelineTenantExclusion(ctx, sqlc.SetPipelineTenantExclusionParams{PipelineID: pid, OrgID: orgID, Reason: reason})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // another recompute recorded this change
	}
	if err != nil {
		return fmt.Errorf("recording the tenant exclusion: %w", err)
	}
	b, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encoding audit detail: %w", err)
	}
	if err := q.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		Actor: "serve", ActorType: "system", OrgID: orgID, Action: action,
		ResourceType: "pipeline", ResourceID: id, Detail: b,
	}); err != nil {
		return fmt.Errorf("writing audit row: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing: %w", err)
	}
	return nil
}

// recordTenantExclusionsLogged is RecordTenantExclusions on a serve path:
// best-effort, since the exclusion itself is already in what is served.
func recordTenantExclusionsLogged(ctx context.Context, st *store.Store, orgID pgtype.UUID, pipelines []merge.Pipeline) {
	if err := RecordTenantExclusions(ctx, st, orgID, pipelines); err != nil {
		slog.Error("strict tenants: recording the pipelines the serve-time check excludes failed; they are still excluded",
			"org_id", orgID.String(), "err", err)
	}
}
