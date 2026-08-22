package chartvalues_test

import (
	"os"
	"strings"
	"testing"

	"shepherd/internal/chartvalues"
)

// TestPinAgreesWithVendoredMeta is the Go half of G8's offline consistency
// check (the shell half is `make check-chartvalues-pin`): PinnedChartVersion
// and the provenance record vendored alongside the schema it describes must
// name the same chart release. A red run for this is trivial to produce —
// edit either string — which is exactly what the Makefile guard automates
// for CI; this copy exists so `go test` alone catches the same drift
// without requiring make.
func TestPinAgreesWithVendoredMeta(t *testing.T) {
	meta, err := chartvalues.LoadSchemaMeta()
	if err != nil {
		t.Fatalf("loading schema meta: %v", err)
	}
	if meta.ChartVersion != chartvalues.PinnedChartVersion {
		t.Errorf("testdata/values.schema.meta.json chart_version=%q but chartvalues.PinnedChartVersion=%q",
			meta.ChartVersion, chartvalues.PinnedChartVersion)
	}
	if meta.SHA256 == "" || meta.SourceURL == "" {
		t.Errorf("schema meta is missing provenance fields: %+v", meta)
	}
}

// --- Closed-world key-membership walker -----------------------------------
//
// jsonschema.Schema.Validate (schema.go's ValidateValues, exercised by
// TestGoldensValidateAgainstSchema below) is necessary but not sufficient
// proof that every key Render emits exists in the vendored schema: several
// objects in this schema — most importantly the top-level "collectors" map —
// declare no "properties"/"additionalProperties" restriction at all, so an
// invented key nested inside one would validate successfully and this
// package's brief ("every key you emit must exist in the vendored schema,"
// docs/gateway-tier-plan.md W9) would go unproven for exactly the paths that
// matter most. schemaPropertyNames below walks the RAW parsed document
// (chartvalues.SchemaDocument) and reads each node's declared "properties"
// set directly, resolving "$ref" indirection along the way, which is a
// strictly stronger check: it fails for a key the schema's author never
// wrote down, whether or not the object happens to be technically open.

// resolveRef follows node's "$ref" (if any) to the schema it points at,
// walking doc. A node with no "$ref" is returned unchanged. Loop-guarded
// against a self-referential "$ref" (none exist in this schema, but a
// silent infinite loop would be a worse failure than a loud one if that
// ever changed).
func resolveRef(t *testing.T, doc, node map[string]any) map[string]any {
	t.Helper()
	seen := map[string]bool{}
	for {
		refAny, ok := node["$ref"]
		if !ok {
			return node
		}
		ref, ok := refAny.(string)
		if !ok || seen[ref] {
			return node
		}
		seen[ref] = true
		ref = strings.TrimPrefix(ref, "#/")
		next := doc
		for _, part := range strings.Split(ref, "/") {
			child, ok := next[part].(map[string]any)
			if !ok {
				t.Fatalf("schema: cannot resolve $ref %q at segment %q", refAny, part)
			}
			next = child
		}
		node = next
	}
}

// schemaPropertyNames walks doc from the root through each literal key in
// path (resolving "$ref" before every hop, since a schema node reached via
// $ref indirection — as every "collectors.<name>" entry effectively is, per
// the top-level "alloy-metrics"/"alloy-logs"/etc. aliases this schema
// declares for exactly the same "#/definitions/alloy-collector" target the
// real chart's own helm-unittest fixtures use for "collectors.alloy-metrics"
// — must expose that target's declared properties, not its own empty
// wrapper), then returns the final node's own declared "properties" key set.
// Fails the test immediately if any hop or the final node is missing —
// there is no reasonable "the key might exist" fallback for a test whose
// entire job is proving existence.
func schemaPropertyNames(t *testing.T, doc map[string]any, path ...string) map[string]bool {
	t.Helper()
	node := doc
	for i, key := range path {
		node = resolveRef(t, doc, node)
		childAny, ok := node[key]
		if !ok {
			t.Fatalf("schema: no key %q at path %s", key, strings.Join(path[:i+1], "/"))
		}
		child, ok := childAny.(map[string]any)
		if !ok {
			t.Fatalf("schema: value at %s is not an object", strings.Join(path[:i+1], "/"))
		}
		node = child
	}
	node = resolveRef(t, doc, node)
	propsAny, ok := node["properties"]
	if !ok {
		t.Fatalf("schema: node at %s declares no \"properties\"", strings.Join(path, "/"))
	}
	props, ok := propsAny.(map[string]any)
	if !ok {
		t.Fatalf("schema: \"properties\" at %s is not an object", strings.Join(path, "/"))
	}
	out := make(map[string]bool, len(props))
	for k := range props {
		out[k] = true
	}
	return out
}

