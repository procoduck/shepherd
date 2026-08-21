package receiver_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/schema"
	"shepherd/internal/version"
)

// This file proves the claim receiver.go's comments make: every component
// name and attribute path this package renders actually exists in the pinned
// schema artifact (internal/schema/artifacts/alloy-<version>.json), checked
// against the artifact rather than assumed from memory or from Grafana's
// docs. Modelled on internal/visual/render_test.go's "Corpus ↔ shipped
// artifact" suite, adapted from graph edges/ports to this package's fixed
// attribute paths since this renderer builds Alloy text directly rather than
// from a graph.
func shippedSchemaComponents() map[string]any {
	GinkgoHelper()
	reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
	Expect(err).NotTo(HaveOccurred())
	merged, _, err := reg.Get(reg.CurrentVersion())
	Expect(err).NotTo(HaveOccurred())
	raw, err := json.Marshal(merged)
	Expect(err).NotTo(HaveOccurred())
	var payload map[string]any
	Expect(json.Unmarshal(raw, &payload)).To(Succeed())
	comps, ok := payload["components"].(map[string]any)
	Expect(ok).To(BeTrue(), "schema payload must carry a components map")
	return comps
}

// attrPath is one (component, path-to-attribute) claim this package's
// renderer relies on. path's elements before the last name nested blocks to
// descend into; the last element is the attribute name.
type attrPath struct {
	component string
	path      []string
}

// renderedAttrs is every attribute render.go sets, one entry per line it
// emits. The list is checked in two directions: every entry must exist in the
// pinned schema artifact (so we never emit a field Alloy does not declare),
// and every entry must appear in at least one committed golden (so a fixture
// that stops exercising a field cannot silently stop proving anything about
// it). The second direction is the "goldens exercise every claimed attribute"
// spec below.
var renderedAttrs = []attrPath{
	{"otelcol.receiver.otlp", []string{"grpc", "endpoint"}},
	{"otelcol.receiver.otlp", []string{"grpc", "max_recv_msg_size"}},
	{"otelcol.receiver.otlp", []string{"grpc", "max_concurrent_streams"}},
	{"otelcol.receiver.otlp", []string{"grpc", "include_metadata"}},
	{"otelcol.receiver.otlp", []string{"http", "endpoint"}},
	{"otelcol.receiver.otlp", []string{"http", "max_request_body_size"}},
	{"otelcol.receiver.otlp", []string{"http", "include_metadata"}},
	{"otelcol.receiver.otlp", []string{"output", "metrics"}},
	{"otelcol.receiver.otlp", []string{"output", "logs"}},
	{"otelcol.receiver.otlp", []string{"output", "traces"}},

	{"otelcol.processor.batch", []string{"timeout"}},
	{"otelcol.processor.batch", []string{"send_batch_size"}},
	{"otelcol.processor.batch", []string{"send_batch_max_size"}},
	{"otelcol.processor.batch", []string{"output", "metrics"}},
	{"otelcol.processor.batch", []string{"output", "logs"}},
	{"otelcol.processor.batch", []string{"output", "traces"}},

	{"otelcol.exporter.otlp", []string{"client", "endpoint"}},
	{"otelcol.exporter.otlp", []string{"client", "headers"}},
	{"otelcol.exporter.otlp", []string{"client", "auth"}},
	{"otelcol.exporter.otlphttp", []string{"client", "endpoint"}},
	{"otelcol.exporter.otlphttp", []string{"client", "headers"}},
	{"otelcol.exporter.otlphttp", []string{"client", "auth"}},

	// D10 pass-through tenancy (internal/receiver.TenancyPassThrough): the
	// component and header attributes renderOTLPTenantAuth emits.
	{"otelcol.auth.headers", []string{"header", "key"}},
	{"otelcol.auth.headers", []string{"header", "from_context"}},
	{"otelcol.auth.headers", []string{"header", "action"}},

	{"faro.receiver", []string{"server", "listen_address"}},
	{"faro.receiver", []string{"server", "listen_port"}},
	{"faro.receiver", []string{"server", "max_allowed_payload_size"}},
	{"faro.receiver", []string{"server", "cors_allowed_origins"}},
	{"faro.receiver", []string{"server", "rate_limiting", "enabled"}},
	{"faro.receiver", []string{"server", "rate_limiting", "strategy"}},
	{"faro.receiver", []string{"server", "rate_limiting", "rate"}},
	{"faro.receiver", []string{"server", "rate_limiting", "burst_size"}},
	{"faro.receiver", []string{"output", "logs"}},
	{"faro.receiver", []string{"output", "traces"}},

	{"loki.write", []string{"endpoint", "url"}},
	{"loki.write", []string{"endpoint", "tenant_id"}},
	{"loki.write", []string{"endpoint", "bearer_token"}},
}

