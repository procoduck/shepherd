package visual_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/visual"
)

// Every spec here runs against the shipped schema (corpusSchema), so a change
// to the artifact's port model reaches these specs.
var _ = Describe("CheckPortShapes (#233)", func() {
	check := func(content string) []visual.PortShapeDiagnostic {
		return visual.CheckPortShapes(content, corpusSchema())
	}

	const sinks = `
prometheus.remote_write "sink" {
  endpoint {
    url = "https://prom.example.com/api/v1/push"
  }
}

discovery.kubernetes "pods" {
  role = "pod"
}

discovery.relabel "r" {
  targets = discovery.kubernetes.pods.targets
}
`

	Describe("rule 1: a targets export inside a list literal", func() {
		It("flags the issue's reproduction at the element", func() {
			ds := check(sinks + `
prometheus.scrape "demo" {
  targets    = [discovery.kubernetes.pods.targets]
  forward_to = [prometheus.remote_write.sink.receiver]
}
`)
			Expect(ds).To(HaveLen(1))
			Expect(ds[0].Line).To(Equal(17))
			Expect(ds[0].Col).To(Equal(17))
			Expect(ds[0].Message).To(Equal("targets expects a list of targets; [discovery.kubernetes.pods.targets] is a list of lists, " +
				"because discovery.kubernetes.pods.targets is already a list of targets — drop the brackets (use array.concat(a, b) to combine several)"))
		})

		It("flags each wrapped export in a multi-element list, and on every targets-typed port", func() {
			ds := check(sinks + `
loki.source.file "f" {
  targets    = [discovery.kubernetes.pods.targets, discovery.relabel.r.output]
  forward_to = []
}

discovery.relabel "again" {
  targets = [discovery.relabel.r.output]
}
`)
			Expect(ds).To(HaveLen(3))
		})

		It("flags a wrapped export mixed with literal targets", func() {
			ds := check(sinks + `
prometheus.scrape "demo" {
  targets    = [{"__address__" = "localhost:9090"}, discovery.relabel.r.output]
  forward_to = [prometheus.remote_write.sink.receiver]
}
`)
			Expect(ds).To(HaveLen(1))
		})

		It("flags it inside a pipeline's declare wrapper and inside a foreach template", func() {
			ds := check(`declare "pipe_x" {
  discovery.kubernetes "pods" {
    role = "pod"
  }
  prometheus.scrape "demo" {
    targets    = [discovery.kubernetes.pods.targets]
    forward_to = []
  }
  foreach "each_ns" {
    collection = ["a"]
    var        = "ns"
    template {
      prometheus.scrape "inner" {
        targets    = [discovery.kubernetes.pods.targets]
        forward_to = []
      }
    }
  }
}
pipe_x "default" { }
`)
			Expect(ds).To(HaveLen(2))
		})
	})

	Describe("rule 2: a single receiver where a list is required", func() {
		It("flags a bare receiver on forward_to", func() {
			ds := check(sinks + `
prometheus.scrape "demo" {
  targets    = discovery.kubernetes.pods.targets
  forward_to = prometheus.remote_write.sink.receiver
}
`)
			Expect(ds).To(HaveLen(1))
			Expect(ds[0].Message).To(Equal("forward_to expects a list of receivers; prometheus.remote_write.sink.receiver is a single receiver — " +
				"wrap it in brackets: [prometheus.remote_write.sink.receiver]"))
		})

		It("flags a bare consumer on a port nested in an output block", func() {
			ds := check(`
otelcol.receiver.otlp "in" {
  http { }
  output {
    metrics = otelcol.exporter.otlp.out.input
  }
}

otelcol.exporter.otlp "out" {
  client {
    endpoint = "tempo:4317"
  }
}
`)
			Expect(ds).To(HaveLen(1))
			Expect(ds[0].Message).To(HavePrefix("output.metrics expects a list of receivers"))
		})
	})

	Describe("everything it cannot type precisely is left alone", func() {
		DescribeTable("no diagnostic",
			func(body string) {
				Expect(check(sinks + body)).To(BeEmpty())
			},
			Entry("the correct bare targets reference", `
prometheus.scrape "demo" {
  targets    = discovery.kubernetes.pods.targets
  forward_to = [prometheus.remote_write.sink.receiver]
}`),
			Entry("array.concat of several targets exports", `
prometheus.scrape "demo" {
  targets    = array.concat(discovery.kubernetes.pods.targets, discovery.relabel.r.output)
  forward_to = [prometheus.remote_write.sink.receiver]
}`),
			Entry("a list of literal targets", `
prometheus.scrape "demo" {
  targets    = [{"__address__" = "localhost:9090"}]
  forward_to = []
}`),
			Entry("an indexed element of a targets export", `
prometheus.scrape "demo" {
  targets    = [discovery.kubernetes.pods.targets[0]]
  forward_to = []
}`),
			Entry("a reference to a component block that is not declared", `
prometheus.scrape "demo" {
  targets    = [discovery.kubernetes.elsewhere.targets]
  forward_to = prometheus.remote_write.elsewhere.receiver
}`),
			Entry("a module argument", `
prometheus.scrape "demo" {
  targets    = [argument.targets.value]
  forward_to = argument.forward_to.value
}`),
			Entry("an unknown export name", `
prometheus.scrape "demo" {
  targets    = [discovery.kubernetes.pods.nonexistent]
  forward_to = []
}`),
			Entry("a non-port attribute", `
prometheus.scrape "demo" {
  targets    = []
  forward_to = []
  scrape_protocols = [discovery.kubernetes.pods.targets]
}`),
			Entry("an otel.any consumer on a signal-specific port (polymorphic, not a type error)", `
otelcol.receiver.otlp "in" {
  http { }
  output {
    logs = [otelcol.processor.batch.b.input]
  }
}
otelcol.processor.batch "b" {
  output { }
}`),
			Entry("a receiver already in brackets", `
prometheus.scrape "demo" {
  targets    = []
  forward_to = [prometheus.remote_write.sink.receiver]
}`),
		)

		It("skips references through an imported module namespace", func() {
			Expect(check(`
import.file "discovery" {
  filename = "/etc/alloy/modules"
}

discovery.kubernetes "pods" {
  role = "pod"
}

prometheus.scrape "demo" {
  targets    = [discovery.kubernetes.pods.targets]
  forward_to = []
}
`)).To(BeEmpty())
		})

		It("does not let a declare body see components declared outside it", func() {
			Expect(check(`
discovery.kubernetes "pods" {
  role = "pod"
}

declare "inner" {
  prometheus.scrape "demo" {
    targets    = [discovery.kubernetes.pods.targets]
    forward_to = []
  }
}
`)).To(BeEmpty())
		})

		It("returns nothing for content that does not parse (Stage 1 owns it)", func() {
			Expect(check(`prometheus.scrape "demo" { targets = [`)).To(BeEmpty())
		})
	})

	// The two rules rest on facts about the shipped artifact's port model that
	// the extractor does not record directly (exports carry no cardinality).
	// They were checked against grafana/alloy's source at the pinned tag: every
	// discovery.Target export is a []discovery.Target, every receiver export is
	// a single capsule. This pins the schema-side half so an artifact bump that
	// changes the model fails here instead of silently changing what the check
	// means.
	It("rests on a port model the shipped artifact still has", func() {
		receiverTypes := map[string]bool{"prom.metrics": true, "loki.logs": true, "otel.any": true, "pyroscope.profiles": true}
		for name, comp := range corpusSchema().Components {
			for _, out := range comp.Outputs {
				switch out.Role {
				case "produces":
					Expect(out.Type).To(Equal("targets"), "%s.%s: a produces-role export that is not targets", name, out.Export)
				case "accepts":
					Expect(receiverTypes).To(HaveKey(out.Type), "%s.%s: an accepts-role export that is not a receiver", name, out.Export)
				default:
					Fail(name + "." + out.Export + ": export with role " + out.Role)
				}
			}
			for _, in := range comp.Inputs {
				Expect(in.Cardinality).To(Equal("list"), "%s.%s", name, in.Prop)
			}
		}
	})
})
