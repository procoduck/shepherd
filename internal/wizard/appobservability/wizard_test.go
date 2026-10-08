package appobservability_test

import (
	"os"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/validate"
	"shepherd/internal/wizard"
	_ "shepherd/internal/wizard/appobservability"
	"shepherd/internal/wizard/wizardtest"
)

func TestWizard(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "App Observability Wizard Suite")
}

var _ = Describe("AppObservabilityWizard golden files", func() {
	wiz, getErr := wizard.Default().Get("app-observability")
	Expect(getErr).NotTo(HaveOccurred())

	DescribeTable("rendered output matches golden file and passes Stage 1",
		func(fixtureName string, state map[string]any) {
			result, err := wiz.Commit(state, wizardtest.Destinations())
			Expect(err).NotTo(HaveOccurred())

			goldenPath := "testdata/" + fixtureName + ".golden.alloy"
			goldenBytes, readErr := os.ReadFile(goldenPath)
			// A missing golden must fail, never self-heal: writing current output
			// as the baseline would make the comparison prove nothing. Regenerate
			// deliberately by updating the committed file.
			Expect(readErr).NotTo(HaveOccurred(), "golden file %s is missing — it must be committed", goldenPath)
			Expect(result.Contents).To(Equal(string(goldenBytes)),
				"output does not match golden file %s", goldenPath)

			// Golden itself must pass Stage 1 syntax check.
			r := validate.Stage1(string(goldenBytes))
			Expect(r.Valid).To(BeTrue(), "golden file %s fails Stage 1: %v", goldenPath, r.Diagnostics)
		},
		Entry("metrics-only", "metrics-only", map[string]any{
			"scrape_url":        "http://myapp:9090/metrics",
			"job_name":          "myapp",
			"scrape_interval":   "60s",
			"logs_enabled":      false,
			"metrics_dest_name": "prom-prod",
			"cluster_pattern":   "prod-.*",
			"role":              "metrics",
		}),
		Entry("metrics-and-logs", "metrics-and-logs", map[string]any{
			"scrape_url":        "http://app:9090/metrics",
			"job_name":          "app",
			"scrape_interval":   "30s",
			"logs_enabled":      true,
			"log_path":          "/var/log/app/*.log",
			"log_format":        "json",
			"metrics_dest_name": "prom-prod",
			"logs_dest_name":    "loki-prod",
			"cluster_pattern":   "prod-.*",
			// role=singleton, not "metrics": this fixture's output genuinely
			// carries BOTH metrics and logs (scrape + loki blocks below), and
			// gate G6 (docs/gateway-tier-plan.md) now refuses that combination
			// for role=metrics — see wizard.Register's role check. singleton is
			// the policy row for pipelines that legitimately mix signal kinds
			// (internal/signals/policy.go); "metrics" here would have been the
			// exact silent role/signal mismatch the gate exists to catch.
			"role": "singleton",
		}),
		// log_format omitted: the schema's default, logfmt. This is the
		// shape that shipped as a bare `stage.logfmt {}` — accepted by
		// `alloy validate`, refused by a running Alloy ("logfmt mapping or
		// regex is required"). TestGoldensLoadInRealAlloy now loads it.
		Entry("logs-default-format", "logs-default-format", map[string]any{
			"scrape_url":        "http://app:9090/metrics",
			"job_name":          "app",
			"scrape_interval":   "30s",
			"logs_enabled":      true,
			"log_path":          "/var/log/app/*.log",
			"metrics_dest_name": "prom-prod",
			"logs_dest_name":    "loki-prod",
			"cluster_pattern":   "prod-.*",
			"role":              "singleton",
		}),
		// raw: nothing to parse, so no loki.process at all — the file source
		// forwards straight to the writer (it once rendered `stage.raw {}`,
		// a block no Alloy has).
		Entry("logs-raw", "logs-raw", map[string]any{
			"scrape_url":        "http://app:9090/metrics",
			"job_name":          "app",
			"scrape_interval":   "30s",
			"logs_enabled":      true,
			"log_path":          "/var/log/app/*.log",
			"log_format":        "raw",
			"metrics_dest_name": "prom-prod",
			"logs_dest_name":    "loki-prod",
			"cluster_pattern":   "prod-.*",
			"role":              "singleton",
		}),
		// Secret-mode destinations (#229): the metrics writer reads basic
		// auth, the logs writer OAuth2 client credentials, from Secrets on
		// the spoke — wizardtest.Destinations' prom-basic and loki-oauth.
		Entry("secret-auth", "secret-auth", map[string]any{
			"scrape_url":        "http://app:9090/metrics",
			"job_name":          "app",
			"scrape_interval":   "30s",
			"logs_enabled":      true,
			"log_path":          "/var/log/app/*.log",
			"log_format":        "json",
			"metrics_dest_name": "prom-basic",
			"logs_dest_name":    "loki-oauth",
			"cluster_pattern":   "prod-.*",
			"role":              "singleton",
		}),
		// An https URL with no port and a query: __address__ takes the
		// scheme's default port, the query becomes __param_* labels.
		// TestGoldensLoadInRealAlloy runs it, so a label the scrape manager
		// refuses at run time fails there.
		Entry("https-query", "https-query", map[string]any{
			"scrape_url":        "https://app.example.internal/stats/prometheus?format=prometheus&module=app",
			"job_name":          "app",
			"scrape_interval":   "30s",
			"logs_enabled":      false,
			"metrics_dest_name": "prom-prod",
			"cluster_pattern":   "prod-.*",
			"role":              "metrics",
		}),
		// A bare host:port — the only form the old renderer got right, and
		// what some stored states hold (e2e/k8s scrapes "localhost:12345") —
		// is read as http://host:port/metrics.
		Entry("bare-host-port", "bare-host-port", map[string]any{
			"scrape_url":        "localhost:12345",
			"job_name":          "self",
			"scrape_interval":   "10s",
			"logs_enabled":      false,
			"metrics_dest_name": "prom-prod",
			"cluster_pattern":   "prod-.*",
			"role":              "metrics",
		}),
		// The form's untouched path: logs on and no role picked. The wizard
		// picks "singleton" (it used to default to "metrics", which the role
		// check refused for a metrics+logs pipeline).
		Entry("logs-role-unset", "logs-role-unset", map[string]any{
			"scrape_url":        "http://app:9090/metrics",
			"job_name":          "app",
			"logs_enabled":      true,
			"log_path":          "/var/log/app/*.log",
			"metrics_dest_name": "prom-prod",
			"logs_dest_name":    "loki-prod",
		}),
	)

	Describe("scrape_url", func() {
		commit := func(scrapeURL string) (wizard.CommitResult, error) {
			return wiz.Commit(map[string]any{
				"scrape_url": scrapeURL, "metrics_dest_name": "prom-prod", "logs_enabled": false,
			}, wizardtest.Destinations())
		}

		// The walkthrough's failure: the whole URL in __address__ loads, then
		// the scrape manager refuses the target and nothing is scraped.
		DescribeTable("renders host:port, scheme, path and query as separate target labels",
			func(scrapeURL string, want []string) {
				result, err := commit(scrapeURL)
				Expect(err).NotTo(HaveOccurred())
				// Alignment padding aside: compare with whitespace collapsed.
				flat := strings.Join(strings.Fields(result.Contents), " ")
				for _, w := range want {
					Expect(flat).To(ContainSubstring(w))
				}
				Expect(result.Contents).NotTo(MatchRegexp(`"__address__"\s*=\s*"[a-z]+://`),
					"__address__ must be host:port, never a URL")
			},
			Entry("http with port and path", "http://myapp:9090/metrics",
				[]string{`"__address__" = "myapp:9090"`, `"__scheme__" = "http"`, `"__metrics_path__" = "/metrics"`}),
			Entry("https defaults to 443", "https://myapp/custom",
				[]string{`"myapp:443"`, `"__scheme__" = "https"`, `"/custom"`}),
			Entry("http defaults to 80 and /metrics", "http://myapp",
				[]string{`"myapp:80"`, `"/metrics"`}),
			Entry("bare host:port is http at /metrics", "myapp:9100",
				[]string{`"myapp:9100"`, `"http"`, `"/metrics"`}),
			Entry("IPv6 host", "http://[::1]:9090/metrics", []string{`"[::1]:9090"`}),
			Entry("query parameters become __param_ labels", "http://myapp:9090/probe?target=db&module=pg",
				[]string{`"__param_module" = "pg"`, `"__param_target" = "db"`}),
			Entry("surrounding whitespace is ignored", "  http://myapp:9090/metrics ", []string{`"myapp:9090"`}),
		)

		DescribeTable("refuses a URL that cannot be a scrape target, naming the field",
			func(scrapeURL, wantMsg string) {
				_, err := commit(scrapeURL)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("scrape_url"))
				Expect(err.Error()).To(ContainSubstring(wantMsg))
			},
			Entry("another scheme", "ftp://myapp/metrics", "scheme must be http or https"),
			Entry("no host", "http:///metrics", "has no host"),
			Entry("bad port", "http://myapp:99999/metrics", "port"),
			Entry("credentials", "http://user:pass@myapp:9090/metrics", "credentials"),
			Entry("fragment", "http://myapp:9090/metrics#x", "fragment"),
			Entry("a repeated query parameter", "http://myapp:9090/m?a=1&a=2", "given 2 times"),
			Entry("a query parameter that cannot be a label", "http://myapp:9090/m?a-b=1", "query parameter"),
			Entry("a space", "http://myapp:9090/my metrics", "spaces"),
			Entry("a quote", `http://myapp:9090/metrics" broken = "x`, "spaces"),
			Entry("a decoded control character", "http://myapp:9090/m%0a", "control character"),
		)
	})

	Describe("collector role", func() {
		base := func() map[string]any {
			return map[string]any{
				"scrape_url": "http://app:9090/metrics", "metrics_dest_name": "prom-prod",
				"logs_enabled": true, "log_path": "/var/log/app/*.log", "logs_dest_name": "loki-prod",
			}
		}

		It("left unset, is singleton when logs are collected", func() {
			result, err := wiz.Commit(base(), wizardtest.Destinations())
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Role).To(Equal("singleton"))
			Expect(result.Matchers).To(ContainElement(`role="singleton"`))
		})

		It("left unset, is metrics when only metrics are collected — and says so in the matchers", func() {
			state := base()
			state["logs_enabled"] = false
			result, err := wiz.Commit(state, wizardtest.Destinations())
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Role).To(Equal("metrics"))
			Expect(result.Matchers).To(ContainElement(`role="metrics"`))
		})

		It("refuses role=metrics with logs, naming the fix rather than the role check", func() {
			state := base()
			state["role"] = "metrics"
			_, err := wiz.Commit(state, wizardtest.Destinations())
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(`pick the "singleton" role, or turn off log collection`))
			Expect(err.Error()).NotTo(ContainSubstring("does not match its declared role"))
		})

		It("offers no static default, so the form leaves the choice to Role", func() {
			for _, step := range wiz.Schema().Steps {
				for _, f := range step.Fields {
					if f.Name == "role" {
						Expect(f.Default).To(BeNil())
						return
					}
				}
			}
			Fail("the role field disappeared — this spec would pass vacuously")
		})
	})

	// H3: a glob handed straight to loki.source.file's __path__ is stat'ed
	// as one literal file name, so nothing is tailed.
	It("expands the log glob with local.file_match and tails its targets", func() {
		result, err := wiz.Commit(map[string]any{
			"scrape_url": "http://app:9090/metrics", "metrics_dest_name": "prom-prod",
			"logs_enabled": true, "log_path": "/var/log/app/*.log", "logs_dest_name": "loki-prod",
		}, wizardtest.Destinations())
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Contents).To(ContainSubstring(`local.file_match "app_logs"`))
		Expect(result.Contents).To(ContainSubstring(`targets    = local.file_match.app_logs.targets`))
		Expect(result.Contents).NotTo(MatchRegexp(`(?s)loki\.source\.file "app_logs" \{[^}]*__path__`))
	})

	It("refuses a log_format outside the select's options rather than rendering it as a stage", func() {
		_, err := wiz.Commit(map[string]any{
			"scrape_url": "http://app:9090/metrics", "metrics_dest_name": "prom-prod",
			"logs_enabled": true, "log_path": "/var/log/app/*.log", "log_format": "yaml",
			"logs_dest_name": "loki-prod", "role": "singleton",
		}, wizardtest.Destinations())
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("log_format"))
	})

	It("offers exactly the log formats Commit accepts", func() {
		var options []string
		for _, step := range wiz.Schema().Steps {
			for _, f := range step.Fields {
				if f.Name == "log_format" {
					options = f.Options
				}
			}
		}
		Expect(options).NotTo(BeEmpty(), "the log_format field disappeared — this spec would pass vacuously")
		for _, format := range options {
			_, err := wiz.Commit(map[string]any{
				"scrape_url": "http://app:9090/metrics", "metrics_dest_name": "prom-prod",
				"logs_enabled": true, "log_path": "/var/log/app/*.log", "log_format": format,
				"logs_dest_name": "loki-prod", "role": "singleton",
			}, wizardtest.Destinations())
			Expect(err).NotTo(HaveOccurred(), "log_format option %q is offered but Commit refuses it", format)
		}
	})
})

