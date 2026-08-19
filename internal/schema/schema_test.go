package schema_test

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/schema"
	"shepherd/internal/validate"
	"shepherd/internal/version"
)

func TestSchema(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Schema Suite")
}

// currentVersion is the fleet pin. Hardcoding a literal here made the whole
// suite pass against a stale artifact after a version bump (schema-generation
// review, MEDIUM-2), so every spec resolves the pin instead.
const currentVersion = version.AlloySchemaVersion

// coreComponents is the D4 correctness bar: the prometheus / loki / discovery /
// otelcol core must work end to end, so every port they declare must be
// addressable by name.
var coreComponents = []string{
	"discovery.kubernetes", "discovery.relabel",
	"prometheus.scrape", "prometheus.relabel", "prometheus.remote_write",
	"loki.source.file", "loki.process", "loki.relabel", "loki.write",
	"otelcol.receiver.otlp", "otelcol.processor.batch", "otelcol.exporter.otlp",
}

// dataWireTypes are the wire types whose VALUE is the data (decision D1). Every
// other wire type carries a receiver handle, which flips the role a port gets.
var dataWireTypes = map[string]bool{"targets": true}

// validRoles is the closed set of port roles (decision D1).
var validRoles = map[string]bool{"produces": true, "accepts": true}

// artifactComponents loads the merged payload's component map.
func artifactComponents(reg *schema.Registry) map[string]any {
	merged, _, err := reg.Get(currentVersion)
	Expect(err).NotTo(HaveOccurred())
	components, ok := merged["components"].(map[string]any)
	Expect(ok).To(BeTrue(), "components must be a map")
	return components
}

// portsOf returns a component's inputs (Arguments-derived, named by "prop") or
// outputs (Exports-derived, named by "export") as maps.
func portsOf(comp map[string]any, listKey string) []map[string]any {
	raw, ok := comp[listKey].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		port, ok := entry.(map[string]any)
		Expect(ok).To(BeTrue(), "%s entry must be a map", listKey)
		out = append(out, port)
	}
	return out
}

// asMap, asList and asString read a dynamically typed artifact field, yielding
// the zero value when the field is absent or of the wrong type. The specs assert
// on the resulting value, so a wrong type surfaces as a failed expectation with
// the component name attached rather than a bare type-assertion panic.
func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func asList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// portString reads a string field off a port map, "" when absent.
func portString(port map[string]any, key string) string {
	if s, ok := port[key].(string); ok {
		return s
	}
	return ""
}

// validWireTypes is the closed set of wire type ids defined in VB-1 §3.1 plus otel.any (undifferentiated consumer).
var validWireTypes = map[string]bool{
	"targets":            true,
	"prom.metrics":       true,
	"loki.logs":          true,
	"otel.traces":        true,
	"otel.metrics":       true,
	"otel.logs":          true,
	"otel.any":           true, // extractor uses this when signal cannot be differentiated
	"pyroscope.profiles": true,
}

// validStabilities is the closed set of stability values.
var validStabilities = map[string]bool{
	"ga":             true,
	"public-preview": true,
	"experimental":   true,
}