var _ = Describe("Every rendered component and attribute exists in the pinned schema artifact", func() {
	components := shippedSchemaComponents()

	It("declares every component this package emits", func() {
		seen := map[string]bool{}
		for _, a := range renderedAttrs {
			seen[a.component] = true
		}
		for name := range seen {
			Expect(components).To(HaveKey(name), "component %q is not in the pinned schema artifact", name)
		}
	})

	// DescribeTable's variadic args can't mix an explicit argument (the body
	// func) with a spread slice in the same call — build one []any holding
	// both and spread that.
	tableArgs := []any{
		func(a attrPath) {
			comp, ok := components[a.component].(map[string]any)
			Expect(ok).To(BeTrue(), "component %q not found", a.component)
			found := resolveAttrPath(comp, a.path)
			Expect(found).To(BeTrue(), "%s: attribute path %v not declared in the pinned schema artifact", a.component, a.path)
		},
	}
	for _, a := range renderedAttrs {
		tableArgs = append(tableArgs, Entry(fmt.Sprintf("%s %v", a.component, a.path), a))
	}
	DescribeTable("declares the attribute at the claimed path", tableArgs...)
})

// A claim checked only against the schema proves the attribute is spellable,
// not that this package ever spells it. Without this direction, deleting the
// line that renders an attribute leaves renderedAttrs still passing — the
// schema still declares it — and the entry quietly becomes a claim about
// Alloy rather than about Shepherd. Reading the committed goldens (the same
// files alloy_validate_test.go feeds to the real pinned binary) ties every
// entry back to output we actually produce and validate.
//
// Red run, executed: stripping the include_metadata lines from
// testdata/otlp-passthrough.golden.alloy (the only golden that renders them)
// fails this spec with `otelcol.receiver.otlp: no golden renders
// "include_metadata"`.
var _ = Describe("goldens exercise every claimed attribute", func() {
	It("renders each attribute in renderedAttrs in at least one golden", func() {
		paths, err := filepath.Glob(filepath.Join("testdata", "*.golden.alloy"))
		Expect(err).NotTo(HaveOccurred())
		Expect(paths).NotTo(BeEmpty(), "no goldens found — this spec would pass vacuously")

		var corpus strings.Builder
		for _, p := range paths {
			b, err := os.ReadFile(p)
			Expect(err).NotTo(HaveOccurred())
			corpus.Write(b)
			corpus.WriteString("\n")
		}
		text := corpus.String()

		for _, a := range renderedAttrs {
			leaf := a.path[len(a.path)-1]
			// Alloy assigns attributes as `name = value`, always at the start
			// of a line once indentation is trimmed. Anchoring on that avoids
			// matching the same word inside a block name, a string value or a
			// comment.
			re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(leaf) + `\s*=`)
			Expect(re.MatchString(text)).To(BeTrue(),
				"%s: no golden renders %q — either a fixture stopped exercising it, or the "+
					"entry is a claim about Alloy rather than about this package",
				a.component, leaf)
		}
	})
})

// resolveAttrPath walks path's leading elements as nested block names inside
// comp, then checks the final element is a declared attribute name on
// whichever block (or the component itself) that walk lands on.
func resolveAttrPath(comp map[string]any, path []string) bool {
	cur := comp
	for _, blockName := range path[:len(path)-1] {
		next := findBlock(cur, blockName)
		if next == nil {
			return false
		}
		cur = next
	}
	return hasAttribute(cur, path[len(path)-1])
}

func findBlock(comp map[string]any, name string) map[string]any {
	blocks, ok := comp["blocks"].([]any)
	if !ok {
		return nil
	}
	for _, raw := range blocks {
		b, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if n, _ := b["name"].(string); n == name { //nolint:errcheck // absent name never matches
			return b
		}
	}
	return nil
}

func hasAttribute(comp map[string]any, name string) bool {
	attrs, ok := comp["attributes"].([]any)
	if !ok {
		return false
	}
	for _, raw := range attrs {
		a, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if n, _ := a["name"].(string); n == name { //nolint:errcheck // absent name never matches
			return true
		}
	}
	return false
}
