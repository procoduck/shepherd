// Package database implements the "database" wizard: scrapes a
// Postgres/MySQL/Redis exporter and forwards the resulting metrics to a
// Prometheus destination. One of the five catalog wizards
// docs/gateway-tier-plan.md W8 asks for.
//
// This wizard always targets role=metrics (see Role): every exporter it can
// select (prometheus.exporter.postgres/mysql/redis) speaks only the
// prom.metrics wire type via its "targets" plumbing output, and
// prometheus.scrape/prometheus.remote_write are likewise metrics-only.
// wizard.Register still checks that claim against the actual generated
// output on every commit (internal/wizard/role.go).
//
// The connection string is never accepted as wizard state: only the NAME of
// an env var Shepherd (or the operator's own secret injection) resolves at
// serve time, the same "no secret crosses the wire" posture
// docs/gateway-tier-plan.md D6 documents for the beacon and
// appobservability's destination URLs already follow via sys.env(...).
package database

import (
	"fmt"
	"strings"

	"shepherd/internal/wizard"
)

// Kind identifies the database wizard.
const Kind = "database"

// role is the fixed collector role this wizard's output is always checked
// against.
const role = "metrics"

func init() {
	wizard.Register(&Wizard{})
}

// Wizard generates a database exporter metrics pipeline.
type Wizard struct{}

// Kind returns the wizard kind identifier.
func (w *Wizard) Kind() string { return Kind }

// Role always returns "metrics": every engine this wizard can select emits
// only prom.metrics-wire components, regardless of state.
func (w *Wizard) Role(map[string]any) string { return role }

// engines maps the "engine" step field's allowed values to the schema
// component that implements it. Checked against the pinned schema artifact
// by schema_conformance_test.go, not assumed.
var engines = map[string]string{
	"postgres": "prometheus.exporter.postgres",
	"mysql":    "prometheus.exporter.mysql",
	"redis":    "prometheus.exporter.redis",
}

// Schema returns the wizard's input schema.
func (w *Wizard) Schema() wizard.Schema {
	return wizard.Schema{
		Kind:        Kind,
		Title:       "Database Metrics",
		Description: "Collect metrics from a PostgreSQL, MySQL, Redis or MongoDB instance.",
		Steps: []wizard.Step{
			{
				ID:    "engine",
				Title: "Database engine",
				Fields: []wizard.StepField{
					{
						Name: "engine", Label: "Engine", Type: "select", Required: true,
						Options: []string{"postgres", "mysql", "redis"},
					},
					{
						Name: "connection_env", Label: "Connection env var name", Type: "text", Required: true,
						Placeholder: "MYAPP_DB_DSN",
						Description: "Name of an env var Shepherd injects at serve time holding the connection " +
							"string/address. The wizard never sees the credential itself.",
					},
					{Name: "job_name", Label: "Job label", Type: "text", Default: "database"},
					{Name: "scrape_interval", Label: "Scrape interval", Type: "text", Default: "60s"},
				},
			},
			{
				ID:    "destinations",
				Title: "Destinations",
				Fields: []wizard.StepField{
					{
						Name: "metrics_dest_name", Label: "Metrics destination", Type: "text", Required: true,
						Description: "Name of a Prometheus-type destination in this org.",
					},
				},
			},
			{
				ID:    "matchers",
				Title: "Collector matching",
				Fields: []wizard.StepField{
					{
						Name: "cluster_pattern", Label: "Cluster pattern (regex)", Type: "text",
						Placeholder: "prod-.*", Description: "Applies this pipeline to clusters matching the regex.",
					},
				},
			},
		},
	}
}

// Commit generates an Alloy pipeline from the wizard state.
func (w *Wizard) Commit(state map[string]any) (wizard.CommitResult, error) {
	get := func(key string) string {
		v, _ := state[key].(string) //nolint:errcheck // type assert ok flag; empty string is safe default
		return v
	}

	engine := get("engine")
	if _, ok := engines[engine]; !ok {
		return wizard.CommitResult{}, fmt.Errorf(
			"engine %q is not supported, want one of: postgres|mysql|redis", engine)
	}
	connEnv := get("connection_env")
	if connEnv == "" {
		return wizard.CommitResult{}, fmt.Errorf("connection_env is required")
	}
	metricsDest := get("metrics_dest_name")
	if metricsDest == "" {
		return wizard.CommitResult{}, fmt.Errorf("metrics_dest_name is required")
	}
	jobName := get("job_name")
	if jobName == "" {
		jobName = "database"
	}
	scrapeInterval := get("scrape_interval")
	if scrapeInterval == "" {
		scrapeInterval = "60s"
	}

	var sb strings.Builder

	// Each engine's exporter takes the connection value under a different
	// attribute name and shape (postgres wants a list, mysql/redis want a
	// single string) — see wizard.go's package doc for why the value itself
	// is always sys.env(...), never a literal.
	switch engine {
	case "postgres":
		_, _ = fmt.Fprintf(&sb, `prometheus.exporter.postgres "db" {
  data_source_names = [sys.env("%s")]
}
`, connEnv)
	case "mysql":
		_, _ = fmt.Fprintf(&sb, `prometheus.exporter.mysql "db" {
  data_source_name = sys.env("%s")
}
`, connEnv)
	case "redis":
		_, _ = fmt.Fprintf(&sb, `prometheus.exporter.redis "db" {
  redis_addr = sys.env("%s")
}
`, connEnv)
	}

	_, _ = fmt.Fprintf(&sb, `
prometheus.scrape "database" {
  targets         = prometheus.exporter.%s.db.targets
  forward_to      = [prometheus.remote_write.metrics.receiver]
  scrape_interval = "%s"
  job_name        = "%s"
}
`, engine, scrapeInterval, jobName)

	_, _ = fmt.Fprintf(&sb, `
prometheus.remote_write "metrics" {
  endpoint {
    name = "%s"
    url  = sys.env("SHEPHERD_DEST_%s_URL")
    // auth injected by Shepherd at serve time
  }
}
`, metricsDest, strings.ToUpper(strings.ReplaceAll(metricsDest, "-", "_")))

	var matchers []string
	if cp := get("cluster_pattern"); cp != "" {
		matchers = append(matchers, fmt.Sprintf(`cluster=~%q`, cp))
	}
	matchers = append(matchers, fmt.Sprintf(`role=%q`, role))

	return wizard.CommitResult{
		Contents: sb.String(),
		Matchers: matchers,
	}, nil
}
