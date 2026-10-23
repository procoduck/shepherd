package gateway

import (
	"strings"
	"testing"
)

func TestOrgOwnsTenant(t *testing.T) {
	tenants := []OrgTenant{
		{Tenant: "acme", Kind: TenantExact},
		{Tenant: "acme-team-", Kind: TenantPrefix},
	}
	cases := []struct {
		tenant string
		want   bool
	}{
		{"acme", true},
		{"acme-team-a", true},
		{"acme-team-", true}, // the prefix itself is a valid tenant under it
		{"acme-other", false},
		{"acm", false},
		{"globex", false},
		{"", false},
		{"acme-team-a/evil", false}, // under the prefix, but not a valid tenant id
		{"acme-team-" + strings.Repeat("x", MaxTenantIDLen), false},
	}
	for _, tc := range cases {
		if got := OrgOwnsTenant(tenants, tc.tenant); got != tc.want {
			t.Errorf("OrgOwnsTenant(%q) = %v, want %v", tc.tenant, got, tc.want)
		}
	}
	if OrgOwnsTenant(nil, "acme") {
		t.Error("an org with no tenants owns nothing")
	}
}

func TestTenantsOverlap(t *testing.T) {
	ex := func(s string) OrgTenant { return OrgTenant{Tenant: s, Kind: TenantExact} }
	px := func(s string) OrgTenant { return OrgTenant{Tenant: s, Kind: TenantPrefix} }
	cases := []struct {
		name string
		a, b OrgTenant
		want bool
	}{
		{"same exact", ex("acme"), ex("acme"), true},
		{"different exact", ex("acme"), ex("globex"), false},
		{"exact under prefix", ex("acme-a"), px("acme-"), true},
		{"prefix covering exact", px("acme-"), ex("acme-a"), true},
		{"exact equal to prefix", ex("acme-"), px("acme-"), true},
		{"exact not under prefix", ex("acme"), px("acme-"), false},
		{"same prefix", px("acme-"), px("acme-"), true},
		{"prefix of prefix", px("acme-"), px("acme-team-"), true},
		{"prefix of prefix, reversed", px("acme-team-"), px("acme-"), true},
		{"disjoint prefixes", px("acme-"), px("globex-"), false},
		{"sibling prefixes", px("acme-a-"), px("acme-b-"), false},
	}
	for _, tc := range cases {
		if got := TenantsOverlap(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: TenantsOverlap = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestValidateTenantPrefix(t *testing.T) {
	for _, ok := range []string{"acme-", "a", "team.", "x!-_.*'()", strings.Repeat("a", MaxTenantPrefixLen)} {
		if err := ValidateTenantPrefix(ok); err != nil {
			t.Errorf("ValidateTenantPrefix(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "acme/", "acme team", strings.Repeat("a", MaxTenantPrefixLen+1)} {
		if err := ValidateTenantPrefix(bad); err == nil {
			t.Errorf("ValidateTenantPrefix(%q) = nil, want an error", bad)
		}
	}
}

func TestValidateOrgTenant(t *testing.T) {
	if err := ValidateOrgTenant(OrgTenant{Tenant: "__mimir_cluster", Kind: TenantExact}); err == nil {
		t.Error("a reserved exact tenant must be refused")
	}
	if err := ValidateOrgTenant(OrgTenant{Tenant: "acme", Kind: "other"}); err == nil {
		t.Error("an unknown kind must be refused")
	}
	if err := ValidateOrgTenant(OrgTenant{Tenant: "acme-", Kind: TenantPrefix}); err != nil {
		t.Errorf("a valid prefix: %v", err)
	}
}
