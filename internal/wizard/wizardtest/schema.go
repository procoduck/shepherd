package wizardtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"shepherd/internal/schema"
	"shepherd/internal/version"
)

// AttrPath is one (component, path-to-attribute) claim a wizard's renderer
// relies on. Path's elements before the last name nested blocks to descend
// into; the last element is the attribute name. Same shape as
// internal/receiver/schema_conformance_test.go's attrPath — this package
// generalizes that pattern so every wizard catalog package (five, as of
// docs/gateway-tier-plan.md W8) does not reimplement it.
type AttrPath struct {
	Component string
	Path      []string
}

// ShippedSchemaComponents loads the real, embedded, pinned schema artifact
// (the same one internal/wizard/role.go checks every wizard's output
// against at runtime) and returns its components map.
func ShippedSchemaComponents(t *testing.T) map[string]any {
	t.Helper()
	reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
	if err != nil {
		t.Fatalf("schema.New: %v", err)
	}
	merged, _, err := reg.Get(reg.CurrentVersion())
	if err != nil {
		t.Fatalf("reg.Get(%s): %v", reg.CurrentVersion(), err)
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		t.Fatalf("marshal merged schema: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal merged schema: %v", err)
	}
	comps, ok := payload["components"].(map[string]any)
	if !ok {
		t.Fatalf("schema payload must carry a components map")
	}
	return comps
}

// AssertSchemaConformance proves, for one wizard package's rendered output,
// the two claims internal/receiver/schema_conformance_test.go proves for
// internal/receiver: every component/attribute the package's templates emit
// is actually declared in the pinned schema artifact (so nothing is
// fabricated from memory), AND every one of those attrs is exercised by at
// least one committed golden in testdataDir (so a fixture that stops
// exercising a field cannot silently stop proving anything about it).
func AssertSchemaConformance(t *testing.T, attrs []AttrPath, testdataDir string) {
	t.Helper()
	components := ShippedSchemaComponents(t)

	seen := map[string]bool{}
	for _, a := range attrs {
		seen[a.Component] = true
	}
	for name := range seen {
		if _, ok := components[name]; !ok {
			t.Errorf("component %q is not in the pinned schema artifact %s", name, version.AlloySchemaVersion)
		}
	}

	for _, a := range attrs {
		comp, ok := components[a.Component].(map[string]any)
		if !ok {
			t.Errorf("component %q not found in schema", a.Component)
			continue
		}
		if !resolveAttrPath(comp, a.Path) {
			t.Errorf("%s: attribute path %v not declared in the pinned schema artifact", a.Component, a.Path)
		}
	}

	paths, err := filepath.Glob(filepath.Join(testdataDir, "*.golden.alloy"))
	if err != nil {
		t.Fatalf("glob goldens: %v", err)
	}
	if len(paths) == 0 {
		t.Fatalf("no goldens found in %s — this spec would pass vacuously", testdataDir)
	}
	var corpus strings.Builder
	for _, p := range paths {
		b, err := os.ReadFile(p) //nolint:gosec // p comes from filepath.Glob over a caller-fixed testdataDir, not external input
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		corpus.Write(b)
		corpus.WriteString("\n")
	}
	text := corpus.String()
	for _, a := range attrs {
		leaf := a.Path[len(a.Path)-1]
		// Alloy assigns attributes as `name = value`, always at the start of
		// a line once indentation is trimmed. Anchoring on that avoids
		// matching the same word inside a block name, a string value, or a
		// comment.
		re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(leaf) + `\s*=`)
		if !re.MatchString(text) {
			t.Errorf("%s: no golden in %s renders %q — either a fixture stopped exercising it, "+
				"or the entry is a claim about Alloy rather than about this wizard", a.Component, testdataDir, leaf)
		}
	}
}

// HasBlock reports whether component declares a block named blockName —
// for the shape AttrPath can't express: a block emitted empty (e.g.
// `stage.json {}`, no attributes set inside it), where there is no
// attribute leaf left to anchor an AttrPath on. components is
// ShippedSchemaComponents' return value.
func HasBlock(components map[string]any, component, blockName string) bool {
	comp, ok := components[component].(map[string]any)
	if !ok {
		return false
	}
	return findBlock(comp, blockName) != nil
}

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
