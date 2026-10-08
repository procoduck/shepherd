package database_test

import (
	"os"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/validate"
	"shepherd/internal/wizard"
	_ "shepherd/internal/wizard/database"
	"shepherd/internal/wizard/wizardtest"
)

func TestWizard(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Database Wizard Suite")
}

var _ = Describe("DatabaseWizard golden files", func() {
	wiz, getErr := wizard.Default().Get("database")
	Expect(getErr).NotTo(HaveOccurred())

	entries := make([]TableEntry, 0, len(goldenCases))
	for _, c := range goldenCases {
		entries = append(entries, Entry(c.name, c.name, c.state))
	}
	DescribeTable("rendered output matches golden file, passes Stage 1, and is checked to role=metrics",
		func(fixtureName string, state map[string]any) {
			result, err := wiz.Commit(state, wizardtest.Destinations())
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Role).To(Equal("metrics"))

			goldenPath := "testdata/" + fixtureName + ".golden.alloy"
			goldenBytes, readErr := os.ReadFile(goldenPath)
			Expect(readErr).NotTo(HaveOccurred(), "golden file %s is missing — it must be committed", goldenPath)
			Expect(result.Contents).To(Equal(string(goldenBytes)),
				"output does not match golden file %s", goldenPath)

			r := validate.Stage1(string(goldenBytes))
			Expect(r.Valid).To(BeTrue(), "golden file %s fails Stage 1: %v", goldenPath, r.Diagnostics)
		},
		entries,
	)

	base := func(extra map[string]any) map[string]any {
		s := map[string]any{
			"engine":            "postgres",
			"credential_source": "kubernetes_secret",
			"secret_namespace":  "databases",
			"secret_name":       "app-pg",
			"metrics_dest_name": "prom-prod",
		}
		for k, v := range extra {
			s[k] = v
		}
		return s
	}

	Describe("the Kubernetes Secret source (the default)", func() {
		It("is the default for a new state, and reads the DSN from the Secret, never the environment", func() {
			s := base(nil)
			delete(s, "credential_source")
			result, err := wiz.Commit(s, wizardtest.Destinations())
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Contents).To(ContainSubstring(`remote.kubernetes.secret "db_credentials" {`))
			Expect(result.Contents).To(ContainSubstring(
				`data_source_names = [remote.kubernetes.secret.db_credentials.data["dsn"]]`))
			Expect(result.Contents).NotTo(ContainSubstring("sys.env"))
			Expect(result.Contents).NotTo(ContainSubstring("convert.nonsensitive(remote.kubernetes.secret.db_credentials"))
		})

		It("tells the Review step which Secret, key and RBAC the collector needs", func() {
			result, err := wiz.Commit(base(nil), wizardtest.Destinations())
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Warnings).To(HaveLen(2))
			Expect(result.Warnings[0]).To(ContainSubstring(`key "dsn" of Secret databases/app-pg`))
			Expect(result.Warnings[0]).To(ContainSubstring("needs get on Secrets in databases"))
			Expect(result.Warnings[1]).To(ContainSubstring("must run in Kubernetes"))
		})

		It("warns that a redis Secret without the key connects without a password", func() {
			result, err := wiz.Commit(base(map[string]any{
				"engine": "redis", "redis_addr": "redis.example.com:6379",
			}), wizardtest.Destinations())
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Warnings).To(ContainElement(ContainSubstring("without a password")))
		})

		It("defaults the Secret key to password for redis", func() {
			result, err := wiz.Commit(base(map[string]any{
				"engine": "redis", "redis_addr": "redis.example.com:6379",
			}), wizardtest.Destinations())
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Contents).To(ContainSubstring(
				`redis_password = remote.kubernetes.secret.db_credentials.data["password"]`))
		})

		DescribeTable("refuses an invalid Secret reference",
			func(extra map[string]any, want string) {
				_, err := wiz.Commit(base(extra), wizardtest.Destinations())
				Expect(err).To(MatchError(ContainSubstring(want)))
			},
			Entry("missing namespace", map[string]any{"secret_namespace": ""}, "secret_namespace"),
			Entry("invalid namespace", map[string]any{"secret_namespace": "Bad_NS"}, "secret_namespace"),
			Entry("missing name", map[string]any{"secret_name": ""}, "secret_name"),
			Entry("name that injects syntax", map[string]any{"secret_name": `x" }`}, "secret_name"),
			Entry("invalid key", map[string]any{"secret_key": "a/b"}, "secret_key"),
			Entry("key that injects syntax", map[string]any{"secret_key": `dsn"]`}, "secret_key"),
		)

		It("requires a redis address, and refuses one carrying credentials", func() {
			_, err := wiz.Commit(base(map[string]any{"engine": "redis"}), wizardtest.Destinations())
			Expect(err).To(MatchError(ContainSubstring("redis_addr is required")))
			_, err = wiz.Commit(base(map[string]any{
				"engine": "redis", "redis_addr": "redis://:hunter2@redis.example.com:6379",
			}), wizardtest.Destinations())
			Expect(err).To(MatchError(ContainSubstring("must not carry credentials")))
			_, err = wiz.Commit(base(map[string]any{
				"engine": "redis", "redis_addr": "redis://redis.example.com:6379?Password=hunter2",
			}), wizardtest.Destinations())
			Expect(err).To(MatchError(ContainSubstring("must not carry credentials")))
			_, err = wiz.Commit(base(map[string]any{
				"engine": "redis", "redis_addr": "redis://redis.example.com:6379?db=1",
			}), wizardtest.Destinations())
			Expect(err).To(MatchError(ContainSubstring("query string")))
		})
	})

	Describe("the env source", func() {
		It("says the operator must set the variable", func() {
			result, err := wiz.Commit(map[string]any{
				"engine": "postgres", "credential_source": "env", "connection_env": "APP_PG_DSN",
				"metrics_dest_name": "prom-prod",
			}, wizardtest.Destinations())
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Warnings).To(ConsistOf(ContainSubstring("Shepherd does not set APP_PG_DSN")))
		})

		It("requires connection_env, and refuses a name that is not a variable name", func() {
			_, err := wiz.Commit(map[string]any{
				"engine": "postgres", "credential_source": "env", "metrics_dest_name": "prom-prod",
			}, wizardtest.Destinations())
			Expect(err).To(MatchError(ContainSubstring("connection_env")))
			_, err = wiz.Commit(map[string]any{
				"engine": "postgres", "credential_source": "env", "connection_env": `X")] }`,
				"metrics_dest_name": "prom-prod",
			}, wizardtest.Destinations())
			Expect(err).To(MatchError(ContainSubstring("not an environment variable name")))
		})

		// A state stored before credential_source existed was never checked.
		// Refusing its name now would refuse every destination save that
		// regenerates it, so it keeps rendering the bytes it always did.
		It("renders a legacy state with a non-POSIX name exactly as before", func() {
			golden, err := os.ReadFile("testdata/legacy-env-postgres.golden.alloy")
			Expect(err).NotTo(HaveOccurred())
			for _, name := range []string{"APP-PG-DSN", "app.pg.dsn"} {
				result, err := wiz.Commit(map[string]any{
					"engine":            "postgres",
					"connection_env":    name,
					"job_name":          "app-db",
					"scrape_interval":   "60s",
					"metrics_dest_name": "prom-prod",
					"cluster_pattern":   "prod-.*",
				}, wizardtest.Destinations())
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Contents).To(Equal(strings.Replace(string(golden), "APP_PG_DSN", name, 1)))
			}
		})

		It("still refuses a legacy name that would end the string literal", func() {
			for _, name := range []string{`X")] }`, `X\`, "X\nY"} {
				_, err := wiz.Commit(map[string]any{
					"engine": "postgres", "connection_env": name, "metrics_dest_name": "prom-prod",
				}, wizardtest.Destinations())
				Expect(err).To(MatchError(ContainSubstring("quote, backslash or control")), name)
			}
		})

		It("holds an explicitly chosen env source to a POSIX name", func() {
			_, err := wiz.Commit(map[string]any{
				"engine": "postgres", "credential_source": "env", "connection_env": "APP-PG-DSN",
				"metrics_dest_name": "prom-prod",
			}, wizardtest.Destinations())
			Expect(err).To(MatchError(ContainSubstring("not an environment variable name")))
		})
	})

	It("refuses credential_source none for an engine that needs a DSN", func() {
		_, err := wiz.Commit(map[string]any{
			"engine": "mysql", "credential_source": "none", "metrics_dest_name": "prom-prod",
		}, wizardtest.Destinations())
		Expect(err).To(MatchError(ContainSubstring("only for redis")))
	})

	It("refuses an unknown credential_source", func() {
		_, err := wiz.Commit(base(map[string]any{"credential_source": "vault"}), wizardtest.Destinations())
		Expect(err).To(MatchError(ContainSubstring("credential_source")))
	})

	It("requires metrics_dest_name", func() {
		s := base(nil)
		delete(s, "metrics_dest_name")
		_, err := wiz.Commit(s, wizardtest.Destinations())
		Expect(err).To(HaveOccurred())
	})

	It("refuses an unsupported engine", func() {
		_, err := wiz.Commit(base(map[string]any{"engine": "mongodb"}), wizardtest.Destinations())
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("engine"))
	})

	It("quotes job_name and scrape_interval so they cannot inject syntax", func() {
		result, err := wiz.Commit(base(map[string]any{"job_name": `a" }`}), wizardtest.Destinations())
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Contents).To(ContainSubstring(`job_name        = "a\" }"`))
	})
})
