package validate_test

import (
	"context"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/gateway"
	"shepherd/internal/validate"
)

// strictFor is the policy of an org owning the exact tenant "acme" and every
// tenant under the prefix "acme-".
func strictFor() validate.TenantPolicy {
	owned := []gateway.OrgTenant{{Tenant: "acme", Kind: gateway.TenantExact}, {Tenant: "acme-", Kind: gateway.TenantPrefix}}
	return validate.TenantPolicy{Strict: true, Owns: func(t string) bool { return gateway.OrgOwnsTenant(owned, t) }}
}

func errorsOf(ds []validate.Diagnostic) []string {
	var out []string
	for _, d := range ds {
		if !d.Warning {
			out = append(out, d.Message)
		}
	}
	return out
}

func warningsOf(ds []validate.Diagnostic) []string {
	var out []string
	for _, d := range ds {
		if d.Warning {
			out = append(out, d.Message)
		}
	}
	return out
}

var _ = Describe("Strict tenant mode's pipeline lint (TenantLint)", func() {
	remoteWrite := func(tenant string) string {
		return `prometheus.remote_write "out" {
  endpoint {
    url = "https://mimir.example/api/v1/push"
    headers = {
      "X-Scope-OrgID" = ` + tenant + `,
    }
  }
}
`
	}

	It("refuses a literal X-Scope-OrgID header in prometheus.remote_write the org does not own", func() {
		ds := validate.TenantLint(remoteWrite(`"globex"`), strictFor())
		Expect(errorsOf(ds)).To(ConsistOf(ContainSubstring(`tenant "globex" is not one this organisation owns`)))
		Expect(ds[0].Line).To(Equal(5))
		Expect(ds[0].Stage).To(Equal(1))
	})

	It("accepts an owned exact tenant and one under an owned prefix", func() {
		Expect(validate.TenantLint(remoteWrite(`"acme"`), strictFor())).To(BeEmpty())
		Expect(validate.TenantLint(remoteWrite(`"acme-prod"`), strictFor())).To(BeEmpty())
	})

	It("matches the header name case-insensitively, quoted or not", func() {
		src := `loki.write "out" {
  endpoint {
    url = "https://loki.example/loki/api/v1/push"
    headers = { "x-scope-orgid" = "globex" }
  }
}
`
		Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(HaveLen(1))
	})

	It("refuses each unowned tenant of a |-joined header value", func() {
		Expect(errorsOf(validate.TenantLint(remoteWrite(`"acme|globex"`), strictFor()))).
			To(ConsistOf(ContainSubstring(`"globex"`)))
	})

	It("refuses loki.write's literal tenant_id and accepts an owned one", func() {
		src := func(t string) string {
			return `loki.write "out" {
  endpoint {
    url = "https://loki.example/loki/api/v1/push"
    tenant_id = "` + t + `"
  }
}
`
		}
		Expect(errorsOf(validate.TenantLint(src("globex"), strictFor()))).
			To(ConsistOf(ContainSubstring("loki.write endpoint tenant_id")))
		Expect(validate.TenantLint(src("acme"), strictFor())).To(BeEmpty())
	})

	It("refuses the rules components' tenant_id and loki.process stage.tenant's value", func() {
		src := `mimir.rules.kubernetes "r" {
  address = "http://mimir:8080"
  tenant_id = "globex"
}
loki.rules.kubernetes "r" {
  address = "http://loki:3100"
  tenant_id = "globex"
}
loki.process "p" {
  forward_to = []
  stage.tenant {
    value = "globex"
  }
}
`
		Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(HaveLen(3))
	})

	It("refuses an otelcol.auth.headers X-Scope-OrgID header the org does not own", func() {
		src := `otelcol.auth.headers "t" {
  header {
    key   = "X-Scope-OrgID"
    value = "globex"
  }
  header {
    key   = "Authorization"
    value = "Bearer x"
  }
}
`
		Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(ConsistOf(ContainSubstring("otelcol.auth.headers")))
	})

	It("checks headers inside otelcol exporters and declare blocks", func() {
		src := `declare "m" {
  otelcol.exporter.otlphttp "o" {
    client {
      endpoint = "https://tempo.example"
      headers  = { "X-Scope-OrgID" = "globex" }
    }
  }
}
`
		Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(HaveLen(1))
	})

	It("warns, without refusing, on a tenant set from sys.env, a reference or a concatenation", func() {
		for _, expr := range []string{`sys.env("TENANT")`, `local.file.t.content`, `"ac" + "me"`} {
			ds := validate.TenantLint(remoteWrite(expr), strictFor())
			Expect(errorsOf(ds)).To(BeEmpty(), expr)
			Expect(warningsOf(ds)).To(ConsistOf(
				"tenant set from a non-literal expression — cannot be verified in strict mode"), expr)
		}
	})

	It("warns on a headers map that is not an object literal and on stage.tenant's label", func() {
		src := `prometheus.remote_write "out" {
  endpoint {
    url     = "https://mimir.example/api/v1/push"
    headers = local.h
  }
}
loki.process "p" {
  forward_to = []
  stage.tenant {
    label = "team"
  }
}
`
		ds := validate.TenantLint(src, strictFor())
		Expect(errorsOf(ds)).To(BeEmpty())
		Expect(warningsOf(ds)).To(HaveLen(2))
	})

	It("ignores Azure AD tenant_id attributes, which name an identity tenant", func() {
		src := `discovery.azure "a" {
  oauth {
    client_id     = "x"
    tenant_id     = "globex"
    client_secret = "y"
  }
}
`
		Expect(validate.TenantLint(src, strictFor())).To(BeEmpty())
	})

	It("finds nothing when the policy is not strict (a non-strict org is unchanged)", func() {
		Expect(validate.TenantLint(remoteWrite(`"globex"`), validate.TenantPolicy{})).To(BeEmpty())
	})

	It("finds nothing in text that does not parse (Stage 1 reports it)", func() {
		Expect(validate.TenantLint(`prometheus.remote_write "x" {`, strictFor())).To(BeEmpty())
	})
})

var _ = Describe("The gate with strict tenant mode (ValidatePipeline)", func() {
	src := `prometheus.remote_write "out" {
  endpoint {
    url = "https://mimir.example/api/v1/push"
    headers = { "X-Scope-OrgID" = "globex" }
  }
}
`
	It("fails a pipeline the lint refuses, positioned in the text the user wrote", func() {
		r := noBinary().ValidatePipeline(context.Background(), "p", src, strictFor())
		Expect(r.Valid).To(BeFalse())
		Expect(positions(r.Diagnostics)).To(ContainElement(HavePrefix("4:35 ")))
	})

	It("keeps a warning-only result valid", func() {
		r := noBinary().ValidatePipeline(context.Background(), "p",
			strings.Replace(src, `"globex"`, `sys.env("T")`, 1), strictFor())
		Expect(r.Valid).To(BeTrue())
		Expect(r.Diagnostics).To(HaveLen(1))
		Expect(r.Diagnostics[0].Warning).To(BeTrue())
	})

	It("is exactly Stages 1-2 for an org not in strict mode", func() {
		r := noBinary().ValidatePipeline(context.Background(), "p", src, validate.TenantPolicy{})
		Expect(r.Valid).To(BeTrue())
		Expect(r.Diagnostics).To(BeEmpty())
	})
})
