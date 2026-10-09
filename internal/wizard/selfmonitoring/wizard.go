// Package selfmonitoring implements the "self-monitoring" wizard: scrapes
// Alloy's own /metrics via prometheus.exporter.self and, optionally, tails
// Alloy's own log output — the one wizard in the catalog
// (docs/gateway-tier-plan.md W8) that DELIBERATELY mixes signal kinds by
// design, not by accident.
//
// This is exactly the scenario internal/signals/policy.go's "singleton" row
// documents by name: "a self-monitoring pipeline that scrapes Alloy's own
// /metrics AND tails its own log output. Restricting it would make it
// useless for the role it plays." With log collection on, this wizard's
// output carries both Metrics and Logs, so it needs role="singleton"
// (Unrestricted) — declaring "metrics" for that output is refused by the
// same mechanism every other wizard is checked by (internal/wizard/role.go;
// see wizard_test.go).
//
// With log collection off the output is metrics only, and forcing
// "singleton" then (as this wizard did until the 2026-10-09 walkthrough, B6)
// kept it off every fleet split into metrics and logs collectors. The role
// is therefore an optional field, exactly as App Observability's (#289):
// left on Auto it follows what the pipeline carries — "singleton" when logs
// are collected, "metrics" otherwise — an explicit choice is checked against
// the signals, and the role matcher is always emitted.
package selfmonitoring

import (
	"fmt"
	"slices"
	"strings"

	"shepherd/internal/wizard"
)

// Kind identifies the self-monitoring wizard.
const Kind = "self-monitoring"

// roleOptions is the role field's option set. "logs" is not offered: Commit
// always emits the metrics scrape, and a role=logs collector may carry logs
// and nothing else (internal/signals.Policies), so every commit at that role
// would be refused — an option that can never succeed.
var roleOptions = []string{"metrics", "singleton"}

func init() {
	wizard.Register(&Wizard{})
}

// Wizard generates Alloy's own self-monitoring pipeline.
type Wizard struct{}

// Kind returns the wizard kind identifier.
func (w *Wizard) Kind() string { return Kind }

// Role returns the collector role this wizard's committed pipeline targets:
// the "role" field when the operator chose one, otherwise "singleton" when
// the pipeline collects logs and "metrics" when it does not. Commit reads the
// same function for its role matcher, and wizard.Register checks Commit's
// output against it, so the three cannot disagree.
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
// state: logs on (the toggle defaults to on) and a Loki destination named. A
// blank log_path does not turn them off — Commit falls back to the schema's
// default path. Role and Commit both read it, so the two cannot disagree.
func collectsLogs(state map[string]any) bool {
	enabled := true
	if v, ok := state["logs_enabled"].(bool); ok {
		enabled = v
	}
	dest, _ := state["logs_dest_name"].(string) //nolint:errcheck // type assert ok flag; empty string means no destination
	return enabled && dest != ""
}

// Schema returns the wizard's input schema.
func (w *Wizard) Schema() wizard.Schema {
	return wizard.Schema{
		Kind:        Kind,
		Title:       "Self Monitoring",
		Description: "Monitor the collector itself — component health, resource use and pipeline throughput.",
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
						Default: "/var/log/alloy/*.log", Placeholder: "/var/log/alloy/*.log",
						Description: "Glob pattern for Alloy's own log file(s), e.g. /var/log/alloy/*.log. " +
							"Every matching file is tailed, including ones created later.",
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
						Placeholder: "prod-.*",
						Description: "Applies this pipeline to clusters matching the regex.",
					},
					{
						Name: "role", Label: "Collector role", Type: "select",
						// No Default: left unset, the wizard picks the role from
						// what the pipeline carries (see Role).
						Options: roleOptions,
						Description: "Leave on Auto to let the wizard choose: \"singleton\" when Alloy's own logs " +
							"are collected, \"metrics\" otherwise. Collecting metrics and logs together needs a " +
							"singleton collector — a role=metrics collector may only carry metrics. If your fleet " +
							"has only metrics and logs collectors, turn log collection off to use role \"metrics\".",
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
	logPathProvided := logPath != ""
	// The runner UI only seeds a field's Default into wizard state
	// (WizardRunnerPage.tsx), never its Placeholder, so a client that
	// leaves log_path untouched sends no log_path key at all — get("log_path")
	// then returns "". Falling back to the schema's own default here (rather
	// than requiring logPath != "" to enable the block) is what keeps a
	// blank path from silently dropping log collection for an operator who
	// toggled it on and named a Loki destination.
	if logPath == "" {
		logPath = "/var/log/alloy/*.log"
	}
	logsRequested := getBool("logs_enabled", true)
	logsEnabled := collectsLogs(state)

	// Caught here, by name, rather than left to wizard.Register's role
	// check: that refusal ("generated pipeline does not match its declared
	// role") is correct but tells the operator nothing about which input to
	// change.
	switch role := get("role"); {
	case role == "":
	case !slices.Contains(roleOptions, role):
		return wizard.CommitResult{}, fmt.Errorf("role %q is not offered by this wizard, want one of: %s",
			role, strings.Join(roleOptions, "|"))
	case role == "metrics" && logsEnabled:
		return wizard.CommitResult{}, fmt.Errorf(
			"role %q collectors carry metrics only, but this pipeline also tails Alloy's own logs from %q — "+
				"pick the \"singleton\" role, or turn off log collection", role, logPath)
	}

	// Warnings surface the non-obvious decisions this wizard just made, so a
	// preview does not hide them (B2). Two cases matter: log collection asked
	// for but dropped because no destination was named, and a log path that
	// was defaulted rather than typed.
	var warnings []string
	if logsRequested && logsDest == "" {
		warnings = append(warnings, "Log collection was requested but no logs destination was set, "+
			"so it was left out — name a logs destination to include it.")
	}
	if logsEnabled && !logPathProvided {
		warnings = append(warnings, fmt.Sprintf("No log path was given, so the default %q was used.", logPath))
	}

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

	writer, err := wizard.RenderWriter(wizard.WriterPrometheus, "metrics", dests, metricsDest)
	if err != nil {
		return wizard.CommitResult{}, fmt.Errorf("metrics_dest_name: %w", err)
	}
	_, _ = sb.WriteString("\n" + writer)

	// This block is why Role picks "singleton" when logs are collected: once
	// it renders, the pipeline provably carries Logs alongside Metrics
	// (internal/signals.Derive sees loki.source.file/loki.write's loki.logs
	// wire type), and wizard.Register's role check would refuse this exact
	// output under any restricted role — see this package's doc comment.
	if logsEnabled {
		// log_path is a glob: local.file_match expands it, loki.source.file
		// tails what it matched (wizard.RenderFileSource's doc).
		_, _ = sb.WriteString("\n" + wizard.RenderFileSource("self", logPath, jobName, "loki.write.logs.receiver"))

		logsWriter, err := wizard.RenderWriter(wizard.WriterLoki, "logs", dests, logsDest)
		if err != nil {
			return wizard.CommitResult{}, fmt.Errorf("logs_dest_name: %w", err)
		}
		_, _ = sb.WriteString("\n" + logsWriter)
	}

	var matchers []string
	if cp := get("cluster_pattern"); cp != "" {
		matchers = append(matchers, fmt.Sprintf(`cluster=~%q`, cp))
	}
	// Always the role the output was checked against (Role), so a pipeline
	// is only served to the collectors it was proven fit for.
	matchers = append(matchers, fmt.Sprintf(`role=%q`, w.Role(state)))

	return wizard.CommitResult{
		Contents: sb.String(),
		Matchers: matchers,
		Warnings: warnings,
	}, nil
}
