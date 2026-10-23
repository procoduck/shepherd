package mgmtapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/internal/advisorylock"
	"shepherd/internal/gateway"
	"shepherd/internal/store/sqlc"
)

// An organisation owns several tenants and tenant prefixes (org_tenants,
// 0039_org_tenants). This file is the one place that decides who owns which
// tenant: every check below asks gateway.OrgOwnsTenant.
//
// Locking: a change to ownership (AdminService's tenant procedures, CreateOrg
// with an initial tenant) takes LockOrgTenantsExclusive; every write that
// checks ownership before writing (a destination, a destination binding, a
// tenant route) takes LockOrgTenantsShared in the same transaction. A prefix
// is a relation over many tenants, so the key is one for the whole table.

// orgTenantKindFromProto maps the wire kind to gateway's, refusing
// UNSPECIFIED and unknown values.
func orgTenantKindFromProto(k mgmtv1.OrgTenantKind) (gateway.TenantKind, error) {
	switch k {
	case mgmtv1.OrgTenantKind_ORG_TENANT_KIND_EXACT:
		return gateway.TenantExact, nil
	case mgmtv1.OrgTenantKind_ORG_TENANT_KIND_PREFIX:
		return gateway.TenantPrefix, nil
	case mgmtv1.OrgTenantKind_ORG_TENANT_KIND_UNSPECIFIED:
	}
	return "", connect.NewError(connect.CodeInvalidArgument, errors.New("kind must be EXACT or PREFIX"))
}

// orgTenantKindToProto maps a stored org_tenants.kind to the wire kind.
func orgTenantKindToProto(k string) mgmtv1.OrgTenantKind {
	switch gateway.TenantKind(k) {
	case gateway.TenantExact:
		return mgmtv1.OrgTenantKind_ORG_TENANT_KIND_EXACT
	case gateway.TenantPrefix:
		return mgmtv1.OrgTenantKind_ORG_TENANT_KIND_PREFIX
	}
	return mgmtv1.OrgTenantKind_ORG_TENANT_KIND_UNSPECIFIED
}

func toOrgTenantProtos(rows []sqlc.OrgTenant) []*mgmtv1.OrgTenant {
	out := make([]*mgmtv1.OrgTenant, len(rows))
	for i := range rows {
		r := rows[i]
		out[i] = &mgmtv1.OrgTenant{
			Tenant:    r.Tenant,
			Kind:      orgTenantKindToProto(r.Kind),
			IsDefault: r.IsDefault,
			CreatedAt: protoTimestamp(r.CreatedAt),
			CreatedBy: r.CreatedBy,
		}
	}
	return out
}

func toOrgTenantRules(rows []sqlc.OrgTenant) []gateway.OrgTenant {
	out := make([]gateway.OrgTenant, len(rows))
	for i := range rows {
		out[i] = gateway.OrgTenant{Tenant: rows[i].Tenant, Kind: gateway.TenantKind(rows[i].Kind)}
	}
	return out
}

// groupOrgTenants indexes ListAllOrgTenants' rows by org.
func groupOrgTenants(rows []sqlc.OrgTenant) map[pgtype.UUID][]sqlc.OrgTenant {
	out := make(map[pgtype.UUID][]sqlc.OrgTenant)
	for i := range rows {
		out[rows[i].OrgID] = append(out[rows[i].OrgID], rows[i])
	}
	return out
}

// defaultTenant returns the org's default tenant, "" when it has none.
func defaultTenant(rows []sqlc.OrgTenant) string {
	for i := range rows {
		if rows[i].IsDefault {
			return rows[i].Tenant
		}
	}
	return ""
}

// otherOrgOwner answers, for one org, whether a tenant is owned by any OTHER
// org. Built once per request from a single query and asked per tenant.
type otherOrgOwner struct {
	rules map[pgtype.UUID][]gateway.OrgTenant
}

