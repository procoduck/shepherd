package simulate_test

import (
	"encoding/json"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/schema"
	"shepherd/internal/simulate"
	"shepherd/internal/version"
	"shepherd/internal/visual"
)

// mergedShippedSchema is the merged artifact + overlay the run window reads.
func mergedShippedSchema() map[string]any {
	reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
	Expect(err).NotTo(HaveOccurred())
	merged, _, err := reg.Get(version.AlloySchemaVersion)
	Expect(err).NotTo(HaveOccurred())
	return merged
}

func pipelineIntervals(src string) []simulate.RunInterval {
	merged := mergedShippedSchema()
	policy, err := simulate.LoadPolicy(merged)
	Expect(err).NotTo(HaveOccurred())
	got, err := simulate.PipelineIntervals([]byte(src), merged, policy)
	Expect(err).NotTo(HaveOccurred())
	return got
}

var _ = Describe("the sandbox run window (#424)", func() {
	const ceiling = 120 * time.Second

	Describe("ExtendRunWindow", func() {
		scrape60 := simulate.RunInterval{Component: "prometheus.scrape", Label: "x", Attribute: "scrape_interval", Interval: time.Minute}

		It("extends a run to two of the longest intervals and says why", func() {
			w := simulate.ExtendRunWindow(30*time.Second, ceiling, []simulate.RunInterval{
				{Component: "pyroscope.scrape", Label: "p", Attribute: "scrape_interval", Interval: 15 * time.Second},
				scrape60,
			})
			Expect(w.Duration).To(Equal(2 * time.Minute))
			Expect(w.Note).To(Equal(`Extended to 2m so the 60s scrape interval of prometheus.scrape "x" runs twice.`))
		})

		It("leaves a run long enough already alone", func() {
			w := simulate.ExtendRunWindow(30*time.Second, ceiling, []simulate.RunInterval{
				{Component: "prometheus.scrape", Label: "x", Attribute: "scrape_interval", Interval: 15 * time.Second},
			})
			Expect(w).To(Equal(simulate.RunWindow{Duration: 30 * time.Second}))
			Expect(simulate.ExtendRunWindow(30*time.Second, ceiling, nil)).To(Equal(simulate.RunWindow{Duration: 30 * time.Second}))
		})

		It("never shortens what was asked for", func() {
			w := simulate.ExtendRunWindow(100*time.Second, ceiling, []simulate.RunInterval{
				{Component: "prometheus.scrape", Label: "x", Attribute: "scrape_interval", Interval: 20 * time.Second},
			})
			Expect(w).To(Equal(simulate.RunWindow{Duration: 100 * time.Second}))
		})

		It("caps at the maximum run length and says the slowest component may not run twice", func() {
			w := simulate.ExtendRunWindow(30*time.Second, ceiling, []simulate.RunInterval{
				{Component: "remote.http", Label: "cfg", Attribute: "poll_frequency", Interval: 5 * time.Minute},
			})
			Expect(w.Duration).To(Equal(ceiling))
			Expect(w.Note).To(Equal(`Extended to 2m, the maximum run length, for the 5m poll frequency of remote.http "cfg"; ` +
				`it needs 10m to run twice, so it may not run twice in this run.`))
		})

		It("says so when the run is already at the maximum and the slowest component may not run twice", func() {
			w := simulate.ExtendRunWindow(ceiling, ceiling, []simulate.RunInterval{
				{Component: "remote.http", Label: "cfg", Attribute: "poll_frequency", Interval: 5 * time.Minute},
			})
			Expect(w.Duration).To(Equal(ceiling))
			Expect(w.Note).To(Equal(`The run is at the 2m maximum run length; the 5m poll frequency of remote.http "cfg" ` +
				`needs 10m to run twice, so it may not run twice in this run.`))
		})

		It("names a block-nested attribute by its block and says when a non-literal fell back to the default", func() {
			w := simulate.ExtendRunWindow(30*time.Second, ceiling, []simulate.RunInterval{
				{Component: "prometheus.scrape", Label: "x", Attribute: "scrape_interval", Interval: time.Minute, NonLiteral: true},
			})
			Expect(w.Note).To(Equal(`Extended to 2m so the 60s scrape interval of prometheus.scrape "x" runs twice ` +
				`(its value is not a literal, so the schema default 60s is assumed).`))

			w = simulate.ExtendRunWindow(30*time.Second, ceiling, []simulate.RunInterval{
				{Component: "database_observability.mysql", Label: "db", Attribute: "query_details.collect_interval", Interval: 45 * time.Second},
			})
			Expect(w.Note).To(Equal(`Extended to 90s so the 45s query_details collect interval of database_observability.mysql "db" runs twice.`))
		})
	})

	Describe("PipelineIntervals", func() {
		It("takes a literal interval, and the schema default when the attribute is unset", func() {
			got := pipelineIntervals(`
prometheus.scrape "fast" {
  targets = []
  forward_to = []
  scrape_interval = "15s"
}
prometheus.scrape "slow" {
  targets = []
  forward_to = []
}
`)
			Expect(got).To(ConsistOf(
				simulate.RunInterval{Component: "prometheus.scrape", Label: "fast", Attribute: "scrape_interval", Interval: 15 * time.Second},
				simulate.RunInterval{Component: "prometheus.scrape", Label: "slow", Attribute: "scrape_interval", Interval: time.Minute},
			))
		})

		It("falls back to the schema default for a non-literal value and says so", func() {
			got := pipelineIntervals(`
prometheus.scrape "x" {
  targets = []
  forward_to = []
  scrape_interval = sys.env("SCRAPE_INTERVAL")
}
`)
			Expect(got).To(ConsistOf(simulate.RunInterval{
				Component: "prometheus.scrape", Label: "x", Attribute: "scrape_interval", Interval: time.Minute, NonLiteral: true,
			}))
		})

		It("reads an interval in a nested block, the block's default when the block is present, and nothing for an absent block", func() {
			got := pipelineIntervals(`
otelcol.receiver.awscloudwatch "cw" {
  region = "eu-west-1"
  logs {
    poll_interval = "3m"
  }
  output {}
}
prometheus.exporter.cloudwatch "cw" {
  sts_region = "eu-west-1"
}
`)
			Expect(got).To(ConsistOf(
				simulate.RunInterval{Component: "otelcol.receiver.awscloudwatch", Label: "cw", Attribute: "logs.poll_interval", Interval: 3 * time.Minute},
			))
		})

		It("ignores intervals the classification excludes, and components the sandbox replaces with a stub", func() {
			got := pipelineIntervals(`
discovery.file "f" {
  files = ["/tmp/x.json"]
}
otelcol.exporter.otlp "o" {
  client {
    endpoint = "localhost:4317"
  }
  retry_on_failure {
    max_interval = "10m"
  }
}
`)
			Expect(got).To(BeEmpty())
		})

		It("counts a connector's flush interval and a remote poll frequency", func() {
			got := pipelineIntervals(`
otelcol.connector.spanmetrics "s" {
  histogram {
    explicit {}
  }
  output {}
}
remote.http "cfg" {
  url = "http://localhost:1/x"
  poll_frequency = "5m"
}
`)
			Expect(got).To(ConsistOf(
				simulate.RunInterval{Component: "otelcol.connector.spanmetrics", Label: "s", Attribute: "metrics_flush_interval", Interval: time.Minute},
				simulate.RunInterval{Component: "remote.http", Label: "cfg", Attribute: "poll_frequency", Interval: 5 * time.Minute},
			))
		})

		It("reads components inside a declare", func() {
			got := pipelineIntervals(`
declare "inner" {
  prometheus.scrape "x" {
    targets = []
    forward_to = []
    scrape_interval = "45s"
  }
}
`)
			Expect(got).To(ConsistOf(simulate.RunInterval{Component: "prometheus.scrape", Label: "x", Attribute: "scrape_interval", Interval: 45 * time.Second}))
		})
	})

	It("RunWindowFor renders the graph and extends a default-interval scrape run to 2m", func() {
		doc := visual.GraphDocument{
			Kind: "alloy-graph/v1", SchemaVersion: version.AlloySchemaVersion,
			Nodes: []visual.GraphNode{{ID: "n1", Component: "prometheus.scrape", Label: "x", Props: map[string]any{
				"targets": []any{}, "forward_to": []any{},
			}}},
		}
		w, err := simulate.RunWindowFor(doc, mergedShippedSchema(), 30*time.Second, ceiling)
		Expect(err).NotTo(HaveOccurred())
		Expect(w.Duration).To(Equal(2 * time.Minute))
		Expect(w.Note).To(Equal(`Extended to 2m so the 60s scrape interval of prometheus.scrape "x" runs twice.`))
	})
})

