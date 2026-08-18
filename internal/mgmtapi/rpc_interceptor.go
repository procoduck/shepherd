package mgmtapi

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/auth"
	"shepherd/internal/store"
)

// reqAppOrOrgAdmin is the one requirement value outside auth's Role*
// vocabulary: AdminService.SearchGroups is usable by an app admin (global
// search) or an org admin of the org named in the request (scoped search).
// See docs/api-contract-design.md's Services table.
const reqAppOrOrgAdmin = "app-or-org-admin"

// orgScoped is implemented by every org-scoped shepherd.mgmt.v1 request
// message (every generated message with `string org_id = 1;`).
type orgScoped interface {
	GetOrgId() string
}

// procedureRequirements maps every shepherd.mgmt.v1 Connect procedure to its
// authorization requirement, mirroring the Services table in
// docs/api-contract-design.md. A procedure absent from this map is denied —
// see newAuthzInterceptor.
var procedureRequirements = map[string]string{ //nolint:gochecknoglobals // static authz table, read-only after init
	// MeService — any authenticated session.
	mgmtv1connect.MeServiceGetMeProcedure: auth.RoleAny,

	// AdminService — app admin only, except SearchGroups (app admin OR org admin).
	mgmtv1connect.AdminServiceListOrgsProcedure:         auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceCreateOrgProcedure:        auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceUpdateOrgProcedure:        auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceDeleteOrgProcedure:        auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceListClustersProcedure:     auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceClaimClusterProcedure:     auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceUnclaimClusterProcedure:   auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceListAgentTokensProcedure:  auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceCreateAgentTokenProcedure: auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceRevokeAgentTokenProcedure: auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceSearchGroupsProcedure:     reqAppOrOrgAdmin,

	// FleetService — org reader for reads, org admin for writes.
	mgmtv1connect.FleetServiceListCollectorsProcedure:   auth.RoleOrgReader,
	mgmtv1connect.FleetServiceGetCollectorProcedure:     auth.RoleOrgReader,
	mgmtv1connect.FleetServiceGetServedConfigProcedure:  auth.RoleOrgReader,
	mgmtv1connect.FleetServiceListAttributesProcedure:   auth.RoleOrgReader,
	mgmtv1connect.FleetServiceCreateAssignmentProcedure: auth.RoleOrgAdmin,
	mgmtv1connect.FleetServiceDeleteAssignmentProcedure: auth.RoleOrgAdmin,

	// PipelineService — org reader for reads, org admin for writes (including
	// validation/preview endpoints, which gate on the same role as the
	// content they validate/preview).
	mgmtv1connect.PipelineServiceListPipelinesProcedure:    auth.RoleOrgReader,
	mgmtv1connect.PipelineServiceGetPipelineProcedure:      auth.RoleOrgReader,
	mgmtv1connect.PipelineServicePreviewMatchesProcedure:   auth.RoleOrgReader,
	mgmtv1connect.PipelineServiceListRevisionsProcedure:    auth.RoleOrgReader,
	mgmtv1connect.PipelineServiceCreatePipelineProcedure:   auth.RoleOrgAdmin,
	mgmtv1connect.PipelineServiceUpdatePipelineProcedure:   auth.RoleOrgAdmin,
	mgmtv1connect.PipelineServiceDeletePipelineProcedure:   auth.RoleOrgAdmin,
	mgmtv1connect.PipelineServiceEnablePipelineProcedure:   auth.RoleOrgAdmin,
	mgmtv1connect.PipelineServiceDisablePipelineProcedure:  auth.RoleOrgAdmin,
	mgmtv1connect.PipelineServiceValidatePipelineProcedure: auth.RoleOrgAdmin,

	// DestinationService — org reader for reads, org admin for writes.
	mgmtv1connect.DestinationServiceListDestinationsProcedure:  auth.RoleOrgReader,
	mgmtv1connect.DestinationServiceGetDestinationProcedure:    auth.RoleOrgReader,
	mgmtv1connect.DestinationServiceCreateDestinationProcedure: auth.RoleOrgAdmin,
	mgmtv1connect.DestinationServiceUpdateDestinationProcedure: auth.RoleOrgAdmin,
	mgmtv1connect.DestinationServiceDeleteDestinationProcedure: auth.RoleOrgAdmin,

	// GitOpsService — org admin.
	mgmtv1connect.GitOpsServiceListCredentialsProcedure:  auth.RoleOrgAdmin,
	mgmtv1connect.GitOpsServiceCreateCredentialProcedure: auth.RoleOrgAdmin,
	mgmtv1connect.GitOpsServiceDeleteCredentialProcedure: auth.RoleOrgAdmin,
	mgmtv1connect.GitOpsServiceListRepoLinksProcedure:    auth.RoleOrgAdmin,
	mgmtv1connect.GitOpsServiceCreateRepoLinkProcedure:   auth.RoleOrgAdmin,
	mgmtv1connect.GitOpsServiceDeleteRepoLinkProcedure:   auth.RoleOrgAdmin,

	// WizardService — org admin.
	mgmtv1connect.WizardServiceListWizardsProcedure:     auth.RoleOrgAdmin,
	mgmtv1connect.WizardServiceGetWizardSchemaProcedure: auth.RoleOrgAdmin,
	mgmtv1connect.WizardServiceCommitWizardProcedure:    auth.RoleOrgAdmin,

	// VisualService — org admin, except GraphView (org reader).
	mgmtv1connect.VisualServiceRenderProcedure:       auth.RoleOrgAdmin,
	mgmtv1connect.VisualServiceValidateProcedure:     auth.RoleOrgAdmin,
	mgmtv1connect.VisualServiceUpgradeCheckProcedure: auth.RoleOrgAdmin,
	mgmtv1connect.VisualServiceGraphViewProcedure:    auth.RoleOrgReader,

	// SimulateService — org admin.
	mgmtv1connect.SimulateServiceSimulateRelabelProcedure: auth.RoleOrgAdmin,
	mgmtv1connect.SimulateServiceSimulateLogsProcedure:    auth.RoleOrgAdmin,

	// AuditService — org admin.
	mgmtv1connect.AuditServiceListAuditProcedure: auth.RoleOrgAdmin,
}