func loadOtherOrgOwner(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID) (otherOrgOwner, error) {
	rows, err := q.ListOrgTenantsOfOtherOrgs(ctx, orgID)
	if err != nil {
		return otherOrgOwner{}, fmt.Errorf("listing other orgs' tenants: %w", err)
	}
	rules := make(map[pgtype.UUID][]gateway.OrgTenant)
	for i := range rows {
		rules[rows[i].OrgID] = append(rules[rows[i].OrgID], gateway.OrgTenant{Tenant: rows[i].Tenant, Kind: gateway.TenantKind(rows[i].Kind)})
	}
	return otherOrgOwner{rules: rules}, nil
}

// owns reports whether some other org owns tenant.
func (o otherOrgOwner) owns(tenant string) bool {
	for _, rules := range o.rules {
		if gateway.OrgOwnsTenant(rules, tenant) {
			return true
		}
	}
	return false
}

// lockOrgTenantsShared takes the reader side of the ownership lock in q's
// transaction.
func lockOrgTenantsShared(ctx context.Context, q *sqlc.Queries) error {
	if err := q.LockOrgTenantsShared(ctx, advisorylock.TenantID); err != nil {
		return connect.NewError(connect.CodeInternal, errors.New("failed to check tenant ownership"))
	}
	return nil
}

// lockOrgTenantsExclusive takes the writer side of the ownership lock in q's
// transaction.
func lockOrgTenantsExclusive(ctx context.Context, q *sqlc.Queries) error {
	if err := q.LockOrgTenantsExclusive(ctx, advisorylock.TenantID); err != nil {
		return connect.NewError(connect.CodeInternal, errors.New("failed to lock tenant ownership"))
	}
	return nil
}

// errTenantUsedByOtherDestination is the org-side refusal of the cross-org
// tenant guard (#261, review of #277). It does not say which org: the
// destination belongs to an org whose configuration is not named here.
var errTenantUsedByOtherDestination = errors.New("the tenant is in use by another organisation's " +
	"destination or destination binding; an org may not own a tenant another org already writes under")

// orgTenantDetail is an org tenant change's audit detail.
type orgTenantDetail struct {
	Tenant    string `json:"tenant"`
	Kind      string `json:"kind"`
	IsDefault bool   `json:"is_default,omitempty"`
}

// assignOrgTenant gives orgID the rule, refusing (failed_precondition) one
// that overlaps any other org's tenants or prefixes (gateway.TenantsOverlap)
// or covers a tenant another org's destination or binding sends. q must be
// a transaction holding lockOrgTenantsExclusive; the caller writes the audit
// row (auditOrgTenantChange). rule has already been validated.
func assignOrgTenant(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, rule gateway.OrgTenant, makeDefault bool, actor string) error {
	others, err := q.ListOrgTenantsOfOtherOrgs(ctx, orgID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, errors.New("failed to check tenant ownership"))
	}
	for i := range others {
		other := gateway.OrgTenant{Tenant: others[i].Tenant, Kind: gateway.TenantKind(others[i].Kind)}
		if gateway.TenantsOverlap(rule, other) {
			// Only an app admin reaches this, and they can see every org, so
			// naming the holder is what lets them resolve it.
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
				"%s %q overlaps %s %q of org %q; a tenant belongs to at most one org",
				describeKind(rule.Kind), rule.Tenant, describeKind(other.Kind), other.Tenant, others[i].OrgName))
		}
	}
	sent, err := q.ListOtherOrgDestinationTenants(ctx, orgID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, errors.New("failed to check tenant ownership"))
	}
	for _, t := range sent {
		if gateway.RuleCovers(rule, t) {
			return connect.NewError(connect.CodeFailedPrecondition, errTenantUsedByOtherDestination)
		}
	}
	if makeDefault {
		if err := q.ClearOrgDefaultTenant(ctx, orgID); err != nil {
			return connect.NewError(connect.CodeInternal, errors.New("failed to set the default tenant"))
		}
	}
	if _, err := q.AddOrgTenant(ctx, sqlc.AddOrgTenantParams{
		OrgID: orgID, Tenant: rule.Tenant, Kind: string(rule.Kind), IsDefault: makeDefault, CreatedBy: actor,
	}); err != nil {
		if isUniqueViolation(err) {
			return connect.NewError(connect.CodeAlreadyExists,
				fmt.Errorf("%s %q is already assigned", describeKind(rule.Kind), rule.Tenant))
		}
		return connect.NewError(connect.CodeInternal, errors.New("failed to add the tenant"))
	}
	return nil
}