// intervalShaped is what the classification must cover: every duration
// attribute whose name ends in interval, frequency or period.
var intervalShaped = regexp.MustCompile(`(interval|frequency|period)$`)

type shippedDurationAttr struct {
	component string
	path      []string
}

func shippedIntervalAttributes() []shippedDurationAttr {
	var out []shippedDurationAttr
	seen := map[string]bool{}
	entries, err := fs.ReadDir(schema.Embedded, "artifacts")
	Expect(err).NotTo(HaveOccurred())
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "alloy-v") || path.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := fs.ReadFile(schema.Embedded, "artifacts/"+e.Name())
		Expect(err).NotTo(HaveOccurred())
		var art struct {
			Components map[string]json.RawMessage `json:"components"`
		}
		Expect(json.Unmarshal(raw, &art)).To(Succeed())
		for name, body := range art.Components {
			var node schemaNode
			Expect(json.Unmarshal(body, &node)).To(Succeed())
			var walk func(n schemaNode, prefix []string)
			walk = func(n schemaNode, prefix []string) {
				for _, a := range n.Attributes {
					if a.Type != "duration" || !intervalShaped.MatchString(a.Name) {
						continue
					}
					p := append(append([]string{}, prefix...), a.Name)
					key := name + " " + strings.Join(p, ".")
					if !seen[key] {
						seen[key] = true
						out = append(out, shippedDurationAttr{component: name, path: p})
					}
				}
				for _, b := range n.Blocks {
					walk(b, append(append([]string{}, prefix...), b.Name))
				}
			}
			walk(node, nil)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].component+strings.Join(out[i].path, ".") < out[j].component+strings.Join(out[j].path, ".")
	})
	return out
}