// A select field that offers a choice no valid state can commit is a dead end
// dressed as an option. Review of this wizard found exactly that: "logs" was
// offered as a collector role while Commit unconditionally emits the
// prometheus.scrape/remote_write block, so role enforcement (gate G6) refused
// every commit at that role. The refusal was correct — the dropdown was not.
//
// This spec generalizes the fix rather than pinning the one bad value: for
// EVERY role the schema offers, some valid state must commit successfully.
// Adding a new role option that Commit cannot satisfy fails here.
//
// Red run, executed: putting "logs" back into the role field's Options fails
// this spec with `role option "logs" is offered by the schema but no valid
// state commits at that role`.
var _ = Describe("every offered role is satisfiable", func() {
	It("commits successfully for each role option the schema advertises", func() {
		wiz, err := wizard.Default().Get("app-observability")
		Expect(err).NotTo(HaveOccurred())

		var roleField *wizard.StepField
		for _, step := range wiz.Schema().Steps {
			for i := range step.Fields {
				if step.Fields[i].Name == "role" {
					roleField = &step.Fields[i]
				}
			}
		}
		Expect(roleField).NotTo(BeNil(), "the role field disappeared — this spec would pass vacuously")
		Expect(roleField.Options).NotTo(BeEmpty())

		// The two shapes this wizard can produce: metrics alone, and metrics
		// plus logs. A role is satisfiable if either one commits at it.
		shapes := []map[string]any{
			{
				"scrape_url": "http://myapp:9090/metrics", "job_name": "myapp",
				"scrape_interval": "60s", "logs_enabled": false,
				"metrics_dest_name": "prom-prod", "cluster_pattern": "prod-.*",
			},
			{
				"scrape_url": "http://myapp:9090/metrics", "job_name": "myapp",
				"scrape_interval": "60s", "logs_enabled": true,
				"log_path": "/var/log/app/*.log", "log_format": "json",
				"metrics_dest_name": "prom-prod", "logs_dest_name": "loki-prod",
				"cluster_pattern": "prod-.*",
			},
		}

		for _, role := range roleField.Options {
			satisfiable := false
			var lastErr error
			for _, shape := range shapes {
				state := map[string]any{"role": role}
				for k, v := range shape {
					state[k] = v
				}
				if _, commitErr := wiz.Commit(state, wizardtest.Destinations()); commitErr == nil {
					satisfiable = true
					break
				} else {
					lastErr = commitErr
				}
			}
			Expect(satisfiable).To(BeTrue(),
				"role option %q is offered by the schema but no valid state commits at that role — "+
					"the last refusal was: %v", role, lastErr)
		}
	})
})
