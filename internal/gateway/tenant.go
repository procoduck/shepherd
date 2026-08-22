package gateway

import (
	"fmt"
	"regexp"
)

// MaxTenantIDLen is the longest tenant identifier Shepherd accepts, in bytes.
//
// The number comes from Grafana Mimir's own documented limit ("Tenant IDs must
// be less-than or equal-to 150 bytes or characters in length", verified against
// grafana.com/docs/mimir/latest/configure/about-tenant-ids/ on 2026-08-22), not
// from a guess about what looks reasonable. Loki and Tempo read the same
// X-Scope-OrgID header, and Mimir is the strictest of the three we target, so
// its rule is the one worth enforcing: an identifier Shepherd accepts but a
// destination rejects fails at ingest time, far from the org-creation screen
// where it could have been refused with a useful message.
const MaxTenantIDLen = 150

// tenantIDCharsetRE is Mimir's documented allowed set: alphanumerics plus
// ! - _ . * ' ( ). Explicitly NOT slashes or whitespace, which is what makes a
// tenant id safe to place in a header value and in a route's slug without
// escaping.
var tenantIDCharsetRE = regexp.MustCompile(`^[0-9a-zA-Z!\-_.*'()]+$`)

// reservedTenantIDs are values a destination refuses or treats specially. "."
// and ".." are path-traversal shapes Mimir rejects outright;
// "__mimir_cluster" is Mimir's own internal tenant.
var reservedTenantIDs = map[string]bool{
	".":               true,
	"..":              true,
	"__mimir_cluster": true,
}

// ValidateTenantID reports whether s is usable as a tenant identifier — the
// value Shepherd injects as X-Scope-OrgID (TenantHeader) on every request a
// tenant route carries.
//
// This is the single definition of that rule. It is enforced in three places
// that must not drift: here at the API boundary when an app admin sets an
// org's tenant id, again by the CHECK constraint in
// 0013_org_tenant_id.up.sql, and implicitly by RenderHTTPRoute, which refuses
// control bytes in any case. A charset check in only one of those is the
// "enforced on one path" shape docs/gateway-tier-plan.md §10 keeps recording.
//
// Every rejection names what was wrong and what is allowed: this runs at org
// creation, where the operator can still fix it cheaply.
func ValidateTenantID(s string) error {
	if s == "" {
		return fmt.Errorf("tenant id must not be empty")
	}
	if len(s) > MaxTenantIDLen {
		return fmt.Errorf("tenant id is %d bytes; the limit is %d (Grafana Mimir's documented maximum)",
			len(s), MaxTenantIDLen)
	}
	if reservedTenantIDs[s] {
		return fmt.Errorf("tenant id %q is reserved and would be refused by the destination", s)
	}
	if !tenantIDCharsetRE.MatchString(s) {
		return fmt.Errorf("tenant id %q contains characters outside the allowed set: letters, digits, "+
			"and ! - _ . * ' ( ). Slashes and whitespace are not permitted, because this value is sent "+
			"as the %s header and embedded in route slugs", s, TenantHeader)
	}
	return nil
}