type schemaNode struct {
	Name       string `json:"name"`
	Attributes []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"attributes"`
	Blocks []schemaNode `json:"blocks"`
}

var _ = Describe("the run window's interval classification (#424)", func() {
	It("classifies every interval-shaped duration attribute of every shipped schema as counted or excluded, with a reason", func() {
		var missing []string
		for _, a := range shippedIntervalAttributes() {
			c, ok := simulate.ClassifyIntervalAttribute(a.component, a.path)
			if !ok {
				missing = append(missing, a.component+" "+strings.Join(a.path, "."))
				continue
			}
			Expect(c.Reason).NotTo(BeEmpty(), "%s %s has no reason", a.component, strings.Join(a.path, "."))
		}
		Expect(missing).To(BeEmpty(), "add these to internal/simulate/runintervals.go as counted or excluded, with a reason")
	})

	It("has no classification entry that no shipped schema attribute uses", func() {
		used := map[string]bool{}
		for _, a := range shippedIntervalAttributes() {
			if c, ok := simulate.ClassifyIntervalAttribute(a.component, a.path); ok {
				used[c.Key] = true
			}
		}
		var stale []string
		for _, k := range simulate.IntervalClassificationKeys() {
			if !used[k] {
				stale = append(stale, k)
			}
		}
		Expect(stale).To(BeEmpty())
	})
})
