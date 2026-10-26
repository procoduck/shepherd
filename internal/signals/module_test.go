package signals

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #378: an instance of a module the pipeline declares itself (`declare "x"`
// plus `x "feature"`) is known, and carries exactly the signals of the
// declare body Derive already walks. Before, every such instance landed in
// Unknown, so RoleCheck widened the pipeline to every signal and a restricted
// role dropped it.

// TestDerive_K8sMonitoringChart: the grafana/k8s-monitoring chart renders
// each feature module inline as a declare plus a `<name> "feature"` instance,
// with no import blocks (testdata/k8smon-*.alloy, from `helm template` of
// chart 4.5.5). Both must derive a proven, single-signal set, verbatim:
// including the destination's OTLP bridge (otelcol.exporter.prometheus /
// otelcol.exporter.loki), whose generic otel.any input is narrowed by its
// curated overlay override (override_test.go).
func TestDerive_K8sMonitoringChart(t *testing.T) {
	reg := testRegistry(t)
	for _, tc := range []struct {
		file string
		want []Signal
	}{
		{"k8smon-metrics.alloy", []Signal{Metrics}},
		{"k8smon-logs.alloy", []Signal{Logs}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			got, err := Derive(string(data), reg)
			if err != nil {
				t.Fatalf("Derive: %v", err)
			}
			if !got.Proven() {
				t.Fatalf("not proven: unknown=%+v unclassified=%v", got.Unknown, got.Unclassified)
			}
			if want := NewSet(tc.want...); !got.Combined.Equal(want) {
				t.Fatalf("Combined = %s, want %s", got.Combined, want)
			}
		})
	}
}

// scrapeBody is a metrics-only component body for the module fixtures.
const scrapeBody = `prometheus.scrape "s" {
    targets    = []
    forward_to = []
  }`