// errUnknownProcedure is returned (fail closed) for any Connect procedure not
// present in procedureRequirements.
var errUnknownProcedure = errors.New("mgmtapi: unknown procedure")

// newAuthzInterceptor returns a connect.UnaryInterceptorFunc enforcing
// procedureRequirements against the session in ctx. The session is populated
// by auth.Handler.SessionMiddleware, which runs ahead of the Connect
// handlers in the same chi router group (see MountRPC in router.go) — the
// interceptor never touches cookies or headers itself.
func newAuthzInterceptor(st *store.Store) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			requirement, ok := procedureRequirements[req.Spec().Procedure]
			if !ok {
				return nil, connect.NewError(connect.CodePermissionDenied, errUnknownProcedure)
			}

			var orgID string
			if scoped, ok := req.Any().(orgScoped); ok {
				orgID = scoped.GetOrgId()
			}

			sess := auth.SessionFromCtx(ctx)
			if err := authorizeProcedure(ctx, st, sess, orgID, requirement); err != nil {
				return nil, err
			}

			return next(ctx, req)
		}
	}
}

// authorizeProcedure evaluates requirement against sess/orgID and maps
// auth.Authorize's sentinel errors to connect codes per
// docs/api-contract-design.md's error-mapping table. reqAppOrOrgAdmin tries
// the app-admin check first and falls back to the org-admin check scoped to
// orgID.
func authorizeProcedure(ctx context.Context, st *store.Store, sess *auth.Session, orgID, requirement string) error {
	if requirement != reqAppOrOrgAdmin {
		return toConnectError(auth.Authorize(ctx, st, sess, orgID, requirement))
	}
	if err := auth.Authorize(ctx, st, sess, orgID, auth.RoleAppAdmin); err == nil {
		return nil
	}
	return toConnectError(auth.Authorize(ctx, st, sess, orgID, auth.RoleOrgAdmin))
}

// toConnectError maps auth's sentinel errors to connect.Error codes.
func toConnectError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, auth.ErrUnauthenticated):
		return connect.NewError(connect.CodeUnauthenticated, err)
	case errors.Is(err, auth.ErrOrgNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, auth.ErrInvalidOrgID):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodePermissionDenied, err)
	}
}
