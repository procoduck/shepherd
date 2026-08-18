// Package appobservability implements the "app-observability" wizard.
// It generates a pipeline that scrapes application metrics, collects structured
// logs, and optionally collects traces, forwarding to user-selected destinations.
package appobservability

import (
	"fmt"
	"strings"

	"shepherd/internal/wizard"
)

// Kind identifies the app-observability wizard.
const Kind = "app-observability"

func init() {
	wizard.Register(&Wizard{})
}

// Wizard generates a full-stack observability pipeline.
type Wizard struct{}

// Kind returns the wizard kind identifier.
func (w *Wizard) Kind() string { return Kind }

// Schema returns the wizard's input schema.
func (w *Wizard) Schema() wizard.Schema {
	return wizard.Schema{
		Kind:  Kind,
		Title: "App Observability",
		Steps: []wizard.Step{
			{
				ID:    "targets",
				Title: "Scrape targets",
				Fields: []wizard.StepField{
					{
						Name: "scrape_url", Label: "Metrics endpoint URL", Type: "text", Required: true,
						Placeholder: "http://myapp:9090/metrics", Description: "Prometheus /metrics URL exposed by your app.",
					},
					{Name: "scrape_interval", Label: "Scrape interval", Type: "text", Default: "60s"},
					{Name: "job_name", Label: "Job label", Type: "text", Required: true, Placeholder: "my-app"},
				},
			},
			{
				ID:    "logs",
				Title: "Log collection",
				Fields: []wizard.StepField{
					{Name: "logs_enabled", Label: "Collect logs", Type: "toggle", Default: true},
					{
						Name: "log_path", Label: "Log file path(s)", Type: "text",
						Placeholder: "/var/log/my-app/*.log", Description: "Glob pattern for log files.",
					},
					{Name: "log_format", Label: "Log format", Type: "select", Options: []string{"logfmt", "json", "raw"}, Default: "logfmt"},
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
					{
						Name: "role", Label: "Collector role", Type: "select",
						Options: []string{"metrics", "logs", "singleton"}, Default: "metrics",
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

	scrapeURL := get("scrape_url")
	if scrapeURL == "" {
		return wizard.CommitResult{}, fmt.Errorf("scrape_url is required")
	}
	jobName := get("job_name")
	if jobName == "" {
		jobName = "app"
	}
	scrapeInterval := get("scrape_interval")
	if scrapeInterval == "" {
		scrapeInterval = "60s"
	}
	metricsDest := get("metrics_dest_name")
	if metricsDest == "" {
		return wizard.CommitResult{}, fmt.Errorf("metrics_dest_name is required")
	}
	logsDest := get("logs_dest_name")
	logsEnabled := getBool("logs_enabled", true) && logsDest != ""
	logPath := get("log_path")
	logFormat := get("log_format")
	if logFormat == "" {
		logFormat = "logfmt"
	}

	var sb strings.Builder

	// Prometheus scrape → remote write.
	_, _ = fmt.Fprintf(&sb, `prometheus.scrape "app" {
  targets = [{"__address__" = "%s"}]
  forward_to = [prometheus.remote_write.metrics.receiver]
  scrape_interval = "%s"
  job_name = "%s"
}
	`, scrapeURL, scrapeInterval, jobName)

	_, _ = fmt.Fprintf(&sb, `prometheus.remote_write "metrics" {
  endpoint {
    name = "%s"
    url  = env("SHEPHERD_DEST_%s_URL")
    // auth injected by Shepherd at serve time
  }
}
	`, metricsDest, strings.ToUpper(strings.ReplaceAll(metricsDest, "-", "_")))

	// Optional log collection.
	if logsEnabled && logPath != "" {
		_, _ = fmt.Fprintf(&sb, `loki.source.file "app_logs" {
  targets = [
    {__path__ = "%s", job = "%s"},
  ]
  forward_to = [loki.write.logs.receiver]
}

loki.process "app_process" {
  forward_to = [loki.write.logs.receiver]
  stage.%s {}
}

loki.write "logs" {
  endpoint {
    name = "%s"
    url  = env("SHEPHERD_DEST_%s_URL")
  }
}
	`, logPath, jobName, logFormat, logsDest, strings.ToUpper(strings.ReplaceAll(logsDest, "-", "_")))
	}

	// Build matchers.
	var matchers []string
	if cp := get("cluster_pattern"); cp != "" {
		matchers = append(matchers, fmt.Sprintf(`cluster=~%q`, cp))
	}
	if role := get("role"); role != "" {
		matchers = append(matchers, fmt.Sprintf(`role=%q`, role))
	}

	return wizard.CommitResult{
		Contents: sb.String(),
		Matchers: matchers,
	}, nil
}
