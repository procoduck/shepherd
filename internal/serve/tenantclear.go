package serve

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/store"
)

// ClearTenantExclusions removes every serve-time exclusion of orgID,
// auditing each as cleared once: what turning strict tenant mode off for
// the org does (its pipelines are no longer checked when served).
func ClearTenantExclusions(ctx context.Context, st *store.Store, orgID pgtype.UUID) error {
	return RecordTenantExclusions(ctx, st, orgID, nil)
}

// ClearNonStrictTenantExclusions is ClearTenantExclusions for every org with
// an exclusion recorded that strict tenant mode no longer applies to (its
// flag is off and requireOwned — tenants.require_owned — is off): what boot
// runs, since the setting may have changed since the last boot.
func ClearNonStrictTenantExclusions(ctx context.Context, st *store.Store, requireOwned bool) error {
	if requireOwned {
		return nil
	}
	orgs, err := st.Queries.ListPipelineTenantExclusionOrgs(ctx)
	if err != nil {
		return fmt.Errorf("listing orgs with tenant exclusions: %w", err)
	}
	for i := range orgs {
		if orgs[i].StrictTenants {
			continue
		}
		if err := ClearTenantExclusions(ctx, st, orgs[i].OrgID); err != nil {
			return err
		}
	}
	return nil
}
