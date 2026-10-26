package mgmtapi

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/proto"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/config"
	"shepherd/internal/pinnedimport"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/tenancy"
	"shepherd/internal/validate"
)

// Strict tenant mode (internal/tenancy): an org's destinations, destination
// bindings, tenant routes and pipelines may only use tenants it owns. It
// applies when the org's strict_tenants flag is on (SetOrgStrictTenants,
// app admin) or the server-wide tenants.require_owned setting is. Where it
// is enforced:
//
//   - destination create/update and binding create/update
//     (checkStrictTenant, under the shared ownership lock, so neither the
//     org's tenants nor its flag move before the write commits);
//   - the render-time re-check of every destination (wizardDestinations):
//     wizard render/commit, destination re-renders and
//     `shepherd admin rerender-destinations`;
//   - tenant routes always use an owned tenant (resolveRouteTenant);
//   - pipeline text, by the validation gate's tenant lint
//     (validate.ValidatePipeline with the org's policy) on every save path,
//     and by Stage 3 over every pipeline a change would enable or replace.

// tenantPolicy loads orgID's strict tenant policy through q.
func tenantPolicy(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, requireOwned bool) (tenancy.Policy, error) {
	p, err := tenancy.Load(ctx, q, orgID, requireOwned)
	if err != nil {
		return tenancy.Policy{}, connect.NewError(connect.CodeInternal, errors.New("failed to load the org's tenant policy"))
	}
	return p, nil
}

// lintPolicy is orgID's policy as the validation gate takes it. An org that
// cannot be read fails closed (an internal error), never an unlinted save.
//
// src reads the pipeline's pinned import.git modules (pinnedimport): the
// service's Resolver.Saving() on a save or validation path.
func lintPolicy(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, requireOwned bool, src pinnedimport.Source) (validate.TenantPolicy, error) {
	if !orgID.Valid {
		// No org (RenderWizard's preview tolerates a missing one): nothing
		// can be saved into it, and there are no tenants to own.
		return validate.TenantPolicy{}, nil
	}
	p, err := tenantPolicy(ctx, q, orgID, requireOwned)
	if err != nil {
		return validate.TenantPolicy{}, err
	}
	return p.Validation(q, src), nil
}

// checkStrictTenant refuses (invalid_argument) a tenant orgID does not own
// when strict tenant mode applies to it. q must be the write's transaction,
// holding lockOrgTenantsShared.
func checkStrictTenant(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, requireOwned bool, tenant string) error {
	p, err := tenantPolicy(ctx, q, orgID, requireOwned)
	if err != nil {
		return err
	}
	if err := p.Check(tenant); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return nil
}

// MaxPipelineContentsBytes is the largest pipeline text a write accepts:
// 1 MiB, the gitsync.max_file_bytes default (internal/config), so a UI
// pipeline is no larger than a synced file may be, and the size the tenant
// lint reads at most (validate maxContentBytes). Served configs run to tens
// or a few hundred KiB even for a full k8s-monitoring module (agentapi
// MaxRequestBytes).
const MaxPipelineContentsBytes = config.MaxPipelineContentsBytes

// MaxRequestBytes is the largest management API request body read:
// 4 MiB — a pipeline at MaxPipelineContentsBytes plus its visual graph
// (wizard_state / GraphDocument, a node per component, typically a fraction
// of the text) with JSON escaping at most doubling either. The collector API
// caps at 8 MiB for effective_config echoes; nothing a person or a service
// account sends here is larger.
const MaxRequestBytes = 4 << 20

// rawAlloyProcedures are the procedures whose request carries Alloy text or
// a visual graph that is rendered to Alloy text — each refuses one larger
// than MaxPipelineContentsBytes (capContents / capGraph) before anything
// parses or renders it. TestRawAlloyProceduresAreCapped fails when a request
// message gains such a field and its procedure is not listed here.
var rawAlloyProcedures = map[string]bool{
	mgmtv1connect.PipelineServiceCreatePipelineProcedure:   true,
	mgmtv1connect.PipelineServiceUpdatePipelineProcedure:   true,
	mgmtv1connect.PipelineServiceValidatePipelineProcedure: true,
	mgmtv1connect.PipelineServiceFormatPipelineProcedure:   true,
	mgmtv1connect.VisualServiceRenderProcedure:             true,
	mgmtv1connect.VisualServiceValidateProcedure:           true,
	mgmtv1connect.VisualServiceUpgradeCheckProcedure:       true,
	mgmtv1connect.SimulateServiceCreateRunProcedure:        true,
}

// capContents refuses Alloy text over MaxPipelineContentsBytes.
func capContents(contents string) error {
	if len(contents) > MaxPipelineContentsBytes {
		return errPipelineTooLarge(len(contents))
	}
	return nil
}