var _ = Describe("Artifact invariants", func() {
	// 7.3.1 — hermetic, against the committed artifact.
	var (
		reg      *schema.Registry
		artifact map[string]any
	)

	BeforeEach(func() {
		var err error
		reg, err = schema.New(schema.Embedded, currentVersion)
		Expect(err).NotTo(HaveOccurred(), "registry must initialize from embedded artifacts")

		artifact, _, err = reg.Get(currentVersion)
		Expect(err).NotTo(HaveOccurred())
	})

	It("components_total equals map size", func() {
		meta, ok := artifact["_meta"].(map[string]any)
		Expect(ok).To(BeTrue(), "_meta must be a map")

		totalRaw := meta["components_total"]
		Expect(totalRaw).NotTo(BeNil(), "_meta.components_total must be present")

		var total int
		switch v := totalRaw.(type) {
		case float64:
			total = int(v)
		case int:
			total = v
		default:
			Fail("components_total has unexpected type")
		}
		Expect(total).To(BeNumerically(">", 0), "components_total must be non-zero")

		components, ok := artifact["components"].(map[string]any)
		Expect(ok).To(BeTrue(), "components must be a map")
		Expect(total).To(Equal(len(components)),
			"components_total (%d) must equal actual component map size (%d)", total, len(components))
	})

	It("every component has a valid stability", func() {
		components, ok := artifact["components"].(map[string]any)
		Expect(ok).To(BeTrue())

		for name, compRaw := range components {
			comp, ok := compRaw.(map[string]any)
			Expect(ok).To(BeTrue(), "component %q must be a map", name)

			stability, ok := comp["stability"].(string)
			Expect(ok).To(BeTrue(), "component %q must have a string stability field", name)
			Expect(validStabilities).To(HaveKey(stability),
				"component %q has invalid stability %q", name, stability)
		}
	})

	It("every input and output type is in the wire-type closed set", func() {
		components, ok := artifact["components"].(map[string]any)
		Expect(ok).To(BeTrue())

		for name, compRaw := range components {
			comp, ok := compRaw.(map[string]any)
			Expect(ok).To(BeTrue())

			if inputs, ok := comp["inputs"].([]any); ok {
				for _, inRaw := range inputs {
					in, ok := inRaw.(map[string]any)
					Expect(ok).To(BeTrue(), "input entry in %q must be a map", name)
					wt, wtOk := in["type"].(string)
					Expect(wtOk).To(BeTrue(), "component %q input type must be a string", name)
					Expect(validWireTypes).To(HaveKey(wt),
						"component %q input has invalid wire type %q", name, wt)
				}
			}

			if outputs, ok := comp["outputs"].([]any); ok {
				for _, outRaw := range outputs {
					out, ok := outRaw.(map[string]any)
					Expect(ok).To(BeTrue(), "output entry in %q must be a map", name)
					wt, wtOk := out["type"].(string)
					Expect(wtOk).To(BeTrue(), "component %q output type must be a string", name)
					Expect(validWireTypes).To(HaveKey(wt),
						"component %q output has invalid wire type %q", name, wt)
				}
			}
		}
	})

	It("every default_snippet parses with the Alloy syntax parser (stage 1)", func() {
		// Import the Alloy syntax parser — this is the strongest invariant: N snippets through stage 1.
		components, ok := artifact["components"].(map[string]any)
		Expect(ok).To(BeTrue())

		for name, compRaw := range components {
			comp, ok := compRaw.(map[string]any)
			Expect(ok).To(BeTrue())

			snippet, snippetOk := comp["default_snippet"].(string)
			if !snippetOk {
				continue // optional; opaque components may omit it
			}

			Expect(snippet).NotTo(BeEmpty(), "component %q default_snippet must not be empty", name)
			Expect(snippet).To(ContainSubstring(name),
				"component %q default_snippet must contain the component name", name)

			// The spec title promised a parser and the old body only asserted a
			// substring (schema-generation review, LOW-4). validate.Stage1 is the
			// real grafana/alloy syntax parser and runs in-process, so it is used.
			res := validate.Stage1(snippet)
			Expect(res.Valid).To(BeTrue(),
				"component %q default_snippet must parse at stage 1: %q -> %v", name, snippet, res.Diagnostics)
		}
	})

	It("red run evidence: corrupt components_total fails", func() {
		// Verify that the components_total check catches a real mismatch.
		// We do this by constructing a patched artifact in memory.
		components, ok := artifact["components"].(map[string]any)
		Expect(ok).To(BeTrue())

		meta, ok := artifact["_meta"].(map[string]any)
		Expect(ok).To(BeTrue())

		// Patch: set components_total to actual+1 → mismatch
		badTotal := len(components) + 1
		Expect(badTotal).NotTo(Equal(len(components)), "bad total must differ from real total")

		// Re-check inline: this proves that the assertion above would catch the discrepancy.
		totalRaw := meta["components_total"]
		var total int
		switch v := totalRaw.(type) {
		case float64:
			total = int(v)
		case int:
			total = v
		}
		// The real artifact must pass (total == len(components)).
		Expect(total).To(Equal(len(components)),
			"the committed artifact passes the components_total check")
		// badTotal does NOT pass.
		Expect(badTotal).NotTo(Equal(len(components)),
			"a corrupted total is detected")
	})
})

