package gateway

import (
	"fmt"
	"strings"
)

// TenantKind is how an org owns a tenant: one exact tenant id, or every valid
// tenant id under a prefix (org_tenants.kind, 0039_org_tenants).
type TenantKind string

// The two TenantKind values. The strings are the org_tenants.kind column's.
const (
	TenantExact  TenantKind = "exact"
	TenantPrefix TenantKind = "prefix"
)

// MaxTenantPrefixLen is the longest tenant prefix Shepherd accepts: one byte
// short of MaxTenantIDLen, so at least one tenant id fits under it.
const MaxTenantPrefixLen = MaxTenantIDLen - 1

// OrgTenant is one tenant (or tenant prefix) an org owns.
type OrgTenant struct {
	Tenant string
	Kind   TenantKind
}

// ValidateTenantPrefix reports whether s is usable as a tenant prefix: the
// same charset as ValidateTenantID, non-empty, and at most
// MaxTenantPrefixLen bytes. The reserved tenant ids are not refused as
// prefixes — only the tenants under a prefix are tenant ids, and each of
// those is checked by ValidateTenantID where it is used.
func ValidateTenantPrefix(s string) error {
	if s == "" {
		return fmt.Errorf("tenant prefix must not be empty")
	}
	if len(s) > MaxTenantPrefixLen {
		return fmt.Errorf("tenant prefix is %d bytes; the limit is %d, one short of the tenant id limit "+
			"so a tenant still fits under it", len(s), MaxTenantPrefixLen)
	}
	if !tenantIDCharsetRE.MatchString(s) {
		return fmt.Errorf("tenant prefix %q contains characters outside the allowed set: letters, digits, "+
			"and ! - _ . * ' ( )", s)
	}
	return nil
}

// ValidateOrgTenant validates t by its kind.
func ValidateOrgTenant(t OrgTenant) error {
	switch t.Kind {
	case TenantExact:
		return ValidateTenantID(t.Tenant)
	case TenantPrefix:
		return ValidateTenantPrefix(t.Tenant)
	default:
		return fmt.Errorf("tenant kind %q is neither %q nor %q", t.Kind, TenantExact, TenantPrefix)
	}
}

// covers reports whether rule t owns tenant: an exact rule its one tenant, a
// prefix rule every tenant that starts with it.
func (t OrgTenant) covers(tenant string) bool {
	switch t.Kind {
	case TenantExact:
		return tenant == t.Tenant
	case TenantPrefix:
		return strings.HasPrefix(tenant, t.Tenant)
	default:
		return false
	}
}

// OrgOwnsTenant reports whether an org with these tenants owns tenant: it is
// one of the org's exact tenants, or a valid tenant id under one of its
// prefixes. This is the one definition of "owned" — tenant routes, the
// cross-org destination and binding checks and the removal guard all ask it.
func OrgOwnsTenant(tenants []OrgTenant, tenant string) bool {
	if ValidateTenantID(tenant) != nil {
		return false
	}
	for _, t := range tenants {
		if t.covers(tenant) {
			return true
		}
	}
	return false
}

// RuleCovers reports whether rule t, on its own, owns tenant — the
// single-rule form of OrgOwnsTenant the removal guard uses to find what a
// rule being removed still serves.
func RuleCovers(t OrgTenant, tenant string) bool {
	return OrgOwnsTenant([]OrgTenant{t}, tenant)
}

// TenantsOverlap reports whether rules a and b, held by two different orgs,
// would let both orgs own one tenant: two equal exact tenants, an exact
// tenant under the other's prefix, or two prefixes where either is a prefix
// of the other.
func TenantsOverlap(a, b OrgTenant) bool {
	switch {
	case a.Kind == TenantExact && b.Kind == TenantExact:
		return a.Tenant == b.Tenant
	case a.Kind == TenantExact:
		return b.covers(a.Tenant)
	case b.Kind == TenantExact:
		return a.covers(b.Tenant)
	default:
		return strings.HasPrefix(a.Tenant, b.Tenant) || strings.HasPrefix(b.Tenant, a.Tenant)
	}
}
