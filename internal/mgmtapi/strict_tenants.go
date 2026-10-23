package mgmtapi

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
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
func lintPolicy(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, requireOwned bool) (validate.TenantPolicy, error) {
	if !orgID.Valid {
		// No org (RenderWizard's preview tolerates a missing one): nothing
		// can be saved into it, and there are no tenants to own.
		return validate.TenantPolicy{}, nil
	}
	p, err := tenantPolicy(ctx, q, orgID, requireOwned)
	if err != nil {
		return validate.TenantPolicy{}, err
	}
	return p.Validation(), nil
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

// WithRequireOwnedTenants passes the server-wide tenants.require_owned
// setting to the pipeline write path (and the destination service built on
// it).
func WithRequireOwnedTenants(on bool) PipelineServiceOption {
	return func(s *PipelineService) { s.requireOwned = on }
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
	return s.inOrgTenantTx(ctx, orgID, func(q *sqlc.Queries, org sqlc.Org, _ []sqlc.OrgTenant) error {
		if org.StrictTenants == on {
			return nil
		}
		if on {
			vs, err := tenancy.Preflight(ctx, q, org, true)
			if err != nil {
				s.logger.Error("strict tenants: preflight", "err", err, "org_id", orgID.String())
				return connect.NewError(connect.CodeInternal, errors.New("failed to check the org's tenants"))
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
}

// TenantPreflight reports what would break the strict tenant rule (app
// admin, read-only): for one org, exactly what SetOrgStrictTenants would
// refuse over; for every org (org_id empty), what tenants.require_owned
// refuses to start over.
func (s *AdminService) TenantPreflight(ctx context.Context, req *connect.Request[mgmtv1.TenantPreflightRequest]) (*connect.Response[mgmtv1.TenantPreflightResponse], error) {
	q := s.store.Queries
	if req.Msg.GetOrgId() == "" {
		vs, err := tenancy.PreflightAll(ctx, q)
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
	vs, err := tenancy.Preflight(ctx, q, org, true)
	if err != nil {
		s.logger.Error("tenant preflight", "err", err, "org_id", orgID.String())
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to run the tenant preflight"))
	}
	return connect.NewResponse(&mgmtv1.TenantPreflightResponse{Violations: violationsToProto(vs)}), nil
}
