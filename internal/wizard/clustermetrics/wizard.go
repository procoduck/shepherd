// Package clustermetrics implements the "cluster-metrics" wizard: a
// cAdvisor-backed pipeline giving per-container CPU/memory/network metrics
// for every node Alloy runs on. It is one of the five catalog wizards
// docs/gateway-tier-plan.md W8 asks for.
//
// This wizard always targets role=metrics (Kind returns a fixed role
// regardless of state — see Role): nothing it generates ever carries logs,
// traces, or profiles, so there is no operator-facing "role" field to get
// wrong. wizard.Register still checks that claim against the actual
// generated output on every commit (internal/wizard/role.go) — the fixed
// role is a design choice this wizard makes, not a trust wizard.Register
// extends it.
package clustermetrics

import (
	"fmt"
	"strings"

	"shepherd/internal/wizard"
)

// Kind identifies the cluster-metrics wizard.
const Kind = "cluster-metrics"

// role is the fixed collector role this wizard's output is always checked
// against. A package-level const rather than a literal repeated in Role and
// doc comments, so there is exactly one place to change it.
const role = "metrics"

func init() {
	wizard.Register(&Wizard{})
}

// Wizard generates a cluster-wide container metrics pipeline.
type Wizard struct{}

// Kind returns the wizard kind identifier.
func (w *Wizard) Kind() string { return Kind }

// Role always returns "metrics": this wizard's template only ever emits
// prometheus.exporter.cadvisor -> prometheus.scrape -> prometheus.remote_write,
// all prom.metrics wire type, regardless of state.
func (w *Wizard) Role(map[string]any) string { return role }

// Schema returns the wizard's input schema.
func (w *Wizard) Schema() wizard.Schema {
	return wizard.Schema{
		Kind:        Kind,
		Title:       "Cluster Metrics",
		Description: "Collect Kubernetes cluster and node metrics — kubelet, cAdvisor and kube-state-metrics.",
		Steps: []wizard.Step{
			{
				ID:    "scrape",
				Title: "Collection",
				Fields: []wizard.StepField{
					{Name: "job_name", Label: "Job label", Type: "text", Default: "cluster-metrics"},
					{Name: "scrape_interval", Label: "Scrape interval", Type: "text", Default: "60s"},
					{
						Name: "docker_only", Label: "Docker containers only", Type: "toggle", Default: false,
						Description: "Report only Docker-managed containers instead of every cgroup cAdvisor sees.",
					},
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
	getBool := func(key string) bool {
		v, _ := state[key].(bool) //nolint:errcheck // type assert ok flag; false is safe default
		return v
	}

	metricsDest := get("metrics_dest_name")
	if metricsDest == "" {
		return wizard.CommitResult{}, fmt.Errorf("metrics_dest_name is required")
	}
	jobName := get("job_name")
	if jobName == "" {
		jobName = "cluster-metrics"
	}
	scrapeInterval := get("scrape_interval")
	if scrapeInterval == "" {
		scrapeInterval = "60s"
	}
	dockerOnly := getBool("docker_only")

	var sb strings.Builder

	_, _ = fmt.Fprintf(&sb, `prometheus.exporter.cadvisor "cluster" {
  docker_only = %t
}
`, dockerOnly)

	_, _ = fmt.Fprintf(&sb, `
prometheus.scrape "cluster_metrics" {
  targets          = prometheus.exporter.cadvisor.cluster.targets
  forward_to       = [prometheus.remote_write.metrics.receiver]
  scrape_interval  = "%s"
  job_name         = "%s"
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
