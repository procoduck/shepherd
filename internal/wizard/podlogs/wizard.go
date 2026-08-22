// Package podlogs implements the "pod-logs" wizard: discovers Kubernetes
// pods matching a namespace pattern and tails their container logs into a
// Loki destination. One of the five catalog wizards
// docs/gateway-tier-plan.md W8 asks for.
//
// This wizard always targets role=logs (see Role): every component it can
// emit — loki.source.kubernetes, loki.process, loki.write — speaks the
// loki.logs wire type exclusively (internal/signals classifies it as
// Logs), and the discovery components upstream of them (discovery.kubernetes,
// discovery.relabel) speak only the signal-free "targets" plumbing wire.
// wizard.Register still checks that claim against the actual generated
// output on every commit (internal/wizard/role.go).
package podlogs

import (
	"fmt"
	"strings"

	"shepherd/internal/wizard"
)

// Kind identifies the pod-logs wizard.
const Kind = "pod-logs"

// role is the fixed collector role this wizard's output is always checked
// against.
const role = "logs"

func init() {
	wizard.Register(&Wizard{})
}

// Wizard generates a Kubernetes pod log collection pipeline.
type Wizard struct{}

// Kind returns the wizard kind identifier.
func (w *Wizard) Kind() string { return Kind }

// Role always returns "logs": this wizard's template only ever emits
// loki-wire components downstream of plumbing-only discovery, regardless of
// state.
func (w *Wizard) Role(map[string]any) string { return role }

// logFormats lists the loki.process stage names this wizard is willing to
// emit — a strict subset of the schema's declared stage.* blocks
// (internal/schema/artifacts/alloy-v1.18.1.json), not every stage that
// exists. Accepting an arbitrary operator-supplied string here would let a
// typo become an unparseable `stage.<garbage> {}` block that Stage 1 syntax
// checking cannot catch (block names aren't schema-checked until the real
// binary runs) — validated against the pinned artifact by
// schema_conformance_test.go, not memory.
var logFormats = map[string]bool{
	"logfmt": true,
	"json":   true,
	"cri":    true,
	"docker": true,
}

// Schema returns the wizard's input schema.
func (w *Wizard) Schema() wizard.Schema {
	return wizard.Schema{
		Kind:  Kind,
		Title: "Pod Logs",
		Steps: []wizard.Step{
			{
				ID:    "targets",
				Title: "Pod selection",
				Fields: []wizard.StepField{
					{
						Name: "namespace_pattern", Label: "Namespace pattern (regex)", Type: "text", Required: true,
						Placeholder: "prod-.*", Description: "Only pods in namespaces matching this regex are tailed.",
					},
					{
						Name: "log_format", Label: "Log line format", Type: "select",
						Options: []string{"", "logfmt", "json", "cri", "docker"}, Default: "",
						Description: "Leave blank to ship raw lines unparsed.",
					},
				},
			},
			{
				ID:    "destinations",
				Title: "Destinations",
				Fields: []wizard.StepField{
					{
						Name: "logs_dest_name", Label: "Logs destination (Loki)", Type: "text", Required: true,
						Description: "Name of a Loki-type destination in this org.",
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

	namespacePattern := get("namespace_pattern")
	if namespacePattern == "" {
		return wizard.CommitResult{}, fmt.Errorf("namespace_pattern is required")
	}
	logsDest := get("logs_dest_name")
	if logsDest == "" {
		return wizard.CommitResult{}, fmt.Errorf("logs_dest_name is required")
	}
	logFormat := get("log_format")
	if logFormat != "" && !logFormats[logFormat] {
		return wizard.CommitResult{}, fmt.Errorf(
			"log_format %q is not supported, want one of: logfmt|json|cri|docker (or empty for raw lines)", logFormat)
	}

	var sb strings.Builder

	_, _ = sb.WriteString(`discovery.kubernetes "pods" {
  role = "pod"
}
`)

	_, _ = fmt.Fprintf(&sb, `
discovery.relabel "pods" {
  targets = discovery.kubernetes.pods.targets

  rule {
    source_labels = ["__meta_kubernetes_namespace"]
    regex         = "%s"
    action        = "keep"
  }
}
`, namespacePattern)

	_, _ = sb.WriteString(`
loki.source.kubernetes "pods" {
  targets    = discovery.relabel.pods.output
  forward_to = [loki.process.pods.receiver]
}
`)

	if logFormat != "" {
		_, _ = fmt.Fprintf(&sb, `
loki.process "pods" {
  forward_to = [loki.write.logs.receiver]
  stage.%s {}
}
`, logFormat)
	} else {
		_, _ = sb.WriteString(`
loki.process "pods" {
  forward_to = [loki.write.logs.receiver]
}
`)
	}

	_, _ = fmt.Fprintf(&sb, `
loki.write "logs" {
  endpoint {
    name = "%s"
    url  = sys.env("SHEPHERD_DEST_%s_URL")
    // auth injected by Shepherd at serve time
  }
}
`, logsDest, strings.ToUpper(strings.ReplaceAll(logsDest, "-", "_")))

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
