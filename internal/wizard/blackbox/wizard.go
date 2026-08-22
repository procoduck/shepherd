// Package blackbox implements the "blackbox" wizard: probes a list of
// HTTP/TCP/ICMP/DNS targets via the blackbox exporter and forwards the
// resulting probe-success/latency metrics to a Prometheus destination. One
// of the five catalog wizards docs/gateway-tier-plan.md W8 asks for.
//
// This wizard always targets role=metrics (see Role): prometheus.exporter.
// blackbox and prometheus.scrape/prometheus.remote_write speak only the
// prom.metrics wire type. wizard.Register still checks that claim against
// the actual generated output on every commit (internal/wizard/role.go).
package blackbox

import (
	"fmt"
	"strings"

	"shepherd/internal/wizard"
)

// Kind identifies the blackbox wizard.
const Kind = "blackbox"

// role is the fixed collector role this wizard's output is always checked
// against.
const role = "metrics"

func init() {
	wizard.Register(&Wizard{})
}

// Wizard generates a blackbox probe metrics pipeline.
type Wizard struct{}

// Kind returns the wizard kind identifier.
func (w *Wizard) Kind() string { return Kind }

// Role always returns "metrics": prometheus.exporter.blackbox's only output
// is prom.metrics, regardless of state.
func (w *Wizard) Role(map[string]any) string { return role }

// modules lists the blackbox exporter's built-in default modules this
// wizard is willing to select. The blackbox_exporter binary Alloy embeds
// ships built-in default modules when no config_file/config override is
// given (this wizard deliberately never sets either), but WHICH names those
// defaults carry is a fact about prometheus/blackbox_exporter's own
// modules.yml — not something internal/schema/artifacts' Alloy component
// schema declares or the pinned `alloy validate` binary checks (a bad
// module name is a probe-time failure, invisible to config validation). Per
// docs/gateway-tier-plan.md §8 rule 6 ("verify version-dependent facts
// against pinned artifacts... never memory"), this list is deliberately
// kept to the two module names documented and used verbatim in Grafana's
// own published k8s-monitoring/blackbox examples (http_2xx, tcp_connect)
// rather than a longer list this package cannot verify the same way it
// verifies component/attribute names. Widen it only alongside a citable
// source for the additional name.
var modules = map[string]bool{
	"http_2xx":    true,
	"tcp_connect": true,
}

// Schema returns the wizard's input schema.
func (w *Wizard) Schema() wizard.Schema {
	return wizard.Schema{
		Kind:  Kind,
		Title: "Blackbox Probes",
		Steps: []wizard.Step{
			{
				ID:    "targets",
				Title: "Probe targets",
				Fields: []wizard.StepField{
					{
						Name: "probe_targets", Label: "Targets", Type: "text", Required: true,
						Placeholder: "https://example.com,https://example.org",
						Description: "Comma-separated URLs/hosts to probe.",
					},
					{
						Name: "module", Label: "Probe module", Type: "select", Default: "http_2xx",
						Options: []string{"http_2xx", "tcp_connect"},
					},
					{Name: "job_name", Label: "Job label", Type: "text", Default: "blackbox"},
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

	rawTargets := get("probe_targets")
	if rawTargets == "" {
		return wizard.CommitResult{}, fmt.Errorf("probe_targets is required")
	}
	var targets []string
	for _, t := range strings.Split(rawTargets, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		return wizard.CommitResult{}, fmt.Errorf("probe_targets must contain at least one non-empty target")
	}

	module := get("module")
	if module == "" {
		module = "http_2xx"
	}
	if !modules[module] {
		return wizard.CommitResult{}, fmt.Errorf(
			"module %q is not a built-in blackbox_exporter module this wizard supports, want one of: http_2xx|tcp_connect", module)
	}
	metricsDest := get("metrics_dest_name")
	if metricsDest == "" {
		return wizard.CommitResult{}, fmt.Errorf("metrics_dest_name is required")
	}
	jobName := get("job_name")
	if jobName == "" {
		jobName = "blackbox"
	}
	scrapeInterval := get("scrape_interval")
	if scrapeInterval == "" {
		scrapeInterval = "60s"
	}

	var sb strings.Builder

	_, _ = sb.WriteString(`prometheus.exporter.blackbox "probes" {
`)
	for i, addr := range targets {
		_, _ = fmt.Fprintf(&sb, `  target {
    name    = %q
    address = %q
    module  = %q
  }
`, probeName(addr, i), addr, module)
	}
	_, _ = sb.WriteString("}\n")

	_, _ = fmt.Fprintf(&sb, `
prometheus.scrape "probes" {
  targets         = prometheus.exporter.blackbox.probes.targets
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

// probeName derives a stable, readable target{} block name from a probe
// address: lowercased, every run of non [a-z0-9] characters collapsed to a
// single "-", trimmed of leading/trailing "-". Falls back to a positional
// "probe-N" when that leaves nothing (e.g. an address that is only
// punctuation) — the target block's "name" attribute must be non-empty for
// Alloy to accept it, checked against blackbox_exporter's target block
// alongside every other attribute this file emits.
func probeName(addr string, i int) string {
	var b strings.Builder
	lastDash := true // suppresses a leading '-'
	for _, r := range strings.ToLower(addr) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	name := strings.TrimSuffix(b.String(), "-")
	if name == "" {
		return fmt.Sprintf("probe-%d", i+1)
	}
	return name
}
