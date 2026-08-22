// Package selfmonitoring implements the "self-monitoring" wizard: scrapes
// Alloy's own /metrics via prometheus.exporter.self and, optionally, tails
// Alloy's own log output — the one wizard in the catalog
// (docs/gateway-tier-plan.md W8) that DELIBERATELY mixes signal kinds by
// design, not by accident.
//
// This is exactly the scenario internal/signals/policy.go's "singleton" row
// documents by name: "a self-monitoring pipeline that scrapes Alloy's own
// /metrics AND tails its own log output. Restricting it would make it
// useless for the role it plays." Every other catalog wizard (cluster-metrics,
// pod-logs, database, blackbox) is single-signal and role=metrics or
// role=logs would already refuse a mismatch on its own; this wizard is the
// one that needs role="singleton" (Unrestricted) specifically BECAUSE its
// output can legitimately carry both Metrics and Logs at once — which is
// also what makes it the sharpest available demonstration that
// wizard.Register's role check (internal/wizard/role.go) is not a formality:
// declaring this wizard "metrics" instead of "singleton" is refused by the
// same mechanism every other wizard is checked by (see selfmonitoring_test.go).
package selfmonitoring

import (
	"fmt"
	"strings"

	"shepherd/internal/wizard"
)

// Kind identifies the self-monitoring wizard.
const Kind = "self-monitoring"

// role is the fixed collector role this wizard's output is always checked
// against. Must be "singleton" (internal/signals.Policies' one Unrestricted
// row) because Commit can legitimately emit both Metrics and Logs — see the
// package doc.
const role = "singleton"

func init() {
	wizard.Register(&Wizard{})
}

// Wizard generates Alloy's own self-monitoring pipeline.
type Wizard struct{}

// Kind returns the wizard kind identifier.
func (w *Wizard) Kind() string { return Kind }

// Role always returns "singleton": see the package doc for why this
// wizard's mixed-signal output specifically needs the one Unrestricted
// policy row rather than a metrics- or logs-only one.
func (w *Wizard) Role(map[string]any) string { return role }

// Schema returns the wizard's input schema.
func (w *Wizard) Schema() wizard.Schema {
	return wizard.Schema{
		Kind:  Kind,
		Title: "Self Monitoring",
		Steps: []wizard.Step{
			{
				ID:    "metrics",
				Title: "Metrics",
				Fields: []wizard.StepField{
					{Name: "job_name", Label: "Job label", Type: "text", Default: "alloy-self"},
					{Name: "scrape_interval", Label: "Scrape interval", Type: "text", Default: "60s"},
					{
						Name: "metrics_dest_name", Label: "Metrics destination", Type: "text", Required: true,
						Description: "Name of a Prometheus-type destination in this org.",
					},
				},
			},
			{
				ID:    "logs",
				Title: "Log collection",
				Fields: []wizard.StepField{
					{Name: "logs_enabled", Label: "Also tail Alloy's own log output", Type: "toggle", Default: true},
					{
						Name: "log_path", Label: "Alloy log file path", Type: "text",
						Placeholder: "/var/log/alloy/*.log", Description: "Glob pattern for Alloy's own log file(s).",
					},
					{
						Name: "logs_dest_name", Label: "Logs destination (Loki)", Type: "text",
						Description: "Name of a Loki-type destination. Leave blank to skip log forwarding.",
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
	getBool := func(key string, def bool) bool {
		if v, ok := state[key].(bool); ok {
			return v
		}
		return def
	}

	metricsDest := get("metrics_dest_name")
	if metricsDest == "" {
		return wizard.CommitResult{}, fmt.Errorf("metrics_dest_name is required")
	}
	jobName := get("job_name")
	if jobName == "" {
		jobName = "alloy-self"
	}
	scrapeInterval := get("scrape_interval")
	if scrapeInterval == "" {
		scrapeInterval = "60s"
	}

	logsDest := get("logs_dest_name")
	logPath := get("log_path")
	logsEnabled := getBool("logs_enabled", true) && logsDest != "" && logPath != ""

	var sb strings.Builder

	_, _ = sb.WriteString(`prometheus.exporter.self "alloy" {}
`)

	_, _ = fmt.Fprintf(&sb, `
prometheus.scrape "self" {
  targets         = prometheus.exporter.self.alloy.targets
  forward_to      = [prometheus.remote_write.metrics.receiver]
  scrape_interval = "%s"
  job_name        = "%s"
}
`, scrapeInterval, jobName)

	_, _ = fmt.Fprintf(&sb, `
prometheus.remote_write "metrics" {
  endpoint {
    name = "%s"
    url  = sys.env("SHEPHERD_DEST_%s_URL")
    // auth injected by Shepherd at serve time
  }
}
`, metricsDest, strings.ToUpper(strings.ReplaceAll(metricsDest, "-", "_")))

	// This block is the whole reason role="singleton" instead of "metrics":
	// once it renders, the pipeline provably carries Logs alongside Metrics
	// (internal/signals.Derive sees loki.source.file/loki.write's loki.logs
	// wire type), and wizard.Register's role check would refuse this exact
	// output under any restricted role — see this package's doc comment.
	if logsEnabled {
		_, _ = fmt.Fprintf(&sb, `
loki.source.file "self" {
  targets = [
    {__path__ = "%s", job = "%s"},
  ]
  forward_to = [loki.write.logs.receiver]
}
`, logPath, jobName)

		_, _ = fmt.Fprintf(&sb, `
loki.write "logs" {
  endpoint {
    name = "%s"
    url  = sys.env("SHEPHERD_DEST_%s_URL")
    // auth injected by Shepherd at serve time
  }
}
`, logsDest, strings.ToUpper(strings.ReplaceAll(logsDest, "-", "_")))
	}

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