func requireKey(t *testing.T, set map[string]bool, key, context string) {
	t.Helper()
	if !set[key] {
		t.Errorf("%s: key %q is not declared in the vendored schema — chartvalues must not emit paths "+
			"it cannot prove against testdata/values.schema.json", context, key)
	}
}

// TestSchemaKeysExist proves, against the vendored schema itself rather than
// from memory, that every key path Render emits is real: cluster.name, and
// (via the alloy-collector definition every "collectors.<name>" entry
// shares — see schemaPropertyNames' doc comment) remoteConfig.{enabled,url,
// pollFrequency,auth,extraAttributes} and auth.{type,usernameFrom,
// passwordFrom}.
func TestSchemaKeysExist(t *testing.T) {
	doc, err := chartvalues.SchemaDocument()
	if err != nil {
		t.Fatalf("loading schema document: %v", err)
	}

	root := schemaPropertyNames(t, doc)
	requireKey(t, root, "cluster", "root")
	requireKey(t, root, "collectors", "root")

	cluster := schemaPropertyNames(t, doc, "properties", "cluster")
	requireKey(t, cluster, "name", "cluster")

	collector := schemaPropertyNames(t, doc, "definitions", "alloy-collector")
	requireKey(t, collector, "remoteConfig", "alloy-collector")

	remoteConfig := schemaPropertyNames(t, doc, "definitions", "alloy-collector", "properties", "remoteConfig")
	for _, key := range []string{"enabled", "url", "pollFrequency", "auth", "extraAttributes"} {
		requireKey(t, remoteConfig, key, "remoteConfig")
	}

	auth := schemaPropertyNames(t, doc, "definitions", "alloy-collector", "properties", "remoteConfig", "properties", "auth")
	for _, key := range []string{"type", "usernameFrom", "passwordFrom"} {
		requireKey(t, auth, key, "remoteConfig.auth")
	}
}

// TestSchemaPropertyNamesCatchesInventedKey is the walker's own red run: an
// invented field name that TestSchemaKeysExist's requireKey calls would
// otherwise silently accept if the lookup itself were broken (e.g. if a typo
// made it check the wrong node). Run in a sub-process-free way by asserting
// on the returned set directly rather than on t.Fatalf's exit path.
func TestSchemaPropertyNamesCatchesInventedKey(t *testing.T) {
	doc, err := chartvalues.SchemaDocument()
	if err != nil {
		t.Fatalf("loading schema document: %v", err)
	}
	remoteConfig := schemaPropertyNames(t, doc, "definitions", "alloy-collector", "properties", "remoteConfig")
	const invented = "shepherdMadeThisUp"
	if remoteConfig[invented] {
		t.Fatalf("test bug: schema unexpectedly declares %q — pick a different invented name", invented)
	}
}

// TestGoldensValidateAgainstSchema is G9's first half: every rendered golden
// must validate against the vendored values.schema.json.
func TestGoldensValidateAgainstSchema(t *testing.T) {
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile("testdata/golden/" + name + ".values.yaml")
			if err != nil {
				t.Fatalf("reading golden: %v", err)
			}
			if err := chartvalues.ValidateValues(b); err != nil {
				t.Errorf("golden %s does not validate against the vendored schema: %v", name, err)
			}
		})
	}
}

// TestValidateValuesRejectsUnknownType is ValidateValues' red run, proving it
// actually enforces the schema rather than merely parsing YAML and returning
// nil. It has to target cluster.name rather than anything under
// "collectors": TestSchemaKeysExist's own doc comment explains that
// "collectors" is a schema-open map, so the compiled schema validates
// nothing inside it at all — confirmed empirically (an earlier version of
// this test asserted rejection of a bad collectors.*.remoteConfig.enabled
// value and it was NOT rejected, exactly because nothing constrains that
// subtree). cluster.name IS declared `"type": "string"` at a level the
// schema does check, so a number there must be rejected.
func TestValidateValuesRejectsUnknownType(t *testing.T) {
	bad := []byte("cluster:\n  name: 123\n")
	if err := chartvalues.ValidateValues(bad); err == nil {
		t.Error("expected ValidateValues to reject cluster.name: 123 (schema declares it a string), got nil")
	}
}