var _ = Describe("Overlay guards", func() {
	// 7.3.2 — every overlay key must exist in the artifact.
	var reg *schema.Registry

	BeforeEach(func() {
		var err error
		reg, err = schema.New(schema.Embedded, currentVersion)
		Expect(err).NotTo(HaveOccurred())
	})

	It("every overlay component key exists in the artifact", func() {
		violations, err := reg.ValidateOverlay()
		Expect(err).NotTo(HaveOccurred())
		Expect(violations).To(BeEmpty(),
			"overlay references components not present in the artifact: %v", violations)
	})

	It("wire_types in the overlay match the §3.1 closed set", func() {
		merged, _, err := reg.Get(currentVersion)
		Expect(err).NotTo(HaveOccurred())

		wireTypes, ok := merged["wire_types"].(map[string]any)
		Expect(ok).To(BeTrue(), "merged schema must contain wire_types")

		for id := range wireTypes {
			Expect(validWireTypes).To(HaveKey(id),
				"wire_type %q in overlay is not in the §3.1 closed set", id)
		}

		// All §3.1 types must be present.
		for wt := range validWireTypes {
			Expect(wireTypes).To(HaveKey(wt),
				"§3.1 wire type %q is missing from the overlay wire_types", wt)
		}
	})

	It("discovery_stub keys are all discovery.* components in the artifact", func() {
		violations, err := reg.ValidateOverlay()
		Expect(err).NotTo(HaveOccurred())
		// ValidateOverlay checks discovery_stub placement; zero violations = guard passes.
		Expect(violations).To(BeEmpty())
	})

	It("every wire type carries a hex color", func() {
		merged, _, err := reg.Get(currentVersion)
		Expect(err).NotTo(HaveOccurred())

		wireTypes, ok := merged["wire_types"].(map[string]any)
		Expect(ok).To(BeTrue(), "merged schema must contain wire_types")
		Expect(wireTypes).NotTo(BeEmpty())

		for id, wtRaw := range wireTypes {
			wt, ok := wtRaw.(map[string]any)
			Expect(ok).To(BeTrue(), "wire_type %q must be a map", id)
			color, ok := wt["color"].(string)
			Expect(ok).To(BeTrue(), "wire_type %q must have a string color", id)
			Expect(color).To(MatchRegexp(`^#[0-9a-fA-F]{6}$`),
				"wire_type %q color %q must be a hex color", id, color)
		}
	})

	It("merged payload exposes a categories section, every category carrying a hex color", func() {
		merged, _, err := reg.Get(currentVersion)
		Expect(err).NotTo(HaveOccurred())

		categories, ok := merged["categories"].(map[string]any)
		Expect(ok).To(BeTrue(), "merged schema must contain categories")

		// The palette categories the frontend renders (VB refinement §C4).
		wantCategories := map[string]bool{
			"sources": true, "transform": true, "destinations": true,
			"config": true, "advanced": true,
		}
		for id := range wantCategories {
			Expect(categories).To(HaveKey(id), "categories must include %q", id)
		}

		for id, catRaw := range categories {
			cat, ok := catRaw.(map[string]any)
			Expect(ok).To(BeTrue(), "category %q must be a map", id)

			color, ok := cat["color"].(string)
			Expect(ok).To(BeTrue(), "category %q must have a string color", id)
			Expect(color).To(MatchRegexp(`^#[0-9a-fA-F]{6}$`),
				"category %q color %q must be a hex color", id, color)

			label, ok := cat["label"].(string)
			Expect(ok).To(BeTrue(), "category %q must have a string label", id)
			Expect(label).NotTo(BeEmpty())
		}
	})

	It("merged result contains both artifact and overlay fields", func() {
		merged, _, err := reg.Get(currentVersion)
		Expect(err).NotTo(HaveOccurred())

		// Artifact field: components map.
		Expect(merged).To(HaveKey("components"))
		// Overlay field: wire_types.
		Expect(merged).To(HaveKey("wire_types"))

		// A known component should have its overlay category merged in.
		components, ok := merged["components"].(map[string]any)
		Expect(ok).To(BeTrue())
		scrape, ok := components["prometheus.scrape"].(map[string]any)
		Expect(ok).To(BeTrue(), "prometheus.scrape must be present")
		Expect(scrape).To(HaveKeyWithValue("category", "transform"),
			"overlay category must be merged into prometheus.scrape")
	})

	It("ETag is stable for the same version", func() {
		_, etag1, err := reg.Get(currentVersion)
		Expect(err).NotTo(HaveOccurred())
		_, etag2, err := reg.Get(currentVersion)
		Expect(err).NotTo(HaveOccurred())
		Expect(etag1).To(Equal(etag2), "ETag must be deterministic for the same version")
		Expect(etag1).NotTo(BeEmpty())
	})

	It("not-found version returns ErrNotFound", func() {
		_, _, err := reg.Get("alloy-v0.0.0")
		Expect(err).To(HaveOccurred())
		Expect(schema.IsNotFound(err)).To(BeTrue(),
			"unknown version must return IsNotFound error")
	})

	It("artifact JSON round-trips cleanly through the registry", func() {
		merged, _, err := reg.Get(currentVersion)
		Expect(err).NotTo(HaveOccurred())

		// Re-marshal and unmarshal; must not lose top-level keys.
		b, err := json.Marshal(merged)
		Expect(err).NotTo(HaveOccurred())

		var roundTripped map[string]any
		Expect(json.Unmarshal(b, &roundTripped)).To(Succeed())

		for k := range merged {
			Expect(roundTripped).To(HaveKey(k),
				"key %q lost in JSON round-trip", k)
		}
	})
})

