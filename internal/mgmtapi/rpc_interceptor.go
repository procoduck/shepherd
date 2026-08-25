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
// See docs/archive/api-contract-design.md's Services table.
const reqAppOrOrgAdmin = "app-or-org-admin"

// orgScoped is implemented by every org-scoped shepherd.mgmt.v1 request
// message (every generated message with `string org_id = 1;`).
type orgScoped interface {
	GetOrgId() string
}

// procedureRequirements maps every shepherd.mgmt.v1 Connect procedure to its
// authorization requirement, mirroring the Services table in
// docs/archive/api-contract-design.md. A procedure absent from this map is denied —
// see newAuthzInterceptor.
var procedureRequirements = map[string]string{ //nolint:gochecknoglobals // static authz table, read-only after init
	// MeService — any authenticated session.
	mgmtv1connect.MeServiceGetMeProcedure: auth.RoleAny,

	// AdminService — app admin only, except SearchGroups (app admin OR org admin).
	mgmtv1connect.AdminServiceListOrgsProcedure:         auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceCreateOrgProcedure:        auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceUpdateOrgProcedure:        auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceDeleteOrgProcedure:        auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceSetOrgTenantIDProcedure:   auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceListClustersProcedure:     auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceClaimClusterProcedure:     auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceUnclaimClusterProcedure:   auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceListAgentTokensProcedure:  auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceCreateAgentTokenProcedure: auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceRevokeAgentTokenProcedure: auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceSearchGroupsProcedure:     reqAppOrOrgAdmin,

	// AdminService — OIDC single sign-on configuration. App admin only, and
	// there is no org-scoped variant on purpose: this configuration decides
	// who can hold an app-admin session in the first place, so delegating any
	// part of it to an org admin would let them widen their own reach.
	mgmtv1connect.AdminServiceGetOidcSettingsProcedure:         auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceUpdateOidcSettingsProcedure:      auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceTestOidcSettingsProcedure:        auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceDeleteOidcSettingsProcedure:      auth.RoleAppAdmin,
	mgmtv1connect.AdminServiceListOidcProviderPresetsProcedure: auth.RoleAppAdmin,

	// UserService — app admin only, all of it. Deciding who may sign in, and
	// with what rights, is the definition of an application-administration act;
	// there is deliberately no org-scoped variant, because an org admin who
	// could mint users could mint themselves a second, wider account.
	mgmtv1connect.UserServiceListUsersProcedure:         auth.RoleAppAdmin,
	mgmtv1connect.UserServiceCreateUserProcedure:        auth.RoleAppAdmin,
	mgmtv1connect.UserServiceUpdateUserProcedure:        auth.RoleAppAdmin,
	mgmtv1connect.UserServiceDeleteUserProcedure:        auth.RoleAppAdmin,
	mgmtv1connect.UserServiceResetUserPasswordProcedure: auth.RoleAppAdmin,
	mgmtv1connect.UserServiceListOrgMembersProcedure:    auth.RoleAppAdmin,
	mgmtv1connect.UserServiceSetOrgMemberProcedure:      auth.RoleAppAdmin,
	mgmtv1connect.UserServiceRemoveOrgMemberProcedure:   auth.RoleAppAdmin,

	// FleetService — org reader for reads, org admin for writes.
	mgmtv1connect.FleetServiceListCollectorsProcedure:   auth.RoleOrgReader,
	mgmtv1connect.FleetServiceGetCollectorProcedure:     auth.RoleOrgReader,
	mgmtv1connect.FleetServiceGetServedConfigProcedure:  auth.RoleOrgReader,
	mgmtv1connect.FleetServiceListAttributesProcedure:   auth.RoleOrgReader,
	mgmtv1connect.FleetServiceListAssignmentsProcedure:  auth.RoleOrgAdmin,
	mgmtv1connect.FleetServiceCreateAssignmentProcedure: auth.RoleOrgAdmin,
	mgmtv1connect.FleetServiceDeleteAssignmentProcedure: auth.RoleOrgAdmin,

	// PipelineService — org reader for reads. Writes
	// (create/update/delete/enable/disable) are ALSO gated at org-reader
	// here — deliberately looser than the pre-W10 org-admin floor —
	// because W10's scoped write (G11) lets a non-admin team member write
	// their team's own pipeline; this table only proves "has some access
	// to this org", the fine-grained org-admin-OR-owns-it decision is made
	// inside each handler by auth.AuthorizeOwnership (see
	// rpc_pipeline.go). Making that decision here instead would need this
	// table to carry the pipeline id per procedure, which it cannot
	// express — resource-scoped authorization belongs in the handler, the
	// same place loadPipeline/loadOwnedDestination already put the
	// org-scoping check. SetPipelineOwner stays org-admin-only:
	// granting/revoking ownership itself is a platform decision, not a
	// delegated one.
	mgmtv1connect.PipelineServiceListPipelinesProcedure:    auth.RoleOrgReader,
	mgmtv1connect.PipelineServiceGetPipelineProcedure:      auth.RoleOrgReader,
	mgmtv1connect.PipelineServicePreviewMatchesProcedure:   auth.RoleOrgReader,
	mgmtv1connect.PipelineServiceListRevisionsProcedure:    auth.RoleOrgReader,
	mgmtv1connect.PipelineServiceCreatePipelineProcedure:   auth.RoleOrgReader,
	mgmtv1connect.PipelineServiceUpdatePipelineProcedure:   auth.RoleOrgReader,
	mgmtv1connect.PipelineServiceDeletePipelineProcedure:   auth.RoleOrgReader,
	mgmtv1connect.PipelineServiceEnablePipelineProcedure:   auth.RoleOrgReader,
	mgmtv1connect.PipelineServiceDisablePipelineProcedure:  auth.RoleOrgReader,
	mgmtv1connect.PipelineServiceValidatePipelineProcedure: auth.RoleOrgReader,
	mgmtv1connect.PipelineServiceSetPipelineOwnerProcedure: auth.RoleOrgAdmin,

	// DestinationService — org reader for reads, org admin for writes.
	mgmtv1connect.DestinationServiceListDestinationsProcedure:  auth.RoleOrgReader,
	mgmtv1connect.DestinationServiceGetDestinationProcedure:    auth.RoleOrgReader,
	mgmtv1connect.DestinationServiceCreateDestinationProcedure: auth.RoleOrgAdmin,
	mgmtv1connect.DestinationServiceUpdateDestinationProcedure: auth.RoleOrgAdmin,
	mgmtv1connect.DestinationServiceDeleteDestinationProcedure: auth.RoleOrgAdmin,

	// DestinationService — tenant bindings (W2): same reader/admin split as
	// destinations themselves.
	mgmtv1connect.DestinationServiceListDestinationBindingsProcedure:   auth.RoleOrgReader,
	mgmtv1connect.DestinationServiceGetDestinationBindingProcedure:     auth.RoleOrgReader,
	mgmtv1connect.DestinationServiceResolveDestinationBindingProcedure: auth.RoleOrgReader,
	mgmtv1connect.DestinationServiceCreateDestinationBindingProcedure:  auth.RoleOrgAdmin,
	mgmtv1connect.DestinationServiceUpdateDestinationBindingProcedure:  auth.RoleOrgAdmin,
	mgmtv1connect.DestinationServiceDeleteDestinationBindingProcedure:  auth.RoleOrgAdmin,

	// GitOpsService — org admin.
	mgmtv1connect.GitOpsServiceListCredentialsProcedure:  auth.RoleOrgAdmin,
	mgmtv1connect.GitOpsServiceCreateCredentialProcedure: auth.RoleOrgAdmin,
	mgmtv1connect.GitOpsServiceDeleteCredentialProcedure: auth.RoleOrgAdmin,
	mgmtv1connect.GitOpsServiceTestCredentialProcedure:   auth.RoleOrgAdmin,
	mgmtv1connect.GitOpsServiceListRepoLinksProcedure:    auth.RoleOrgAdmin,
	mgmtv1connect.GitOpsServiceCreateRepoLinkProcedure:   auth.RoleOrgAdmin,
	mgmtv1connect.GitOpsServiceDeleteRepoLinkProcedure:   auth.RoleOrgAdmin,

	// WizardService, VisualService and SimulateService — org EDITOR.
	//
	// These author what the org RUNS. Changing what the org IS —
	// destinations, tenant routes, git credentials, teams, service accounts —
	// stays org admin below. Before the editor role existed there was no way to
	// grant the first without also granting the second.
	mgmtv1connect.WizardServiceListWizardsProcedure:     auth.RoleOrgEditor,
	mgmtv1connect.WizardServiceGetWizardSchemaProcedure: auth.RoleOrgEditor,
	mgmtv1connect.WizardServiceRenderWizardProcedure:    auth.RoleOrgEditor,
	mgmtv1connect.WizardServiceCommitWizardProcedure:    auth.RoleOrgEditor,

	// VisualService — org editor, except GraphView (org reader).
	mgmtv1connect.VisualServiceRenderProcedure:       auth.RoleOrgEditor,
	mgmtv1connect.VisualServiceValidateProcedure:     auth.RoleOrgEditor,
	mgmtv1connect.VisualServiceUpgradeCheckProcedure: auth.RoleOrgEditor,
	mgmtv1connect.VisualServiceGraphViewProcedure:    auth.RoleOrgReader,

	// SimulateService — org editor.
	mgmtv1connect.SimulateServiceSimulateRelabelProcedure: auth.RoleOrgEditor,
	mgmtv1connect.SimulateServiceSimulateLogsProcedure:    auth.RoleOrgEditor,
	mgmtv1connect.SimulateServiceCreateRunProcedure:       auth.RoleOrgEditor,
	mgmtv1connect.SimulateServiceGetRunProcedure:          auth.RoleOrgEditor,

	// AuditService — org admin.
	mgmtv1connect.AuditServiceListAuditProcedure: auth.RoleOrgAdmin,

	// TenantRouteService — org reader for reads, org admin for writes
	// (create/rotate/revoke all mint or destroy routing capacity).
	mgmtv1connect.TenantRouteServiceListTenantRoutesProcedure:  auth.RoleOrgReader,
	mgmtv1connect.TenantRouteServiceCreateTenantRouteProcedure: auth.RoleOrgAdmin,
	mgmtv1connect.TenantRouteServiceRotateTenantRouteProcedure: auth.RoleOrgAdmin,
	mgmtv1connect.TenantRouteServiceRevokeTenantRouteProcedure: auth.RoleOrgAdmin,

	// TeamService — org admin (team definition is a platform decision; see
	// team.proto). A non-admin team member's write reach comes from OWNING
	// a pipeline (PipelineService above), not from managing team definitions.
	mgmtv1connect.TeamServiceListTeamsProcedure:  auth.RoleOrgReader,
	mgmtv1connect.TeamServiceCreateTeamProcedure: auth.RoleOrgAdmin,
	mgmtv1connect.TeamServiceDeleteTeamProcedure: auth.RoleOrgAdmin,

	// ServiceAccountService — org admin (minting/revoking machine
	// credentials is a platform decision, mirroring AdminService's
	// agent-token surface).
	mgmtv1connect.ServiceAccountServiceListServiceAccountsProcedure:  auth.RoleOrgAdmin,
	mgmtv1connect.ServiceAccountServiceCreateServiceAccountProcedure: auth.RoleOrgAdmin,
	mgmtv1connect.ServiceAccountServiceRevokeServiceAccountProcedure: auth.RoleOrgAdmin,
}

