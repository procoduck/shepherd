package main

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestReconcile(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Alloy Schema Reconcile Suite")
}

var _ = Describe("categorize", func() {
	DescribeTable("heuristic category from component path (§C3)",
		func(name, want string) {
			Expect(categorize(name)).To(Equal(want))
		},
		Entry("discovery.* is a source", "discovery.kubernetes", "sources"),
		Entry("*.exporter.* is a source", "prometheus.exporter.redis", "sources"),
		Entry("*.source.* is a source", "loki.source.file", "sources"),
		Entry("discovery.relabel is a transform, not a source (priority)", "discovery.relabel", "transform"),
		Entry("*.relabel is a transform", "prometheus.relabel", "transform"),
		Entry("*.process* is a transform", "otelcol.processor.batch", "transform"),
		Entry("*.remote_write is a destination", "prometheus.remote_write", "destinations"),
		Entry("*.write is a destination", "loki.write", "destinations"),
		Entry("otelcol.exporter.* is a destination, not a source (priority)", "otelcol.exporter.otlp", "destinations"),
		Entry("remote.* is config", "remote.http", "config"),
		Entry("local.* is config", "local.file", "config"),
		Entry("unrecognized falls back to advanced", "beyla.ebpf", "advanced"),
	)
})

var _ = Describe("reconcileComponents", func() {
	It("appends a needs_review skeleton for a component missing from the overlay", func() {
		artifact := map[string]any{
			"discovery.kubernetes": map[string]any{},
		}
		overlay := map[string]any{}

		added, removed := reconcileComponents(artifact, overlay)

		Expect(added).To(ConsistOf("discovery.kubernetes"))
		Expect(removed).To(BeEmpty())

		entry, ok := overlay["discovery.kubernetes"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(entry).To(Equal(map[string]any{
			"category":     "sources",
			"doc":          "",
			"needs_review": true,
		}))
	})

	It("deletes an overlay entry whose component vanished from the artifact", func() {
		artifact := map[string]any{}
		overlay := map[string]any{
			"discovery.gone": map[string]any{"category": "sources", "doc": "old"},
		}

		added, removed := reconcileComponents(artifact, overlay)

		Expect(added).To(BeEmpty())
		Expect(removed).To(ConsistOf("discovery.gone"))
		Expect(overlay).NotTo(HaveKey("discovery.gone"))
	})

	It("never touches editorial fields on a surviving entry", func() {
		artifact := map[string]any{
			"discovery.kubernetes": map[string]any{},
		}
		overlay := map[string]any{
			"discovery.kubernetes": map[string]any{
				"category": "sources",
				"doc":      "hand-written doc, do not overwrite",
				"icon":     "k8s",
			},
		}

		added, removed := reconcileComponents(artifact, overlay)

		Expect(added).To(BeEmpty())
		Expect(removed).To(BeEmpty())
		Expect(overlay["discovery.kubernetes"]).To(Equal(map[string]any{
			"category": "sources",
			"doc":      "hand-written doc, do not overwrite",
			"icon":     "k8s",
		}))
	})

	It("no-ops when artifact and overlay already match (the common case)", func() {
		artifact := map[string]any{"discovery.kubernetes": map[string]any{}}
		overlay := map[string]any{"discovery.kubernetes": map[string]any{"category": "sources"}}

		added, removed := reconcileComponents(artifact, overlay)

		Expect(added).To(BeEmpty())
		Expect(removed).To(BeEmpty())
		Expect(overlay["discovery.kubernetes"]).To(Equal(map[string]any{"category": "sources"}))
	})

	It("adds and removes in the same pass when both drift", func() {
		artifact := map[string]any{
			"discovery.kubernetes": map[string]any{},
			"discovery.new_thing":  map[string]any{},
		}
		overlay := map[string]any{
			"discovery.kubernetes": map[string]any{"category": "sources"},
			"discovery.gone":       map[string]any{"category": "sources"},
		}

		added, removed := reconcileComponents(artifact, overlay)

		Expect(added).To(ConsistOf("discovery.new_thing"))
		Expect(removed).To(ConsistOf("discovery.gone"))
		Expect(overlay).To(HaveKey("discovery.kubernetes"))
		Expect(overlay).To(HaveKey("discovery.new_thing"))
		Expect(overlay).NotTo(HaveKey("discovery.gone"))
	})
})

var _ = Describe("escapeNonASCII", func() {
	It("escapes a non-ASCII rune as \\uXXXX, matching the committed overlay.json style", func() {
		got := escapeNonASCII([]byte("an em\u2014dash"))
		Expect(string(got)).To(Equal(`an em\u2014dash`))
	})

	It("leaves ASCII content untouched", func() {
		got := escapeNonASCII([]byte(`{"a": "b"}`))
		Expect(string(got)).To(Equal(`{"a": "b"}`))
	})
})