func TestDerive_ModuleInstances(t *testing.T) {
	reg := testRegistry(t)
	for _, tc := range []struct {
		name string
		src  string
		// unknown lists the component names Derive must report as Unknown;
		// empty means the result is proven.
		unknown []string
		want    []Signal
	}{
		{
			name: "declared module instance",
			src: `declare "feat" {
  ` + scrapeBody + `
}
feat "feature" { }`,
			want: []Signal{Metrics},
		},
		{
			name: "instance before its declare",
			src: `feat "feature" { }
declare "feat" {
  ` + scrapeBody + `
}`,
			want: []Signal{Metrics},
		},
		{
			name: "nested declare instantiated inside its parent",
			src: `declare "outer" {
  declare "inner" {
    ` + scrapeBody + `
  }
  inner "x" { }
}
outer "y" { }`,
			want: []Signal{Metrics},
		},
		{
			name: "parent-scope declare instantiated inside a nested declare",
			src: `declare "shared" {
  ` + scrapeBody + `
}
declare "outer" {
  shared "x" { }
}
outer "y" { }`,
			want: []Signal{Metrics},
		},
		{
			name: "nested declare is not visible outside its parent",
			src: `declare "outer" {
  declare "inner" {
    ` + scrapeBody + `
  }
}
inner "x" { }`,
			unknown: []string{"inner"},
			want:    []Signal{Metrics},
		},
		{
			name: "a declare in a sibling declare is not visible",
			src: `declare "a" {
  declare "inner" {
    ` + scrapeBody + `
  }
}
declare "b" {
  inner "x" { }
}`,
			unknown: []string{"inner"},
			want:    []Signal{Metrics},
		},
		{
			name: "undeclared name stays unknown",
			src: `declare "feat" {
  ` + scrapeBody + `
}
other "feature" { }`,
			unknown: []string{"other"},
			want:    []Signal{Metrics},
		},
		{
			name: "import.string module instance",
			src: `import.string "mods" {
  content = "declare \"feat\" {\n  prometheus.scrape \"s\" {\n    targets = []\n    forward_to = []\n  }\n}\n"
}
mods.feat "x" { }`,
			want: []Signal{Metrics},
		},
		{
			name: "import.string with a raw-string literal",
			src:  "import.string \"mods\" {\n  content = `declare \"feat\" {\n  loki.write \"w\" { }\n}`\n}\nmods.feat \"x\" { }",
			want: []Signal{Logs},
		},
		{
			name: "import.string module declares instantiate each other by bare name",
			src:  "import.string \"mods\" {\n  content = `declare \"a\" {\n  " + scrapeBody + "\n}\ndeclare \"b\" {\n  a \"x\" { }\n}`\n}\nmods.b \"y\" { }",
			want: []Signal{Metrics},
		},
		{
			name: "import.string inside a declare is visible in that declare",
			src:  "declare \"outer\" {\n  import.string \"mods\" {\n    content = `declare \"feat\" {\n  " + scrapeBody + "\n}`\n  }\n  mods.feat \"x\" { }\n}\nouter \"y\" { }",
			want: []Signal{Metrics},
		},
		{
			name: "nested import.string",
			src:  "import.string \"outer\" {\n  content = " + quoteAlloy("import.string \"inner\" {\n  content = `declare \"feat\" {\n  "+scrapeBody+"\n}`\n}\ndeclare \"wrap\" {\n  inner.feat \"x\" { }\n}\n") + "\n}\nouter.wrap \"y\" { }",
			want: []Signal{Metrics},
		},
		{
			name:    "import.string module not declared in its content",
			src:     "import.string \"mods\" {\n  content = `declare \"feat\" {\n  " + scrapeBody + "\n}`\n}\nmods.other \"x\" { }",
			unknown: []string{"mods.other"},
			want:    []Signal{Metrics},
		},
		{
			name: "import.string content that is not a literal",
			src: `local.file "f" {
  filename = "/m.alloy"
}
import.string "mods" {
  content = local.file.f.content
}
mods.feat "x" { }`,
			unknown: []string{"mods.feat"},
		},
		{
			name:    "import.string content that does not parse",
			src:     "import.string \"mods\" {\n  content = `declare \"feat\" {`\n}\nmods.feat \"x\" { }",
			unknown: []string{"mods.feat"},
		},
		{
			name:    "import.string content nested past the parser's cap",
			src:     "import.string \"mods\" {\n  content = `declare \"feat\" { x = " + strings.Repeat("[", 600) + strings.Repeat("]", 600) + " }`\n}\nmods.feat \"x\" { }",
			unknown: []string{"mods.feat"},
		},
		{
			name: "import.git module instance stays unknown",
			src: `import.git "mods" {
  repository = "https://example.com/mods.git"
  revision   = "main"
  path       = "m.alloy"
}
mods.feat "x" { }`,
			unknown: []string{"mods.feat"},
		},
		{
			name: "import.file module instance stays unknown",
			src: `import.file "mods" {
  filename = "/m.alloy"
}
mods.feat "x" { }`,
			unknown: []string{"mods.feat"},
		},
		{
			name: "import.http module instance stays unknown",
			src: `import.http "mods" {
  url = "https://example.com/m.alloy"
}
mods.feat "x" { }`,
			unknown: []string{"mods.feat"},
		},
		{
			// A declare of the same name as a non-string import's label does
			// not make the import's modules known.
			name: "a declare does not vouch for an import.git module",
			src: `declare "mods" {
  ` + scrapeBody + `
}
import.git "mods" {
  repository = "https://example.com/mods.git"
  revision   = "main"
  path       = "m.alloy"
}
mods.feat "x" { }`,
			unknown: []string{"mods.feat"},
			want:    []Signal{Metrics},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Derive(tc.src, reg)
			if err != nil {
				t.Fatalf("Derive: %v", err)
			}
			var names []string
			for _, u := range got.Unknown {
				names = append(names, u.Component)
			}
			if strings.Join(names, ",") != strings.Join(tc.unknown, ",") {
				t.Fatalf("Unknown = %v, want %v", names, tc.unknown)
			}
			if want := NewSet(tc.want...); !got.Combined.Equal(want) {
				t.Fatalf("Combined = %s, want %s", got.Combined, want)
			}
		})
	}
}

// TestDerive_ImportStringDepthCap: import.string modules are followed at most
// maxModuleDepth deep; a module past the cap is not read, so its instance
// stays Unknown (worst case) instead of being trusted.
func TestDerive_ImportStringDepthCap(t *testing.T) {
	reg := testRegistry(t)
	// module(n) is a module text whose "m" declare instantiates, through n
	// nested import.string levels, a metrics module.
	var module func(n int) string
	module = func(n int) string {
		if n == 0 {
			return "declare \"m\" {\n  " + scrapeBody + "\n}\n"
		}
		return "import.string \"i\" {\n  content = " + quoteAlloy(module(n-1)) + "\n}\ndeclare \"m\" {\n  i.m \"x\" { }\n}\n"
	}
	within := "import.string \"top\" {\n  content = " + quoteAlloy(module(maxModuleDepth-1)) + "\n}\ntop.m \"x\" { }\n"
	got, err := Derive(within, reg)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if !got.Proven() || !got.Has(Metrics) {
		t.Fatalf("within the cap: unknown=%+v combined=%s, want proven metrics", got.Unknown, got.Combined)
	}

	past := "import.string \"top\" {\n  content = " + quoteAlloy(module(maxModuleDepth)) + "\n}\ntop.m \"x\" { }\n"
	got, err = Derive(past, reg)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if got.Proven() {
		t.Fatalf("past the cap: proven with combined=%s, want the innermost instance Unknown", got.Combined)
	}
}

// quoteAlloy renders s as a double-quoted Alloy string literal.
func quoteAlloy(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}