// auditOrgTenantChange writes an org tenant change's audit row in q's
// transaction, so the change and its record commit together.
func auditOrgTenantChange(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, action string, detail orgTenantDetail) error {
	if err := auditInsert(ctx, q, actorFromCtx(ctx), "user", orgID, action, "org", orgID.String(), detail); err != nil {
		return connect.NewError(connect.CodeInternal, errors.New("failed to record the tenant change"))
	}
	return nil
}

func describeKind(k gateway.TenantKind) string {
	switch k {
	case gateway.TenantExact:
		return "tenant"
	case gateway.TenantPrefix:
		return "tenant prefix"
	}
	return "tenant"
}

// orgProtoWithTenants loads the org's tenants through q and builds its proto.
func orgProtoWithTenants(ctx context.Context, q *sqlc.Queries, o sqlc.Org) (*mgmtv1.Org, error) {
	rows, err := q.ListOrgTenants(ctx, o.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to load org tenants"))
	}
	return toOrgProto(o, rows), nil
}

// parseOrgTenantRule reads and validates a (tenant, kind) pair from a request.
func parseOrgTenantRule(tenant string, kind mgmtv1.OrgTenantKind) (gateway.OrgTenant, error) {
	k, err := orgTenantKindFromProto(kind)
	if err != nil {
		return gateway.OrgTenant{}, err
	}
	rule := gateway.OrgTenant{Tenant: strings.TrimSpace(tenant), Kind: k}
	if err := gateway.ValidateOrgTenant(rule); err != nil {
		return gateway.OrgTenant{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return rule, nil
}

// AddOrgTenant gives an org an exact tenant or a tenant prefix (app admin).
func (s *AdminService) AddOrgTenant(ctx context.Context, req *connect.Request[mgmtv1.AddOrgTenantRequest]) (*connect.Response[mgmtv1.Org], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	rule, err := parseOrgTenantRule(req.Msg.GetTenant(), req.Msg.GetKind())
	if err != nil {
		return nil, err
	}
	if req.Msg.GetMakeDefault() && rule.Kind != gateway.TenantExact {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("only an exact tenant can be the default"))
	}
	return s.inOrgTenantTx(ctx, orgID, func(q *sqlc.Queries, _ sqlc.Org, _ []sqlc.OrgTenant) error {
		if err := assignOrgTenant(ctx, q, orgID, rule, req.Msg.GetMakeDefault(), actorFromCtx(ctx)); err != nil {
			return err
		}
		return auditOrgTenantChange(ctx, q, orgID, "org.tenant.add",
			orgTenantDetail{Tenant: rule.Tenant, Kind: string(rule.Kind), IsDefault: req.Msg.GetMakeDefault()})
	})
}

// RemoveOrgTenant takes an exact tenant or prefix away from an org (app
// admin), refusing while a live route, destination or binding of the org
// would be left on a tenant the org no longer owns — one no remaining rule
// of the org covers. The default cannot be removed while another exact
// tenant could take its place: switch the default first.
func (s *AdminService) RemoveOrgTenant(ctx context.Context, req *connect.Request[mgmtv1.RemoveOrgTenantRequest]) (*connect.Response[mgmtv1.Org], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	kind, err := orgTenantKindFromProto(req.Msg.GetKind())
	if err != nil {
		return nil, err
	}
	rule := gateway.OrgTenant{Tenant: strings.TrimSpace(req.Msg.GetTenant()), Kind: kind}
	return s.inOrgTenantTx(ctx, orgID, func(q *sqlc.Queries, org sqlc.Org, tenants []sqlc.OrgTenant) error {
		var held *sqlc.OrgTenant
		for i := range tenants {
			if tenants[i].Tenant == rule.Tenant && gateway.TenantKind(tenants[i].Kind) == rule.Kind {
				held = &tenants[i]
			}
		}
		if held == nil {
			return connect.NewError(connect.CodeNotFound,
				fmt.Errorf("org %q has no %s %q", org.Name, describeKind(rule.Kind), rule.Tenant))
		}
		// The org's rules as they would stand after the removal.
		var remaining []gateway.OrgTenant
		otherExact := false
		for i := range tenants {
			if &tenants[i] == held {
				continue
			}
			remaining = append(remaining, gateway.OrgTenant{Tenant: tenants[i].Tenant, Kind: gateway.TenantKind(tenants[i].Kind)})
			if gateway.TenantKind(tenants[i].Kind) == gateway.TenantExact {
				otherExact = true
			}
		}
		if held.IsDefault && otherExact {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
				"tenant %q is org %q's default; make another exact tenant the default first",
				rule.Tenant, org.Name))
		}
		uses, err := q.ListOrgTenantUses(ctx, orgID)
		if err != nil {
			return connect.NewError(connect.CodeInternal, errors.New("failed to check what uses the tenant"))
		}
		var orphaned []string
		for i := range uses {
			if gateway.RuleCovers(rule, uses[i].Tenant) && !gateway.OrgOwnsTenant(remaining, uses[i].Tenant) {
				orphaned = append(orphaned, fmt.Sprintf("%s %q (tenant %q)", uses[i].What, uses[i].Name, uses[i].Tenant))
			}
		}
		if len(orphaned) > 0 {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
				"%s %q is still in use and no other tenant or prefix of the org covers it: %s",
				describeKind(rule.Kind), rule.Tenant, strings.Join(orphaned, "; ")))
		}
		if _, err := q.DeleteOrgTenant(ctx, sqlc.DeleteOrgTenantParams{OrgID: orgID, Tenant: rule.Tenant, Kind: string(rule.Kind)}); err != nil {
			return connect.NewError(connect.CodeInternal, errors.New("failed to remove the tenant"))
		}
		return auditOrgTenantChange(ctx, q, orgID, "org.tenant.remove", orgTenantDetail{Tenant: rule.Tenant, Kind: string(rule.Kind), IsDefault: held.IsDefault})
	})
}

