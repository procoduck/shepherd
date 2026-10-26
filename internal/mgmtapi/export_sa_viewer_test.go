package mgmtapi

import (
	"context"
	"sort"

	"shepherd/internal/auth"
	"shepherd/internal/store"
)

// ProcedureNamesForTest lists every procedure in the interceptor table,
// sorted, for the external viewer-parity spec (service_account_viewer_test.go).
func ProcedureNamesForTest() []string {
	names := make([]string, 0, len(procedureRequirements))
	for p := range procedureRequirements {
		names = append(names, p)
	}
	sort.Strings(names)
	return names
}

// AuthorizeProcedureForTest runs the interceptor's decision for procedure
// exactly as newAuthzInterceptor does: a service account in ctx takes the
// machine branch, otherwise sess is checked.
func AuthorizeProcedureForTest(ctx context.Context, st *store.Store, sess *auth.Session, orgID, procedure string) error {
	return authorizeProcedure(ctx, st, sess, orgID, procedureRequirements[procedure])
}

// WithServiceAccountForTest puts a machine identity in ctx, as
// newServiceAccountAuthGate does after authenticating one.
func WithServiceAccountForTest(ctx context.Context, orgID, role, capability string) context.Context {
	return withServiceAccount(ctx, serviceAccountIdentity{
		ID: "sa-test", OrgID: orgID, Name: "sa-test", Role: role, Capability: capability,
	})
}
