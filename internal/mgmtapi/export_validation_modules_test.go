package mgmtapi

import (
	"context"

	"shepherd/internal/auth"
	"shepherd/internal/store"
)

// ValidationMayFetchForTest is validationModules' caller check
// (validationMayFetch), for the external strict-tenants spec.
func ValidationMayFetchForTest(ctx context.Context, st *store.Store, orgID string) bool {
	oid, err := scanUUID(orgID)
	if err != nil {
		return false
	}
	return validationMayFetch(ctx, st, orgID, oid)
}

// ValidationMayFetchPerTeamForTest is the check validationMayFetch replaced:
// the org-editor check, then pipelineCanEdit once per team of the org. Kept
// only as the reference the one-pass decision is pinned to.
func ValidationMayFetchPerTeamForTest(ctx context.Context, st *store.Store, orgID string) bool {
	oid, err := scanUUID(orgID)
	if err != nil {
		return false
	}
	if authorizeProcedure(ctx, st, auth.SessionFromCtx(ctx), orgID, reqOrgEditor) == nil {
		return true
	}
	teams, err := st.Queries.ListTeamsByOrg(ctx, oid)
	if err != nil {
		return false
	}
	for i := range teams {
		if pipelineCanEdit(ctx, st, orgID, teams[i].ID.String()) {
			return true
		}
	}
	return false
}

// WithDelegatedServiceAccountForTest is WithServiceAccountForTest for a
// credential issued by delegatedPrincipal, presented with an on-behalf-of
// claim (onBehalfOf, "" for none), as the auth gate leaves it after
// verifying that claim.
func WithDelegatedServiceAccountForTest(ctx context.Context, orgID, role, capability, delegatedPrincipal, onBehalfOf string) context.Context {
	return withServiceAccount(ctx, serviceAccountIdentity{
		ID: "sa-test", OrgID: orgID, Name: "sa-test", Role: role, Capability: capability,
		DelegatedPrincipal: delegatedPrincipal, OnBehalfOf: onBehalfOf,
	})
}