// capability values a mutating procedure may require of a service-account
// caller (G12). "" (absent from capabilityRequirements) means the
// procedure is not a write — a propose-scoped OR apply-scoped token may
// call it freely, and a human session is never capability-scoped at all.
// capabilityApply means the procedure performs a write:
// requireWriteAuthorized (machine_auth.go), called individually inside
// each such handler, is what actually enforces this — this table's job is
// only to make TestEveryMutatingProcedureIsCapabilityClassified
// (capability_enumeration_test.go) fail the moment a new mutating RPC is
// added without a classification decision, so "forgot to gate the new
// write path" cannot happen silently.
var capabilityRequirements = map[string]string{ //nolint:gochecknoglobals // static classification table, read-only after init
	mgmtv1connect.AdminServiceCreateOrgProcedure:        capabilityApply,
	mgmtv1connect.AdminServiceUpdateOrgProcedure:        capabilityApply,
	mgmtv1connect.AdminServiceDeleteOrgProcedure:        capabilityApply,
	mgmtv1connect.AdminServiceSetOrgTenantIDProcedure:   capabilityApply,
	mgmtv1connect.AdminServiceClaimClusterProcedure:     capabilityApply,
	mgmtv1connect.AdminServiceUnclaimClusterProcedure:   capabilityApply,
	mgmtv1connect.AdminServiceCreateAgentTokenProcedure: capabilityApply,
	mgmtv1connect.AdminServiceRevokeAgentTokenProcedure: capabilityApply,

	// Repointing or removing the identity provider is as apply-only as a write
	// gets: it decides who can authenticate at all. A service account can
	// never reach these anyway (a service account is never app-admin), but the
	// classification is what makes that explicit rather than incidental.
	mgmtv1connect.AdminServiceUpdateOidcSettingsProcedure: capabilityApply,
	mgmtv1connect.AdminServiceDeleteOidcSettingsProcedure: capabilityApply,

	// Creating accounts and granting roles is as apply-only as it gets.
	mgmtv1connect.UserServiceCreateUserProcedure:        capabilityApply,
	mgmtv1connect.UserServiceUpdateUserProcedure:        capabilityApply,
	mgmtv1connect.UserServiceDeleteUserProcedure:        capabilityApply,
	mgmtv1connect.UserServiceResetUserPasswordProcedure: capabilityApply,
	mgmtv1connect.UserServiceSetOrgMemberProcedure:      capabilityApply,
	mgmtv1connect.UserServiceRemoveOrgMemberProcedure:   capabilityApply,

	mgmtv1connect.FleetServiceCreateAssignmentProcedure: capabilityApply,
	mgmtv1connect.FleetServiceDeleteAssignmentProcedure: capabilityApply,

	mgmtv1connect.PipelineServiceCreatePipelineProcedure:   capabilityApply,
	mgmtv1connect.PipelineServiceUpdatePipelineProcedure:   capabilityApply,
	mgmtv1connect.PipelineServiceDeletePipelineProcedure:   capabilityApply,
	mgmtv1connect.PipelineServiceEnablePipelineProcedure:   capabilityApply,
	mgmtv1connect.PipelineServiceDisablePipelineProcedure:  capabilityApply,
	mgmtv1connect.PipelineServiceSetPipelineOwnerProcedure: capabilityApply,

	mgmtv1connect.DestinationServiceCreateDestinationProcedure: capabilityApply,
	mgmtv1connect.DestinationServiceUpdateDestinationProcedure: capabilityApply,
	mgmtv1connect.DestinationServiceDeleteDestinationProcedure: capabilityApply,

	// W2's tenant bindings. A binding cannot carry a credential (0008 has no
	// column for one), but it does decide which tenant a team's telemetry
	// ships under, so writing one is an apply, not a propose.
	mgmtv1connect.DestinationServiceCreateDestinationBindingProcedure: capabilityApply,
	mgmtv1connect.DestinationServiceUpdateDestinationBindingProcedure: capabilityApply,
	mgmtv1connect.DestinationServiceDeleteDestinationBindingProcedure: capabilityApply,

	mgmtv1connect.GitOpsServiceCreateCredentialProcedure: capabilityApply,
	mgmtv1connect.GitOpsServiceDeleteCredentialProcedure: capabilityApply,
	mgmtv1connect.GitOpsServiceCreateRepoLinkProcedure:   capabilityApply,
	mgmtv1connect.GitOpsServiceDeleteRepoLinkProcedure:   capabilityApply,

	mgmtv1connect.WizardServiceCommitWizardProcedure: capabilityApply,

	mgmtv1connect.TenantRouteServiceCreateTenantRouteProcedure: capabilityApply,
	mgmtv1connect.TenantRouteServiceRotateTenantRouteProcedure: capabilityApply,
	mgmtv1connect.TenantRouteServiceRevokeTenantRouteProcedure: capabilityApply,

	mgmtv1connect.TeamServiceCreateTeamProcedure: capabilityApply,
	mgmtv1connect.TeamServiceDeleteTeamProcedure: capabilityApply,

	mgmtv1connect.ServiceAccountServiceCreateServiceAccountProcedure: capabilityApply,
	mgmtv1connect.ServiceAccountServiceRevokeServiceAccountProcedure: capabilityApply,
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
// docs/archive/api-contract-design.md's error-mapping table. reqAppOrOrgAdmin tries
// the app-admin check first and falls back to the org-admin check scoped to
// orgID.
//
// A service-account caller (ctx set by newServiceAccountAuthInterceptor,
// machine_auth.go) takes a separate branch: this coarse table only decides
// org REACH for a machine caller ("is this the org its token was minted
// for"), never RoleAppAdmin (a service account is never app-admin, by
// construction — 0012_teams_service_accounts' org_id column is required,
// not nullable-for-global) — the finer propose-vs-apply decision is G12,
// asserted per write path by requireWriteAuthorized (machine_auth.go), not
// here.
func authorizeProcedure(ctx context.Context, st *store.Store, sess *auth.Session, orgID, requirement string) error {
	if sa, ok := serviceAccountFromCtx(ctx); ok {
		return authorizeServiceAccountProcedure(sa, orgID, requirement)
	}
	if requirement != reqAppOrOrgAdmin {
		return toConnectError(auth.Authorize(ctx, st, sess, orgID, requirement))
	}
	if err := auth.Authorize(ctx, st, sess, orgID, auth.RoleAppAdmin); err == nil {
		return nil
	}
	return toConnectError(auth.Authorize(ctx, st, sess, orgID, auth.RoleOrgAdmin))
}

// authorizeServiceAccountProcedure decides whether a machine caller may
// reach a procedure at all (org match). auth.RoleAppAdmin is always
// refused — no service account is ever app-admin. auth.RoleAny (MeService)
// is granted to any authenticated machine identity; every other
// requirement (RoleOrgAdmin, RoleOrgReader, reqAppOrOrgAdmin) requires the
// token's org to match the org named in the request.
func authorizeServiceAccountProcedure(sa serviceAccountIdentity, orgID, requirement string) error {
	switch requirement {
	case auth.RoleAny:
		return nil
	case auth.RoleAppAdmin:
		return connect.NewError(connect.CodePermissionDenied, errors.New("mgmtapi: service accounts are never app-admin"))
	default: // RoleOrgAdmin, RoleOrgReader, reqAppOrOrgAdmin
		if orgID == "" || sa.OrgID != orgID {
			return connect.NewError(connect.CodePermissionDenied, errors.New("mgmtapi: service account is not scoped to this org"))
		}
		return nil
	}
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