// capGraph refuses a visual graph whose wire size is over
// MaxPipelineContentsBytes: it renders to Alloy text of about that size.
func capGraph(g proto.Message) error {
	if g == nil {
		return nil
	}
	if n := proto.Size(g); n > MaxPipelineContentsBytes {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"the graph is %d bytes; the limit is %d", n, MaxPipelineContentsBytes))
	}
	return nil
}

// errPipelineTooLarge refuses pipeline contents over MaxPipelineContentsBytes.
func errPipelineTooLarge(n int) error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
		"pipeline contents are %d bytes; the limit is %d", n, MaxPipelineContentsBytes))
}

// errValidationBusy answers a write whose tenant check hit its wall-clock
// guard (validate.ErrLintBusy): retryable, never a refusal.
var errValidationBusy = connect.NewError(connect.CodeUnavailable, validate.ErrLintBusy)

// busyOr maps a validate.ErrLintBusy anywhere in err's chain to
// errValidationBusy, and anything else to fallback.
func busyOr(err error, fallback *connect.Error) error {
	if errors.Is(err, validate.ErrLintBusy) {
		return errValidationBusy
	}
	return fallback
}

// stage3Error is a Stage 3 refusal as a connect error: failed_precondition,
// or the retryable busy when the tenant check ran out of time.
func stage3Error(err error) error {
	if errors.Is(err, validate.ErrLintBusy) {
		return errValidationBusy
	}
	return connect.NewError(connect.CodeFailedPrecondition, err)
}

// lintLocked is tenancy.LintLocked as a connect error: every pipeline write
// runs it in its writing transaction, under the shared ownership lock, so a
// write cannot commit text strict mode refuses after reading a policy the
// toggle (exclusive) has since changed.
func lintLocked(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, requireOwned bool, pipelines ...tenancy.Pipeline) error {
	err := tenancy.LintLocked(ctx, q, orgID, requireOwned, pipelines...)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, validate.ErrLintBusy):
		return errValidationBusy
	case errors.Is(err, tenancy.ErrPipelineNotOwned):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("failed to check the org's tenant policy"))
	}
}

// WithRequireOwnedTenants passes the server-wide tenants.require_owned
// setting to the pipeline write path (and the destination service built on
// it).
func WithRequireOwnedTenants(on bool) PipelineServiceOption {
	return func(s *PipelineService) { s.requireOwned = on }
}

// WithPinnedImports gives the pipeline write path (and the destination
// service built on it) the fetcher of strict mode's pinned import.git
// modules. Without it only modules already cached can be verified.
func WithPinnedImports(r *pinnedimport.Resolver) PipelineServiceOption {
	return func(s *PipelineService) { s.imports = r }
}

// WithWizardPinnedImports is WithPinnedImports for the wizard path.
func WithWizardPinnedImports(r *pinnedimport.Resolver) WizardServiceOption {
	return func(s *WizardService) { s.imports = r }
}

// WithVisualPinnedImports is WithPinnedImports for the visual builder.
func WithVisualPinnedImports(r *pinnedimport.Resolver) VisualServiceOption {
	return func(s *VisualService) { s.imports = r }
}

// WithAdminRecompute gives the strict toggle the eager recompute of an org's
// served configs (PipelineService.RecomputeOrgCaches).
func WithAdminRecompute(recompute func(ctx context.Context, orgID pgtype.UUID)) AdminServiceOption {
	return func(s *AdminService) { s.recompute = recompute }
}

// WithAdminPinnedImports is WithPinnedImports for the tenant preflight.
func WithAdminPinnedImports(r *pinnedimport.Resolver) AdminServiceOption {
	return func(s *AdminService) { s.imports = r }
}

// WithWizardRequireOwnedTenants passes tenants.require_owned to the wizard
// render/commit path.
func WithWizardRequireOwnedTenants(on bool) WizardServiceOption {
	return func(s *WizardService) { s.requireOwned = on }
}

// VisualServiceOption configures optional VisualService behaviour.
type VisualServiceOption func(*VisualService)

// WithVisualRequireOwnedTenants passes tenants.require_owned to the visual
// builder's validation.
func WithVisualRequireOwnedTenants(on bool) VisualServiceOption {
	return func(s *VisualService) { s.requireOwned = on }
}

func violationsToProto(vs []tenancy.Violation) []*mgmtv1.TenantViolation {
	out := make([]*mgmtv1.TenantViolation, len(vs))
	for i := range vs {
		out[i] = &mgmtv1.TenantViolation{
			OrgId: vs[i].OrgID, OrgName: vs[i].OrgName, Kind: vs[i].Kind,
			Name: vs[i].Name, Tenant: vs[i].Tenant, Message: vs[i].Message,
		}
	}
	return out
}