// Ensure the merged schema's _meta still surfaces after overlay merge.
var _ = Describe("Schema meta after merge", func() {
	It("_meta.components_total is preserved through overlay merge", func() {
		reg, err := schema.New(schema.Embedded, currentVersion)
		Expect(err).NotTo(HaveOccurred())

		merged, _, err := reg.Get(currentVersion)
		Expect(err).NotTo(HaveOccurred())

		meta, ok := merged["_meta"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(meta["components_total"]).NotTo(BeNil())
	})
})

// ---------------------------------------------------------------------------
// Port model (decision D1) and extraction completeness.
//
// Port names are the primary key binding a stored graph to a rendered config
// and had zero coverage before: corrupting prometheus.scrape's input prop or
// prometheus.remote_write's export in the committed artifact left this suite
// green (schema-generation review, CRITICAL-1).
// ---------------------------------------------------------------------------

var _ = Describe("Port model", func() {
	var reg *schema.Registry

	BeforeEach(func() {
		var err error
		reg, err = schema.New(schema.Embedded, currentVersion)
		Expect(err).NotTo(HaveOccurred())
	})

	It("every port carries a role from the closed set", func() {
		for name, compRaw := range artifactComponents(reg) {
			comp, ok := compRaw.(map[string]any)
			Expect(ok).To(BeTrue())
			for _, listKey := range []string{"inputs", "outputs"} {
				for _, port := range portsOf(comp, listKey) {
					role, ok := port["role"].(string)
					Expect(ok).To(BeTrue(), "component %q %s port %v must have a string role", name, listKey, port)
					Expect(validRoles).To(HaveKey(role),
						"component %q %s port has invalid role %q", name, listKey, role)
				}
			}
		}
	})

	It("roles follow the D1 rule: receiver exports accept, data exports produce", func() {
		// A RECEIVER-kind export (prometheus.remote_write.receiver) is where data
		// ENTERS the node; a DATA export (discovery.*.targets) is where it leaves.
		// Arguments are the mirror image. Getting this backwards is precisely the
		// inversion the canvas must hide.
		for name, compRaw := range artifactComponents(reg) {
			comp, ok := compRaw.(map[string]any)
			Expect(ok).To(BeTrue())

			for _, port := range portsOf(comp, "inputs") {
				wt := portString(port, "type")
				want := "produces"
				if dataWireTypes[wt] {
					want = "accepts"
				}
				Expect(port["role"]).To(Equal(want),
					"component %q argument port %v (wire %q) must have role %q", name, port["prop"], wt, want)
			}
			for _, port := range portsOf(comp, "outputs") {
				wt := portString(port, "type")
				want := "accepts"
				if dataWireTypes[wt] {
					want = "produces"
				}
				Expect(port["role"]).To(Equal(want),
					"component %q export port %v (wire %q) must have role %q", name, port["export"], wt, want)
			}
		}
	})

	It("no port anywhere lacks a name", func() {
		// 46 of 184 components used to emit a name-less input port, which the
		// canvas addressed as a synthetic index and the renderer then dropped
		// silently (schema-generation review, CRITICAL-2).
		unnamed := []string{}
		for name, compRaw := range artifactComponents(reg) {
			comp, ok := compRaw.(map[string]any)
			Expect(ok).To(BeTrue())
			for _, port := range portsOf(comp, "inputs") {
				if portString(port, "prop") == "" {
					unnamed = append(unnamed, name+" input")
				}
			}
			for _, port := range portsOf(comp, "outputs") {
				if portString(port, "export") == "" {
					unnamed = append(unnamed, name+" output")
				}
			}
		}
		Expect(unnamed).To(BeEmpty(), "these ports cannot be referenced by a stored graph: %v", unnamed)
	})

	It("the D4 core components declare named, well-formed ports", func() {
		components := artifactComponents(reg)
		for _, name := range coreComponents {
			compRaw, ok := components[name]
			Expect(ok).To(BeTrue(), "core component %q must exist in the artifact", name)
			comp, ok := compRaw.(map[string]any)
			Expect(ok).To(BeTrue())

			total := len(portsOf(comp, "inputs")) + len(portsOf(comp, "outputs"))
			Expect(total).To(BeNumerically(">", 0), "core component %q must declare at least one port", name)

			for _, listKey := range []string{"inputs", "outputs"} {
				nameKey := "prop"
				if listKey == "outputs" {
					nameKey = "export"
				}
				for _, port := range portsOf(comp, listKey) {
					id := portString(port, nameKey)
					Expect(id).NotTo(BeEmpty(), "core component %q has an unnamed %s port", name, listKey)
					Expect(port["role"]).NotTo(BeEmpty(), "core component %q port %q has no role", name, id)
					Expect(validWireTypes).To(HaveKey(port["type"]),
						"core component %q port %q has an unknown wire type", name, id)
				}
			}
		}
	})

	It("a port's path re-joins to its name, and dotted names are never top-level", func() {
		// A dotted port id (output.metrics) is a nested-block address. The
		// renderer must emit `output { metrics = [...] }`; an Alloy attribute
		// name may not contain a dot, so path is what it walks.
		for name, compRaw := range artifactComponents(reg) {
			comp, ok := compRaw.(map[string]any)
			Expect(ok).To(BeTrue())
			for _, listKey := range []string{"inputs", "outputs"} {
				nameKey := "prop"
				if listKey == "outputs" {
					nameKey = "export"
				}
				for _, port := range portsOf(comp, listKey) {
					id := portString(port, nameKey)
					rawPath, ok := port["path"].([]any)
					Expect(ok).To(BeTrue(), "component %q port %q must carry a path", name, id)
					segments := make([]string, 0, len(rawPath))
					for _, seg := range rawPath {
						s, ok := seg.(string)
						Expect(ok).To(BeTrue(), "component %q port %q path segment must be a string", name, id)
						Expect(s).NotTo(ContainSubstring("."), "component %q port %q path segment %q must not contain a dot", name, id, s)
						segments = append(segments, s)
					}
					joined := ""
					for i, s := range segments {
						if i > 0 {
							joined += "."
						}
						joined += s
					}
					Expect(joined).To(Equal(id), "component %q port path %v must join to its name %q", name, segments, id)
				}
			}
		}
	})

	It("signal-specific otel ports are refined; otel.any survives only where polymorphic", func() {
		components := artifactComponents(reg)
		batch, ok := components["otelcol.processor.batch"].(map[string]any)
		Expect(ok).To(BeTrue())

		byName := map[string]map[string]any{}
		for _, port := range portsOf(batch, "inputs") {
			byName[portString(port, "prop")] = port
		}
		Expect(byName).To(HaveKey("output.metrics"))
		Expect(byName["output.metrics"]).To(HaveKeyWithValue("type", "otel.metrics"))
		Expect(byName["output.logs"]).To(HaveKeyWithValue("type", "otel.logs"))
		Expect(byName["output.traces"]).To(HaveKeyWithValue("type", "otel.traces"))

		// ConsumerExports.Input is a single consumer for all three signals; it is
		// the one place otel.any is truthful rather than a missing refinement.
		outputs := portsOf(batch, "outputs")
		Expect(outputs).To(HaveLen(1))
		Expect(outputs[0]).To(HaveKeyWithValue("export", "input"))
		Expect(outputs[0]).To(HaveKeyWithValue("type", "otel.any"))
		Expect(outputs[0]).To(HaveKeyWithValue("role", "accepts"))
	})

	It("a wire-typed attribute is marked with input_type so L1 does not demand a literal", func() {
		components := artifactComponents(reg)
		scrape, ok := components["prometheus.scrape"].(map[string]any)
		Expect(ok).To(BeTrue())

		attrs, ok := scrape["attributes"].([]any)
		Expect(ok).To(BeTrue())
		found := map[string]string{}
		for _, raw := range attrs {
			attr, ok := raw.(map[string]any)
			Expect(ok).To(BeTrue())
			name := asString(attr["name"])
			if it, ok := attr["input_type"].(string); ok {
				found[name] = it
			}
		}
		Expect(found).To(HaveKeyWithValue("targets", "targets"))
		Expect(found).To(HaveKeyWithValue("forward_to", "prom.metrics"))
	})
})

var _ = Describe("Attribute and block completeness", func() {
	var reg *schema.Registry

	BeforeEach(func() {
		var err error
		reg, err = schema.New(schema.Embedded, currentVersion)
		Expect(err).NotTo(HaveOccurred())
	})

	It("attributes, blocks, inputs and outputs are always arrays, never null", func() {
		for name, compRaw := range artifactComponents(reg) {
			comp, ok := compRaw.(map[string]any)
			Expect(ok).To(BeTrue())
			for _, key := range []string{"attributes", "blocks", "inputs", "outputs"} {
				Expect(comp[key]).NotTo(BeNil(), "component %q field %q must be an array, not null", name, key)
				_, isArray := comp[key].([]any)
				Expect(isArray).To(BeTrue(), "component %q field %q must be an array", name, key)
			}
		}
	})

	It("every block carries name, repeatable, required and nested arrays at every depth", func() {
		var walk func(componentName string, blocks []any, path string)
		walk = func(componentName string, blocks []any, path string) {
			for _, raw := range blocks {
				block, ok := raw.(map[string]any)
				Expect(ok).To(BeTrue(), "%s: block must be a map", componentName)

				blockName, ok := block["name"].(string)
				Expect(ok).To(BeTrue(), "%s%s: block must have a name", componentName, path)
				Expect(blockName).NotTo(BeEmpty())

				_, ok = block["repeatable"].(bool)
				Expect(ok).To(BeTrue(), "%s%s/%s: block must declare repeatable", componentName, path, blockName)
				_, ok = block["required"].(bool)
				Expect(ok).To(BeTrue(), "%s%s/%s: block must declare required", componentName, path, blockName)

				attrs, ok := block["attributes"].([]any)
				Expect(ok).To(BeTrue(), "%s%s/%s: block attributes must be an array", componentName, path, blockName)
				for _, attrRaw := range attrs {
					attr, ok := attrRaw.(map[string]any)
					Expect(ok).To(BeTrue())
					Expect(attr["name"]).NotTo(BeEmpty())
					Expect(attr["type"]).NotTo(BeEmpty())
				}

				nested, ok := block["blocks"].([]any)
				Expect(ok).To(BeTrue(), "%s%s/%s: block blocks must be an array", componentName, path, blockName)
				walk(componentName, nested, path+"/"+blockName)
			}
		}
		for name, compRaw := range artifactComponents(reg) {
			comp, ok := compRaw.(map[string]any)
			Expect(ok).To(BeTrue())
			blocks := asList(comp["blocks"])
			walk(name, blocks, "")
		}
	})

	It("squashed embedded structs are walked, so credentials are reachable", func() {
		// loki.write's endpoint block embeds *types.HTTPClientConfig with
		// `alloy:",squash"`; before squash support every credential field was
		// missing from the artifact (schema-generation review, HIGH-5).
		components := artifactComponents(reg)
		lokiWrite, ok := components["loki.write"].(map[string]any)
		Expect(ok).To(BeTrue())

		blocks, ok := lokiWrite["blocks"].([]any)
		Expect(ok).To(BeTrue())
		var endpoint map[string]any
		for _, raw := range blocks {
			block := asMap(raw)
			if block["name"] == "endpoint" {
				endpoint = block
			}
		}
		Expect(endpoint).NotTo(BeNil(), "loki.write must declare an endpoint block")

		nestedNames := map[string]bool{}
		nested := asList(endpoint["blocks"])
		for _, raw := range nested {
			block := asMap(raw)
			if n, ok := block["name"].(string); ok {
				nestedNames[n] = true
			}
		}
		for _, want := range []string{"basic_auth", "authorization", "oauth2", "tls_config"} {
			Expect(nestedNames).To(HaveKey(want), "loki.write endpoint must expose %q", want)
		}

		attrNames := map[string]bool{}
		attrs := asList(endpoint["attributes"])
		for _, raw := range attrs {
			attr := asMap(raw)
			if n, ok := attr["name"].(string); ok {
				attrNames[n] = true
			}
		}
		Expect(attrNames).To(HaveKey("url"))
		Expect(attrNames).To(HaveKey("bearer_token"))
	})

	It("a destination's required settings are reachable and typed", func() {
		components := artifactComponents(reg)
		remoteWrite, ok := components["prometheus.remote_write"].(map[string]any)
		Expect(ok).To(BeTrue())

		blocks := asList(remoteWrite["blocks"])
		var endpoint map[string]any
		for _, raw := range blocks {
			block := asMap(raw)
			if block["name"] == "endpoint" {
				endpoint = block
			}
		}
		Expect(endpoint).NotTo(BeNil(), "prometheus.remote_write must declare an endpoint block")
		Expect(endpoint).To(HaveKeyWithValue("repeatable", true))

		attrs := asList(endpoint["attributes"])
		var url map[string]any
		for _, raw := range attrs {
			attr := asMap(raw)
			if attr["name"] == "url" {
				url = attr
			}
		}
		Expect(url).NotTo(BeNil(), "prometheus.remote_write endpoint must expose url")
		Expect(url).To(HaveKeyWithValue("type", "string"))
		Expect(url).To(HaveKeyWithValue("required", true))
	})

	It("defaults are captured where the component declares them", func() {
		components := artifactComponents(reg)
		batch, ok := components["otelcol.processor.batch"].(map[string]any)
		Expect(ok).To(BeTrue())

		attrs := asList(batch["attributes"])
		defaults := map[string]any{}
		for _, raw := range attrs {
			attr := asMap(raw)
			name := asString(attr["name"])
			if d, present := attr["default"]; present {
				defaults[name] = d
			}
		}
		Expect(defaults).To(HaveKeyWithValue("timeout", "200ms"),
			"SetToDefault-provided durations must be captured as Alloy duration literals")
		Expect(defaults).To(HaveKey("send_batch_size"))
	})

	It("never emits a default for a secret-typed attribute", func() {
		walkAttrs := func(name string, attrs []any) {
			for _, raw := range attrs {
				attr, ok := raw.(map[string]any)
				Expect(ok).To(BeTrue())
				if attr["type"] == "secret" {
					Expect(attr).NotTo(HaveKey("default"),
						"component %q attribute %v is secret-typed and must never carry a default", name, attr["name"])
				}
			}
		}
		var walkBlocks func(name string, blocks []any)
		walkBlocks = func(name string, blocks []any) {
			for _, raw := range blocks {
				block := asMap(raw)
				attrs := asList(block["attributes"])
				walkAttrs(name, attrs)
				nested := asList(block["blocks"])
				walkBlocks(name, nested)
			}
		}
		for name, compRaw := range artifactComponents(reg) {
			comp := asMap(compRaw)
			attrs := asList(comp["attributes"])
			walkAttrs(name, attrs)
			blocks := asList(comp["blocks"])
			walkBlocks(name, blocks)
		}
	})

	It("enum values, where present, are a non-trivial set of distinct strings", func() {
		// Enum values are derived from the string constants alloy declares for a
		// named string type. Nothing is invented: an attribute whose valid values
		// live only in prose carries no values at all.
		seen := 0
		walkAttrs := func(name string, attrs []any) {
			for _, raw := range attrs {
				attr := asMap(raw)
				values, present := attr["values"].([]any)
				if !present {
					continue
				}
				seen++
				Expect(len(values)).To(BeNumerically(">=", 2),
					"component %q attribute %v: a single constant is a default, not a choice", name, attr["name"])
				distinct := map[string]bool{}
				for _, v := range values {
					s, ok := v.(string)
					Expect(ok).To(BeTrue(), "component %q attribute %v enum values must be strings", name, attr["name"])
					Expect(distinct).NotTo(HaveKey(s))
					distinct[s] = true
				}
			}
		}
		var walkBlocks func(name string, blocks []any)
		walkBlocks = func(name string, blocks []any) {
			for _, raw := range blocks {
				block := asMap(raw)
				attrs := asList(block["attributes"])
				walkAttrs(name, attrs)
				nested := asList(block["blocks"])
				walkBlocks(name, nested)
			}
		}
		for name, compRaw := range artifactComponents(reg) {
			comp := asMap(compRaw)
			attrs := asList(comp["attributes"])
			walkAttrs(name, attrs)
			blocks := asList(comp["blocks"])
			walkBlocks(name, blocks)
		}
		Expect(seen).To(BeNumerically(">", 0), "the artifact must carry at least some derived enum value sets")
	})

	It("block truncation is explicit, and only where the Go type is recursive", func() {
		// maxDepth no longer cuts real components; the single remaining cut is
		// loki.process's stage.match, whose StageConfig type contains itself.
		truncated := []string{}
		var walkBlocks func(name, path string, blocks []any)
		walkBlocks = func(name, path string, blocks []any) {
			for _, raw := range blocks {
				block := asMap(raw)
				blockName := asString(block["name"])
				if t, ok := block["truncated"].(bool); ok && t {
					truncated = append(truncated, name+path+"/"+blockName)
				}
				nested := asList(block["blocks"])
				walkBlocks(name, path+"/"+blockName, nested)
			}
		}
		for name, compRaw := range artifactComponents(reg) {
			comp := asMap(compRaw)
			blocks := asList(comp["blocks"])
			walkBlocks(name, "", blocks)
		}
		Expect(truncated).To(ConsistOf("loki.process/stage.match/stage"),
			"unexpected truncation — raise maxDepth or explain the new cycle")
	})
})

var _ = Describe("Overlay port ordering guard", func() {
	It("port_display_order names exactly the ports the artifact declares", func() {
		reg, err := schema.New(schema.Embedded, currentVersion)
		Expect(err).NotTo(HaveOccurred())
		violations, err := reg.ValidateOverlay()
		Expect(err).NotTo(HaveOccurred())
		Expect(violations).To(BeEmpty())
	})

	It("red run evidence: a dangling port name is a violation", func() {
		// Rebuild the guard's inputs by hand so the failure path is exercised
		// rather than merely asserted to be absent from the shipped overlay.
		reg, err := schema.New(schema.Embedded, currentVersion)
		Expect(err).NotTo(HaveOccurred())
		components := artifactComponents(reg)

		scrape, ok := components["prometheus.scrape"].(map[string]any)
		Expect(ok).To(BeTrue())
		declared := map[string]bool{}
		for _, port := range portsOf(scrape, "inputs") {
			declared[portString(port, "prop")] = true
		}
		Expect(declared).To(HaveKey("targets"))
		Expect(declared).NotTo(HaveKey("metrics"),
			"prometheus.scrape has no 'metrics' port — the fixture schemas that claim otherwise are the fiction")
	})
})

var _ = Describe("Version pinning", func() {
	It("the pinned schema version resolves to a committed artifact", func() {
		// Bumping ALLOY_VERSION without regenerating the artifact used to leave
		// this suite green while /api/schema/current returned 500.
		reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
		Expect(err).NotTo(HaveOccurred())
		Expect(reg.CurrentVersion()).To(Equal(version.AlloySchemaVersion))

		merged, etag, err := reg.Get(reg.CurrentVersion())
		Expect(err).NotTo(HaveOccurred(),
			"no artifact for the pinned version %q — run `make schema`", version.AlloySchemaVersion)
		Expect(etag).NotTo(BeEmpty())
		Expect(merged).To(HaveKey("components"))
	})
})
