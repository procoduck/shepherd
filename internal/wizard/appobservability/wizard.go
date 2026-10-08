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

// logFormats is the log_format select's option set. Anything else is refused
// rather than rendered: this wizard once interpolated the value straight into
// `stage.<format> {}`, so "raw" became a stage.raw block no Alloy has.
var logFormats = map[string]bool{
	"logfmt":            true,
	"json":              true,
	wizard.LogFormatRaw: true,
}

// Wizard generates a full-stack observability pipeline.
type Wizard struct{}

// Kind returns the wizard kind identifier.
func (w *Wizard) Kind() string { return Kind }

// Role returns the collector role this wizard's committed pipeline targets.
// Unlike the fixed-role wizards in the catalog, this wizard exposes a
// "role" step field the operator chooses directly (matchers.Title above),
// so Role reads that same field — with the same default Commit falls back
// to when the operator (or a stale client) omits it — rather than a
// constant. Reading exactly what Commit reads is what keeps this and
// Commit's actual output from disagreeing: wizard.Register wraps this
// wizard's Commit and checks its result against whatever Role reports, so a
// mismatch between the two would either wrongly refuse a valid pipeline or
// wrongly wave through an invalid one.
//
// The field has no static default. It used to default to "metrics" while the
// logs step defaulted to on, so the form's untouched path produced a
// metrics+logs pipeline declared for a metrics-only role — refused by the
// role check, and visible only as a failed preview on the Review step
// (2026-10-08 walkthrough). Left unset, the role follows what the pipeline
// carries: "singleton" (the one unrestricted row in internal/signals.Policies)
// when logs are collected, "metrics" otherwise.
func (w *Wizard) Role(state map[string]any) string {
	role, _ := state["role"].(string) //nolint:errcheck // type assert ok flag; empty string falls through to the default below
	if role != "" {
		return role
	}
	if collectsLogs(state) {
		return "singleton"
	}
	return "metrics"
}

// collectsLogs reports whether Commit renders the log-collection blocks for
// state: logs on (the toggle defaults to on), a Loki destination named and a
// path given. Role and Commit both read it, so the two cannot disagree.
func collectsLogs(state map[string]any) bool {
	enabled := true
	if v, ok := state["logs_enabled"].(bool); ok {
		enabled = v
	}
	dest, _ := state["logs_dest_name"].(string) //nolint:errcheck // type assert ok flag; empty string means no destination
	path, _ := state["log_path"].(string)       //nolint:errcheck // type assert ok flag; empty string means no path
	return enabled && dest != "" && path != ""
}

// Schema returns the wizard's input schema.
func (w *Wizard) Schema() wizard.Schema {
	return wizard.Schema{
		Kind:        Kind,
		Title:       "App Observability",
		Description: "Scrape metrics and logs from one application and ship them to your org's destinations.",
		Steps: []wizard.Step{
			{
				ID:    "targets",
				Title: "Scrape targets",
				Fields: []wizard.StepField{
					{
						Name: "scrape_url", Label: "Metrics endpoint URL", Type: "text", Required: true,
						Placeholder: "http://myapp:9090/metrics",
						Description: "Full http(s) URL of your app's Prometheus metrics endpoint, e.g. " +
							"http://myapp:9090/metrics. Query parameters are sent with each scrape. " +
							"A bare host:port is scraped over http at /metrics.",
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
						Placeholder: "/var/log/my-app/*.log",
						Description: "Glob pattern for the log files, e.g. /var/log/my-app/*.log. Every " +
							"matching file is tailed, including ones created later.",
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
						// "logs" is deliberately NOT offered. Commit always
						// emits the prometheus.scrape/remote_write block —
						// metrics are this wizard's reason to exist — and a
						// role=logs collector may carry logs and nothing else
						// (internal/signals.Policies), so every commit at that
						// role would be refused by role enforcement. Offering a
						// choice that can never succeed is a dead end dressed
						// as an option; the refusal was correct but the
						// dropdown should not have led anyone there.
						// The Ginkgo spec "every offered role is satisfiable"
						// (wizard_test.go) pins this.
						//
						// No Default: left unset, the wizard picks the role
						// from what the pipeline carries (see Role).
						Options: []string{"metrics", "singleton"},
						Description: "Leave unset to let the wizard choose: \"singleton\" when logs are " +
							"collected, \"metrics\" otherwise. A role=metrics collector may only carry " +
							"metrics, so a metrics+logs pipeline belongs on a singleton collector.",
					},
				},
			},
		},
	}
}