// orgStrictTenantsDetail is SetOrgStrictTenants' audit detail.
type orgStrictTenantsDetail struct {
	StrictTenants bool `json:"strict_tenants"`
	Previous      bool `json:"previous"`
}

// SetOrgStrictTenants turns an org's strict tenant mode on or off (app
// admin). Turning it on runs the preflight under the exclusive ownership
// lock — the one every destination, binding and route write takes shared —
// and refuses while anything of the org breaks the rule or the org owns no
// tenant. Turning it off always succeeds.
func (s *AdminService) SetOrgStrictTenants(ctx context.Context, req *connect.Request[mgmtv1.SetOrgStrictTenantsRequest]) (*connect.Response[mgmtv1.Org], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	on := req.Msg.GetStrictTenants()
	if on {
		// Fetch, outside the lock, any pinned import.git module the org's
		// enabled pipelines need and Shepherd has not cached: the check
		// under the exclusive lock below reads the cache only, so it never
		// holds every tenant write up on the network.
		if org, err := s.store.Queries.GetOrgByID(ctx, orgID); err == nil {
			if _, err := tenancy.Preflight(ctx, s.store.Queries, org, true, s.imports.Preflight()); err != nil {
				s.logger.Warn("strict tenants: fetching pinned modules before the preflight", "err", err, "org_id", orgID.String())
			}
		}
	}
	changed := false
	resp, err := s.inOrgTenantTx(ctx, orgID, func(q *sqlc.Queries, org sqlc.Org, _ []sqlc.OrgTenant) error {
		if org.StrictTenants == on {
			return nil
		}
		changed = true
		// A strict org is served its pinned import.git modules inlined, a
		// non-strict one as written: the toggle changes served bytes.
		if err := q.MarkServeCacheDirtyByOrg(ctx, orgID); err != nil {
			return connect.NewError(connect.CodeInternal, errors.New("failed to change strict tenant mode"))
		}
		if on {
			vs, err := tenancy.Preflight(ctx, q, org, true, pinnedimport.Cached)
			if err != nil {
				s.logger.Error("strict tenants: preflight", "err", err, "org_id", orgID.String())
				return busyOr(err, connect.NewError(connect.CodeInternal, errors.New("failed to check the org's tenants")))
			}
			if len(vs) > 0 {
				return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
					"strict tenant mode cannot be turned on for org %q until %d thing(s) are fixed: %s",
					org.Name, len(vs), tenancy.Summarize(vs)))
			}
		}
		if _, err := q.SetOrgStrictTenants(ctx, sqlc.SetOrgStrictTenantsParams{ID: orgID, StrictTenants: on}); err != nil {
			return connect.NewError(connect.CodeInternal, errors.New("failed to change strict tenant mode"))
		}
		if err := auditInsert(ctx, q, actorFromCtx(ctx), "user", orgID, "org.strict_tenants", "org", orgID.String(),
			orgStrictTenantsDetail{StrictTenants: on, Previous: org.StrictTenants}); err != nil {
			return connect.NewError(connect.CodeInternal, errors.New("failed to record the change"))
		}
		return nil
	})
	if err == nil && changed && s.recompute != nil {
		go s.recompute(context.Background(), orgID) //nolint:contextcheck,gosec // G118+contextcheck: intentional detached context, as UpdatePipeline
	}
	return resp, err
}

// TenantPreflight reports what would break the strict tenant rule (app
// admin, read-only): for one org, exactly what SetOrgStrictTenants would
// refuse over; for every org (org_id empty), what tenants.require_owned
// refuses to start over.
func (s *AdminService) TenantPreflight(ctx context.Context, req *connect.Request[mgmtv1.TenantPreflightRequest]) (*connect.Response[mgmtv1.TenantPreflightResponse], error) {
	q := s.store.Queries
	if req.Msg.GetOrgId() == "" {
		vs, err := tenancy.PreflightAll(ctx, q, s.imports.Preflight())
		if err != nil {
			s.logger.Error("tenant preflight", "err", err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("failed to run the tenant preflight"))
		}
		return connect.NewResponse(&mgmtv1.TenantPreflightResponse{Violations: violationsToProto(vs)}), nil
	}
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	org, err := q.GetOrgByID(ctx, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("org not found"))
	}
	vs, err := tenancy.Preflight(ctx, q, org, true, s.imports.Preflight())
	if err != nil {
		s.logger.Error("tenant preflight", "err", err, "org_id", orgID.String())
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to run the tenant preflight"))
	}
	return connect.NewResponse(&mgmtv1.TenantPreflightResponse{Violations: violationsToProto(vs)}), nil
}
