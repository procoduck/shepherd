package serve

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/merge"
	"shepherd/internal/pinnedimport"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
)

// Inliner builds a pipeline's served text from its stored text (#390): in a
// strict org, pinnedimport.Inline. Nil serves stored text as it is.
type Inliner func(ctx context.Context, contents string) (string, error)

// ServedForm is pipelines as they are served: in a strict org (inline not
// nil) each one passed through inline, which rewrites the import.git blocks
// it finds in the parse tree and leaves every other byte — and text with no
// such block — exactly as stored; with inline nil (an org not in strict
// tenant mode) every pipeline as given. That keeps every other collector's
// bytes and hash where they are. Whatever inline returns, a served text
// that still holds an import.git (validate.ServedFormProblem) fails the
// call. A pipeline inline cannot rewrite fails
// the whole call: a strict org is never served an import.git, so the caller
// keeps the collector's last good config (RecordFailure). The input is not
// modified.
func ServedForm(ctx context.Context, inline Inliner, pipelines []merge.Pipeline) ([]merge.Pipeline, error) {
	if inline == nil {
		return pipelines, nil
	}
	out := make([]merge.Pipeline, len(pipelines))
	copy(out, pipelines)
	for i := range out {
		served, err := inline(ctx, out[i].Contents)
		if err != nil {
			return nil, fmt.Errorf("pipeline %q cannot be served with its pinned modules inlined: %w", out[i].Name, err)
		}
		// The bound, whatever the inliner did: a strict org is served no
		// import.git, however spelt, anywhere — import.string contents
		// included (validate.ServedFormProblem parses, never searches).
		if err := validate.ServedFormProblem(served); err != nil {
			return nil, fmt.Errorf("pipeline %q cannot be served: %w", out[i].Name, err)
		}
		out[i].Contents = served
	}
	return out, nil
}

// OrgInliner is orgID's Inliner: pinnedimport.Inline over its allowlist and
// module cache, read through q, when strict tenant mode applies to it (its
// strict_tenants flag, or requireOwned — tenants.require_owned); nil when it
// does not.
func OrgInliner(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, requireOwned bool) (Inliner, error) {
	strict := requireOwned
	if !strict {
		org, err := q.GetOrgByID(ctx, orgID)
		if err != nil {
			return nil, fmt.Errorf("loading the org: %w", err)
		}
		strict = org.StrictTenants
	}
	if !strict {
		return nil, nil
	}
	repos, err := q.ListOrgImportRepositories(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("loading the org's import repositories: %w", err)
	}
	return func(ctx context.Context, contents string) (string, error) {
		return pinnedimport.Inline(ctx, q, orgID, repos, contents)
	}, nil
}

// LoadPipelines is the org's enabled pipelines in their served form: the
// rows of ListEnabledPipelinesForMerge, PipelinesFromRows, then ServedForm
// with the org's Inliner. Every path that computes, models or describes
// served bytes loads them here.
func LoadPipelines(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, requireOwned bool) ([]merge.Pipeline, error) {
	rows, err := q.ListEnabledPipelinesForMerge(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("listing pipelines: %w", err)
	}
	inline, err := OrgInliner(ctx, q, orgID, requireOwned)
	if err != nil {
		return nil, err
	}
	return ServedForm(ctx, inline, PipelinesFromRows(rows))
}
