package mgmtapi

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/auth"
	"shepherd/internal/store"
)

// orgScoped is implemented by every org-scoped shepherd.mgmt.v1 request
// message (every generated message with `string org_id = 1;`).
type orgScoped interface {
	GetOrgId() string
}

// Shorthands for the requirements the table uses most; each org requirement
// is built from one auth.Role, so the table speaks the single role
// vocabulary (viewer < editor < admin, app admin above every org).
var (
	reqAuthenticated = auth.RequireAuthenticated()
	reqAppAdmin      = auth.RequireAppAdmin()
	reqOrgViewer     = auth.RequireOrg(auth.RoleViewer)
	reqOrgEditor     = auth.RequireOrg(auth.RoleEditor)
	reqOrgAdmin      = auth.RequireOrg(auth.RoleAdmin)

	reqAppAdminOrOrgAdmin = auth.RequireAppAdminOrOrg(auth.RoleAdmin)
)

// procedureRequirements maps every shepherd.mgmt.v1 Connect procedure to its
// authorization requirement, mirroring the Services table in
// docs/archive/api-contract-design.md. A procedure absent from this map is denied —
// see newAuthzInterceptor. Its effective semantics are pinned by
// testdata/procedure_requirements.golden (TestProcedureRequirementsGolden).
var procedureRequirements = map[string]auth.Requirement{
	// MeService — any authenticated session.
	mgmtv1connect.MeServiceGetMeProcedure: reqAuthenticated,

	// AdminService — app admin only, except SearchGroups and the agent-token
	// list/create/revoke (app admin OR org admin of the request's org).
	mgmtv1connect.AdminServiceListOrgsProcedure:  reqAppAdmin,
	mgmtv1connect.AdminServiceCreateOrgProcedure: reqAppAdmin,
	mgmtv1connect.AdminServiceUpdateOrgProcedure: reqAppAdmin,
	mgmtv1connect.AdminServiceDeleteOrgProcedure: reqAppAdmin,
	// Which tenants an org owns decides whose X-Scope-OrgID its routes may
	// inject, so only an app admin changes it — never the org's own admin.
	mgmtv1connect.AdminServiceAddOrgTenantProcedure:        reqAppAdmin,
	mgmtv1connect.AdminServiceRemoveOrgTenantProcedure:     reqAppAdmin,
	mgmtv1connect.AdminServiceSetDefaultOrgTenantProcedure: reqAppAdmin,
	// Strict tenant mode narrows what an org may write to its own tenants;
	// like tenant ownership, only an app admin changes or previews it.
	mgmtv1connect.AdminServiceSetOrgStrictTenantsProcedure: reqAppAdmin,
	mgmtv1connect.AdminServiceTenantPreflightProcedure:     reqAppAdmin,
	mgmtv1connect.AdminServiceListClustersProcedure:        reqAppAdmin,
	mgmtv1connect.AdminServiceClaimClusterProcedure:        reqAppAdmin,
	mgmtv1connect.AdminServiceUnclaimClusterProcedure:      reqAppAdmin,
	// Agent tokens are bound to an org. An org admin manages its own org's
	// tokens: the handlers scope every read and write to the request's
	// org_id, which this requirement has just checked; an empty org_id
	// (every token, unbound ones included) clears only for an app admin.
	// Binding or rebinding a token moves it between orgs, so it stays app
	// admin only.
	mgmtv1connect.AdminServiceListAgentTokensProcedure:     reqAppAdminOrOrgAdmin,
	mgmtv1connect.AdminServiceCreateAgentTokenProcedure:    reqAppAdminOrOrgAdmin,
	mgmtv1connect.AdminServiceRevokeAgentTokenProcedure:    reqAppAdminOrOrgAdmin,
	mgmtv1connect.AdminServiceBindAgentTokenProcedure:      reqAppAdmin,
	mgmtv1connect.AdminServiceListAgentIdentitiesProcedure: reqAppAdmin,
	mgmtv1connect.AdminServiceCreateAgentIdentityProcedure: reqAppAdmin,
	mgmtv1connect.AdminServiceDeleteAgentIdentityProcedure: reqAppAdmin,
	mgmtv1connect.AdminServiceSearchGroupsProcedure:        reqAppAdminOrOrgAdmin,

	// AdminService — OIDC single sign-on configuration. App admin only, and
	// there is no org-scoped variant on purpose: this configuration decides
	// who can hold an app-admin session in the first place, so delegating any
	// part of it to an org admin would let them widen their own reach.
	mgmtv1connect.AdminServiceGetOidcSettingsProcedure:         reqAppAdmin,
	mgmtv1connect.AdminServiceUpdateOidcSettingsProcedure:      reqAppAdmin,
	mgmtv1connect.AdminServiceTestOidcSettingsProcedure:        reqAppAdmin,
	mgmtv1connect.AdminServiceDeleteOidcSettingsProcedure:      reqAppAdmin,
	mgmtv1connect.AdminServiceListOidcProviderPresetsProcedure: reqAppAdmin,

	// UserService — app admin only, all of it. Deciding who may sign in, and
	// with what rights, is the definition of an application-administration act;
	// there is deliberately no org-scoped variant, because an org admin who
	// could mint users could mint themselves a second, wider account.
	mgmtv1connect.UserServiceListUsersProcedure:         reqAppAdmin,
	mgmtv1connect.UserServiceCreateUserProcedure:        reqAppAdmin,
	mgmtv1connect.UserServiceUpdateUserProcedure:        reqAppAdmin,
	mgmtv1connect.UserServiceDeleteUserProcedure:        reqAppAdmin,
	mgmtv1connect.UserServiceResetUserPasswordProcedure: reqAppAdmin,
	mgmtv1connect.UserServiceListOrgMembersProcedure:    reqAppAdmin,
	mgmtv1connect.UserServiceSetOrgMemberProcedure:      reqAppAdmin,
	mgmtv1connect.UserServiceRemoveOrgMemberProcedure:   reqAppAdmin,

	// FleetService — org viewer for reads, org admin for writes.
	mgmtv1connect.FleetServiceListCollectorsProcedure:       reqOrgViewer,
	mgmtv1connect.FleetServiceGetCollectorProcedure:         reqOrgViewer,
	mgmtv1connect.FleetServiceGetServedConfigProcedure:      reqOrgViewer,
	mgmtv1connect.FleetServiceGetReconciliationProcedure:    reqOrgViewer,
	mgmtv1connect.FleetServiceListAttributesProcedure:       reqOrgViewer,
	mgmtv1connect.FleetServiceListAssignmentsProcedure:      reqOrgAdmin,
	mgmtv1connect.FleetServiceCreateAssignmentProcedure:     reqOrgAdmin,
	mgmtv1connect.FleetServiceDeleteAssignmentProcedure:     reqOrgAdmin,
	mgmtv1connect.FleetServiceSetCollectorLabelProcedure:    reqOrgAdmin,
	mgmtv1connect.FleetServiceDeleteCollectorLabelProcedure: reqOrgAdmin,
	mgmtv1connect.FleetServiceRenderChartValuesProcedure:    reqOrgAdmin,

	// PipelineService — org viewer for reads. Writes
	// (create/update/delete/enable/disable) are ALSO gated at org viewer
	// here — deliberately looser than the pre-W10 org admin floor —
	// because W10's scoped write (G11) lets a non-admin team member write
	// their team's own pipeline; this table only proves "has some access
	// to this org", the fine-grained org-admin-or-owns-it decision is made
	// inside each handler by auth.AuthorizeOwnership (see
	// rpc_pipeline.go). Making that decision here instead would need this
	// table to carry the pipeline id per procedure, which it cannot
	// express — resource-scoped authorization belongs in the handler, the
	// same place loadPipeline/loadOwnedDestination already put the
	// org-scoping check. SetPipelineOwner stays org-admin-only:
	// granting/revoking ownership itself is a platform decision, not a
	// delegated one.
	mgmtv1connect.PipelineServiceListPipelinesProcedure:  reqOrgViewer,
	mgmtv1connect.PipelineServiceGetPipelineProcedure:    reqOrgViewer,
	mgmtv1connect.PipelineServicePreviewMatchesProcedure: reqOrgViewer,
	mgmtv1connect.PipelineServiceListRevisionsProcedure:  reqOrgViewer,
	mgmtv1connect.PipelineServiceGetRevisionProcedure:    reqOrgViewer,
	mgmtv1connect.PipelineServiceCreatePipelineProcedure: reqOrgViewer,
	mgmtv1connect.PipelineServiceUpdatePipelineProcedure: reqOrgViewer,
	// RestoreRevision: same row as UpdatePipeline above — this table only
	// proves "has some access to this org"; the fine-grained admin-or-
	// owns-it decision is authorizeOwnership inside the handler.
	mgmtv1connect.PipelineServiceRestoreRevisionProcedure:  reqOrgViewer,
	mgmtv1connect.PipelineServiceDeletePipelineProcedure:   reqOrgViewer,
	mgmtv1connect.PipelineServiceEnablePipelineProcedure:   reqOrgViewer,
	mgmtv1connect.PipelineServiceDisablePipelineProcedure:  reqOrgViewer,
	mgmtv1connect.PipelineServiceValidatePipelineProcedure: reqOrgViewer,
	mgmtv1connect.PipelineServiceFormatPipelineProcedure:   reqOrgViewer,
	mgmtv1connect.PipelineServiceSetPipelineOwnerProcedure: reqOrgAdmin,
	// DetachFromWizard: same row as UpdatePipeline — detaching is an edit of
	// the pipeline, and authorizeOwnership inside the handler makes the same
	// org editor-or-owning-team decision UpdatePipeline makes.
	mgmtv1connect.PipelineServiceDetachFromWizardProcedure: reqOrgViewer,

	// DestinationService — org viewer for reads, org admin for writes.
	mgmtv1connect.DestinationServiceListDestinationsProcedure:  reqOrgViewer,
	mgmtv1connect.DestinationServiceGetDestinationProcedure:    reqOrgViewer,
	mgmtv1connect.DestinationServiceCreateDestinationProcedure: reqOrgAdmin,
	mgmtv1connect.DestinationServiceUpdateDestinationProcedure: reqOrgAdmin,
	mgmtv1connect.DestinationServiceDeleteDestinationProcedure: reqOrgAdmin,

	// DestinationService — tenant bindings (W2): same viewer/admin split as
	// destinations themselves.
	mgmtv1connect.DestinationServiceListDestinationBindingsProcedure:   reqOrgViewer,
	mgmtv1connect.DestinationServiceGetDestinationBindingProcedure:     reqOrgViewer,
	mgmtv1connect.DestinationServiceResolveDestinationBindingProcedure: reqOrgViewer,
	mgmtv1connect.DestinationServiceCreateDestinationBindingProcedure:  reqOrgAdmin,
	mgmtv1connect.DestinationServiceUpdateDestinationBindingProcedure:  reqOrgAdmin,
	mgmtv1connect.DestinationServiceDeleteDestinationBindingProcedure:  reqOrgAdmin,

	// GitOpsService — org admin.
	mgmtv1connect.GitOpsServiceListCredentialsProcedure:  reqOrgAdmin,
	mgmtv1connect.GitOpsServiceCreateCredentialProcedure: reqOrgAdmin,
	mgmtv1connect.GitOpsServiceDeleteCredentialProcedure: reqOrgAdmin,
	mgmtv1connect.GitOpsServiceTestCredentialProcedure:   reqOrgAdmin,
	mgmtv1connect.GitOpsServiceListRepoLinksProcedure:    reqOrgAdmin,
	mgmtv1connect.GitOpsServiceCreateRepoLinkProcedure:   reqOrgAdmin,
	mgmtv1connect.GitOpsServiceDeleteRepoLinkProcedure:   reqOrgAdmin,

	// WizardService and SimulateService — org EDITOR (VisualService's authoring
	// procedures too, decided in its handler — see below).
	//
	// These author what the org RUNS. Changing what the org IS —
	// destinations, tenant routes, git credentials, teams, service accounts —
	// stays org admin below. Before the editor role existed there was no way to
	// grant the first without also granting the second.
	mgmtv1connect.WizardServiceListWizardsProcedure:     reqOrgEditor,
	mgmtv1connect.WizardServiceGetWizardSchemaProcedure: reqOrgEditor,
	mgmtv1connect.WizardServiceRenderWizardProcedure:    reqOrgEditor,
	mgmtv1connect.WizardServiceCommitWizardProcedure:    reqOrgEditor,

	// VisualService — Render/Validate/UpgradeCheck are org viewer HERE, like
	// PipelineService's writes, and decided per request in the handler
	// (VisualService.authorizeGraph, #319): edit rights on the named pipeline
	// when pipeline_id is set — the UpdatePipeline check, so an owning-team
	// member may use the builder on their team's pipeline — and org editor for
	// a new, unsaved graph. GraphView and DiffRevisions are org viewer.
	mgmtv1connect.VisualServiceRenderProcedure:        reqOrgViewer,
	mgmtv1connect.VisualServiceValidateProcedure:      reqOrgViewer,
	mgmtv1connect.VisualServiceUpgradeCheckProcedure:  reqOrgViewer,
	mgmtv1connect.VisualServiceGraphViewProcedure:     reqOrgViewer,
	mgmtv1connect.VisualServiceDiffRevisionsProcedure: reqOrgViewer,

	// SimulateService — org editor.
	mgmtv1connect.SimulateServiceSimulateRelabelProcedure: reqOrgEditor,
	mgmtv1connect.SimulateServiceSimulateLogsProcedure:    reqOrgEditor,
	mgmtv1connect.SimulateServiceCreateRunProcedure:       reqOrgEditor,
	mgmtv1connect.SimulateServiceGetRunProcedure:          reqOrgEditor,

	// AuditService — org admin.
	mgmtv1connect.AuditServiceListAuditProcedure: reqOrgAdmin,

	// TenantRouteService — org viewer for reads, org admin for writes
	// (create/rotate/revoke all mint or destroy routing capacity).
	mgmtv1connect.TenantRouteServiceListTenantRoutesProcedure:  reqOrgViewer,
	mgmtv1connect.TenantRouteServiceRenderConnectAppProcedure:  reqOrgViewer,
	mgmtv1connect.TenantRouteServiceCreateTenantRouteProcedure: reqOrgAdmin,
	mgmtv1connect.TenantRouteServiceRotateTenantRouteProcedure: reqOrgAdmin,
	mgmtv1connect.TenantRouteServiceRevokeTenantRouteProcedure: reqOrgAdmin,

	// TeamService — org admin (team definition is a platform decision; see
	// team.proto). A non-admin team member's write reach comes from OWNING
	// a pipeline (PipelineService above), not from managing team definitions.
	mgmtv1connect.TeamServiceListTeamsProcedure:  reqOrgViewer,
	mgmtv1connect.TeamServiceCreateTeamProcedure: reqOrgAdmin,
	mgmtv1connect.TeamServiceDeleteTeamProcedure: reqOrgAdmin,
	// The roster is org admin rather than viewer, unlike ListTeams: it
	// returns logins and email addresses, so it enumerates accounts. Who the
	// teams ARE is org-wide information; who is IN them is not.
	mgmtv1connect.TeamServiceListTeamMembersProcedure:  reqOrgAdmin,
	mgmtv1connect.TeamServiceAddTeamMemberProcedure:    reqOrgAdmin,
	mgmtv1connect.TeamServiceRemoveTeamMemberProcedure: reqOrgAdmin,

	// ServiceAccountService — org admin (minting/revoking machine
	// credentials is a platform decision, mirroring AdminService's
	// agent-token surface).
	mgmtv1connect.ServiceAccountServiceListServiceAccountsProcedure:  reqOrgAdmin,
	mgmtv1connect.ServiceAccountServiceCreateServiceAccountProcedure: reqOrgAdmin,
	mgmtv1connect.ServiceAccountServiceRevokeServiceAccountProcedure: reqOrgAdmin,
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
var capabilityRequirements = map[string]string{
	mgmtv1connect.AdminServiceCreateOrgProcedure:           capabilityApply,
	mgmtv1connect.AdminServiceUpdateOrgProcedure:           capabilityApply,
	mgmtv1connect.AdminServiceDeleteOrgProcedure:           capabilityApply,
	mgmtv1connect.AdminServiceAddOrgTenantProcedure:        capabilityApply,
	mgmtv1connect.AdminServiceRemoveOrgTenantProcedure:     capabilityApply,
	mgmtv1connect.AdminServiceSetDefaultOrgTenantProcedure: capabilityApply,
	mgmtv1connect.AdminServiceSetOrgStrictTenantsProcedure: capabilityApply,
	mgmtv1connect.AdminServiceClaimClusterProcedure:        capabilityApply,
	mgmtv1connect.AdminServiceUnclaimClusterProcedure:      capabilityApply,
	mgmtv1connect.AdminServiceCreateAgentTokenProcedure:    capabilityApply,
	mgmtv1connect.AdminServiceRevokeAgentTokenProcedure:    capabilityApply,
	mgmtv1connect.AdminServiceBindAgentTokenProcedure:      capabilityApply,
	mgmtv1connect.AdminServiceCreateAgentIdentityProcedure: capabilityApply,
	mgmtv1connect.AdminServiceDeleteAgentIdentityProcedure: capabilityApply,

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

	mgmtv1connect.FleetServiceCreateAssignmentProcedure:     capabilityApply,
	mgmtv1connect.FleetServiceDeleteAssignmentProcedure:     capabilityApply,
	mgmtv1connect.FleetServiceSetCollectorLabelProcedure:    capabilityApply,
	mgmtv1connect.FleetServiceDeleteCollectorLabelProcedure: capabilityApply,

	mgmtv1connect.PipelineServiceCreatePipelineProcedure:   capabilityApply,
	mgmtv1connect.PipelineServiceUpdatePipelineProcedure:   capabilityApply,
	mgmtv1connect.PipelineServiceDeletePipelineProcedure:   capabilityApply,
	mgmtv1connect.PipelineServiceEnablePipelineProcedure:   capabilityApply,
	mgmtv1connect.PipelineServiceDisablePipelineProcedure:  capabilityApply,
	mgmtv1connect.PipelineServiceRestoreRevisionProcedure:  capabilityApply,
	mgmtv1connect.PipelineServiceSetPipelineOwnerProcedure: capabilityApply,
	mgmtv1connect.PipelineServiceDetachFromWizardProcedure: capabilityApply,

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

	mgmtv1connect.TeamServiceCreateTeamProcedure:       capabilityApply,
	mgmtv1connect.TeamServiceDeleteTeamProcedure:       capabilityApply,
	mgmtv1connect.TeamServiceAddTeamMemberProcedure:    capabilityApply,
	mgmtv1connect.TeamServiceRemoveTeamMemberProcedure: capabilityApply,

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

// authorizeProcedure evaluates req against sess/orgID and maps
// auth.Authorize's sentinel errors to connect codes per
// docs/archive/api-contract-design.md's error-mapping table.
//
// A service-account caller (ctx set by newServiceAccountAuthGate,
// machine_auth.go) takes a separate branch: this coarse table only decides
// org REACH for a machine caller ("is this the org its token was minted
// for", and its tier), never app admin (a service account is never app-admin,
// by construction — 0012_teams_service_accounts' org_id column is required,
// not nullable-for-global) — the finer propose-vs-apply decision is G12,
// asserted per write path by requireWriteAuthorized (machine_auth.go), not
// here.
func authorizeProcedure(ctx context.Context, st *store.Store, sess *auth.Session, orgID string, req auth.Requirement) error {
	if sa, ok := serviceAccountFromCtx(ctx); ok {
		return authorizeServiceAccountProcedure(sa, orgID, req)
	}
	return toConnectError(auth.Authorize(ctx, st, sess, orgID, req))
}

// authorizeServiceAccountProcedure decides whether a machine caller may
// reach a procedure at all: org match, THEN role tier (W3-1).
// RequireAuthenticated (MeService) is granted to any authenticated machine
// identity. A requirement no org role clears (RequireAppAdmin) is always
// refused — no service account is ever app-admin. Every other requirement
// needs BOTH the token's org to match the org named in the request AND its
// tier (sa.Role, 0018_service_account_role) to be at least the
// requirement's org role — the same auth.Role comparison a human session's
// role is checked with. For RequireAppAdminOrOrg only the org half is
// reachable. A tier that is not a role denies: it never falls back to a
// default.
func authorizeServiceAccountProcedure(sa serviceAccountIdentity, orgID string, req auth.Requirement) error {
	if req.AuthenticatedOnly() {
		return nil
	}
	need, ok := req.OrgMinimum()
	if !ok {
		return connect.NewError(connect.CodePermissionDenied, errors.New("mgmtapi: service accounts are never app-admin"))
	}
	if orgID == "" || sa.OrgID != orgID {
		return connect.NewError(connect.CodePermissionDenied, errors.New("mgmtapi: service account is not scoped to this org"))
	}
	tier, err := auth.ParseRole(sa.Role)
	if err != nil {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf(
			"mgmtapi: service account %q has an unrecognised tier: %w", sa.Name, err))
	}
	if !tier.AtLeast(need) {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf(
			"mgmtapi: service account %q holds %q tier; this procedure requires %q", sa.Name, tier, need))
	}
	return nil
}

// toConnectError maps auth's sentinel errors to connect.Error codes. It
// delegates to the merged mapError (rpc_errors.go) — which already carries
// the auth.ErrUnauthenticated/ErrOrgNotFound/ErrInvalidOrgID cases this
// function used to duplicate in its own partial sentinel table (W2-S7c) —
// with one deliberate override: mapError's default is CodeInternal ("we
// don't know what this is"), but toConnectError maps the OUTCOME of an
// access check, where "reason unclear" must still mean "no access", not a
// 5xx that reads like a server bug. auth.ErrForbidden (the common case: a
// session authenticated fine but doesn't hold the required role) is exactly
// what falls through to that override today.
func toConnectError(err error) error {
	if err == nil {
		return nil
	}
	if mapped := mapError(err); connect.CodeOf(mapped) != connect.CodeInternal {
		return mapped
	}
	return connect.NewError(connect.CodePermissionDenied, err)
}
