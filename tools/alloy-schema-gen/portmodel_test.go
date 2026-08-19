package main

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("port model (decision D1)", func() {
	DescribeTable("RoleFor derives the dataflow direction from the wire kind",
		func(kind WireKind, export bool, want string) {
			Expect(RoleFor(kind, export)).To(Equal(want))
		},
		// discovery.kubernetes exports `targets` (DATA): data leaves there.
		Entry("data export -> produces", WireKindData, true, RoleProduces),
		// prometheus.scrape takes `targets` (DATA): data enters there.
		Entry("data argument -> accepts", WireKindData, false, RoleAccepts),
		// prometheus.remote_write exports `receiver` (RECEIVER): data enters there,
		// even though it is an export — this is the inversion D1 hides.
		Entry("receiver export -> accepts", WireKindReceiver, true, RoleAccepts),
		// prometheus.scrape takes `forward_to` (RECEIVER list): data leaves there.
		Entry("receiver argument -> produces", WireKindReceiver, false, RoleProduces),
	)

	It("classifies every mapped Go type", func() {
		Expect(GoTypeWireMap).To(HaveLen(5))
		for goType, wt := range GoTypeWireMap {
			Expect(wt.Wire).NotTo(BeEmpty(), "%s must map to a wire type", goType)
			Expect([]WireKind{WireKindData, WireKindReceiver}).To(ContainElement(wt.Kind),
				"%s must be data or receiver", goType)
		}
		Expect(GoTypeWireMap["discovery.Target"].Kind).To(Equal(WireKindData))
		for _, recv := range []string{"storage.Appendable", "loki.LogsReceiver", "otelcol.Consumer", "pyroscope.Appendable"} {
			Expect(GoTypeWireMap[recv].Kind).To(Equal(WireKindReceiver), "%s is a receiver handle", recv)
		}
	})

	It("excludes non-port capsules that look wire-ish", func() {
		Expect(NonPortGoTypes).To(HaveKey("vcs.GitRepo"))
		Expect(GoTypeWireMap).NotTo(HaveKey("vcs.GitRepo"))
	})

	DescribeTable("RefineOtelWire narrows otel.any only on signal-specific fields",
		func(wire, field, want string) {
			Expect(RefineOtelWire(wire, field)).To(Equal(want))
		},
		Entry("output.metrics", "otel.any", "metrics", "otel.metrics"),
		Entry("output.logs", "otel.any", "logs", "otel.logs"),
		Entry("output.traces", "otel.any", "traces", "otel.traces"),
		Entry("ConsumerExports.Input stays polymorphic", "otel.any", "input", "otel.any"),
		Entry("unknown field stays polymorphic", "otel.any", "whatever", "otel.any"),
		Entry("non-otel wire is never rewritten", "loki.logs", "logs", "loki.logs"),
		Entry("targets is never rewritten", "targets", "metrics", "targets"),
	)

	DescribeTable("PortName is the dotted block path",
		func(path []string, want string) {
			Expect(PortName(path)).To(Equal(want))
		},
		Entry("top-level attribute", []string{"forward_to"}, "forward_to"),
		Entry("consumer inside a block", []string{"output", "metrics"}, "output.metrics"),
		Entry("empty path", []string{}, ""),
	)
})