// Commit generates an Alloy pipeline from the wizard state.
func (w *Wizard) Commit(state map[string]any, dests wizard.Destinations) (wizard.CommitResult, error) {
	get := func(key string) string {
		v, _ := state[key].(string) //nolint:errcheck // type assert ok flag; empty string is safe default
		return v
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
	logPath := get("log_path")
	logFormat := get("log_format")
	if logFormat == "" {
		logFormat = "logfmt"
	}

	target, err := parseScrapeURL(scrapeURL)
	if err != nil {
		return wizard.CommitResult{}, err
	}
	logsRender := collectsLogs(state)
	if role := get("role"); role == "metrics" && logsRender {
		// Caught here, by name, rather than left to wizard.Register's role
		// check: that refusal ("generated pipeline does not match its
		// declared role") is correct but tells the operator nothing about
		// which input to change.
		return wizard.CommitResult{}, fmt.Errorf(
			"role %q collectors carry metrics only, but this pipeline also collects logs from %q — "+
				"pick the \"singleton\" role, or turn off log collection", role, logPath)
	}

	var sb strings.Builder

	// Prometheus scrape → remote write. The URL is split into the target
	// labels Prometheus builds a scrape URL from: __address__ is host:port
	// only — a full URL there is refused at run time ("… is not a valid
	// hostname") while the config loads and the collector reports APPLIED.
	_, _ = fmt.Fprintf(&sb, `prometheus.scrape "app" {
  targets = [{%s}]
  forward_to = [prometheus.remote_write.metrics.receiver]
  scrape_interval = "%s"
  job_name = "%s"
}
`, target.labels(), scrapeInterval, jobName)

	metricsWriter, err := wizard.RenderWriter(wizard.WriterPrometheus, "metrics", dests, metricsDest)
	if err != nil {
		return wizard.CommitResult{}, fmt.Errorf("metrics_dest_name: %w", err)
	}
	_, _ = sb.WriteString(metricsWriter)

	// Optional log collection.
	if logsRender {
		if !logFormats[logFormat] {
			return wizard.CommitResult{}, fmt.Errorf(
				"log_format %q is not supported, want one of: logfmt|json|raw", logFormat)
		}
		stages, err := wizard.LogParseStages(logFormat)
		if err != nil {
			return wizard.CommitResult{}, fmt.Errorf("log_format: %w", err)
		}
		// The file source feeds the parser when there is something to parse,
		// the writer directly when there is not ("raw"). This wizard once
		// always forwarded the source straight to the writer, so the
		// loki.process block it emitted alongside never received a line.
		sourceTo := "loki.write.logs.receiver"
		if stages != "" {
			sourceTo = "loki.process.app_process.receiver"
		}
		// log_path is a glob: local.file_match expands it, loki.source.file
		// tails what it matched (wizard.RenderFileSource's doc).
		_, _ = sb.WriteString(wizard.RenderFileSource("app_logs", logPath, jobName, sourceTo) + "\n")
		if stages != "" {
			_, _ = fmt.Fprintf(&sb, `loki.process "app_process" {
  forward_to = [loki.write.logs.receiver]

%s}

`, stages)
		}
		logsWriter, err := wizard.RenderWriter(wizard.WriterLoki, "logs", dests, logsDest)
		if err != nil {
			return wizard.CommitResult{}, fmt.Errorf("logs_dest_name: %w", err)
		}
		_, _ = sb.WriteString(logsWriter)
	}

	// Build matchers.
	var matchers []string
	if cp := get("cluster_pattern"); cp != "" {
		matchers = append(matchers, fmt.Sprintf(`cluster=~%q`, cp))
	}
	// Always the role the output was checked against (Role), so a pipeline
	// is only served to the collectors it was proven fit for — an unset role
	// field used to leave the matcher out entirely.
	matchers = append(matchers, fmt.Sprintf(`role=%q`, w.Role(state)))

	return wizard.CommitResult{
		Contents: sb.String(),
		Matchers: matchers,
	}, nil
}