// SetDefaultOrgTenant makes one of the org's exact tenants its default (app
// admin).
func (s *AdminService) SetDefaultOrgTenant(ctx context.Context, req *connect.Request[mgmtv1.SetDefaultOrgTenantRequest]) (*connect.Response[mgmtv1.Org], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	tenant := strings.TrimSpace(req.Msg.GetTenant())
	return s.inOrgTenantTx(ctx, orgID, func(q *sqlc.Queries, org sqlc.Org, _ []sqlc.OrgTenant) error {
		if err := q.ClearOrgDefaultTenant(ctx, orgID); err != nil {
			return connect.NewError(connect.CodeInternal, errors.New("failed to set the default tenant"))
		}
		n, err := q.MarkOrgDefaultTenant(ctx, sqlc.MarkOrgDefaultTenantParams{OrgID: orgID, Tenant: tenant})
		if err != nil {
			return connect.NewError(connect.CodeInternal, errors.New("failed to set the default tenant"))
		}
		if n == 0 {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
				"%q is not an exact tenant of org %q; only one of its exact tenants can be the default", tenant, org.Name))
		}
		return auditOrgTenantChange(ctx, q, orgID, "org.tenant.set_default", orgTenantDetail{Tenant: tenant, Kind: string(gateway.TenantExact), IsDefault: true})
	})
}

// inOrgTenantTx runs change in a transaction holding the exclusive ownership
// lock, with the org and its current tenants loaded, and answers with the
// org as it stands after the commit.
func (s *AdminService) inOrgTenantTx(ctx context.Context, orgID pgtype.UUID, change func(q *sqlc.Queries, org sqlc.Org, tenants []sqlc.OrgTenant) error) (*connect.Response[mgmtv1.Org], error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to change org tenants"))
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // no-op once committed
	q := s.store.Queries.WithTx(tx)
	if err := lockOrgTenantsExclusive(ctx, q); err != nil {
		return nil, err
	}
	org, err := q.GetOrgByID(ctx, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("org not found"))
	}
	tenants, err := q.ListOrgTenants(ctx, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to load org tenants"))
	}
	if err := change(q, org, tenants); err != nil {
		return nil, err
	}
	out, err := orgProtoWithTenants(ctx, q, org)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to change org tenants"))
	}
	return connect.NewResponse(out), nil
}
