package merge_test

import (
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/merge"
	"shepherd/internal/schema"
	"shepherd/internal/version"
)

// testRegistry builds a *schema.Registry against the real, embedded schema
// artifact — the same construction internal/mgmtapi and internal/server use
// in production — so role-enforcement tests exercise Assemble against the
// artifact a real build actually ships, not a hand-rolled stub. Mirrors
// internal/signals/wiretype_test.go's helper of the same name.
func testRegistry() *schema.Registry {
	reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
	if err != nil {
		panic(err)
	}
	return reg
}

// metricsOnlyPipeline is a minimal, genuinely metrics-shaped Alloy pipeline:
// prometheus.scrape -> prometheus.remote_write, both prom.metrics.
const metricsOnlyPipeline = `
prometheus.scrape "app" {
  forward_to = [prometheus.remote_write.sink.receiver]
}

prometheus.remote_write "sink" {
  endpoint {
    url = "https://example.com/write"
  }
}
`

// logsOnlyPipeline is a minimal, genuinely logs-shaped Alloy pipeline:
// loki.source.file -> loki.write, both loki.logs.
const logsOnlyPipeline = `
loki.source.file "pods" {
  targets    = []
  forward_to = [loki.write.cloud.receiver]
}

loki.write "cloud" {
  endpoint {
    url = "https://loki.example.com/loki/api/v1/push"
  }
}
`

func TestMerge(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Merge Suite")
}

var _ = Describe("SanitizeName", func() {
	DescribeTable("converts names to valid identifiers",
		func(input, expected string) {
			Expect(merge.SanitizeName(input)).To(Equal(expected))
		},
		Entry("simple name", "mypipeline", "mypipeline"),
		Entry("dashes to underscores", "my-pipeline", "my_pipeline"),
		Entry("dots to underscores", "my.pipeline", "my_pipeline"),
		Entry("uppercase lowercased", "MyPipeline", "mypipeline"),
		Entry("starts with digit gets prefix", "1pipeline", "p1pipeline"),
		Entry("collision: a-b and a_b both become a_b", "a-b", "a_b"),
		Entry("spaces to underscores", "my pipeline", "my_pipeline"),
	)
})

var _ = Describe("MatchesPipeline", func() {
	cl := merge.CollectorLabels{
		CollectorID: "coll-uuid-1",
		Labels: map[string]string{
			"cluster": "prod-eu-1",
			"role":    "metrics",
			"env":     "prod",
		},
	}

	DescribeTable("matcher evaluation",
		func(matchers []string, source, repoCollID string, expectedMatch bool) {
			p := merge.Pipeline{
				Name:                "test",
				Matchers:            matchers,
				Source:              source,
				RepoLinkCollectorID: repoCollID,
			}
			matched, err := merge.MatchesPipeline(p, cl)
			Expect(err).NotTo(HaveOccurred())
			Expect(matched).To(Equal(expectedMatch))
		},
		Entry("equal match hits", []string{`cluster="prod-eu-1"`}, "ui", "", true),
		Entry("equal match misses", []string{`cluster="prod-eu-2"`}, "ui", "", false),
		Entry("not-equal match hits (value differs)", []string{`role!="logs"`}, "ui", "", true),
		Entry("not-equal match misses (value equals)", []string{`role!="metrics"`}, "ui", "", false),
		Entry("regex match hits", []string{`cluster=~"prod-.*"`}, "ui", "", true),
		Entry("regex match misses", []string{`cluster=~"staging-.*"`}, "ui", "", false),
		Entry("negative regex hits (not staging)", []string{`env!~"staging.*"`}, "ui", "", true),
		Entry("negative regex misses (is prod, matches regex)", []string{`env!~"prod.*"`}, "ui", "", false),
		Entry("multiple matchers all match (AND)", []string{`cluster="prod-eu-1"`, `role="metrics"`}, "ui", "", true),
		Entry("multiple matchers one fails (AND)", []string{`cluster="prod-eu-1"`, `role="logs"`}, "ui", "", false),
		Entry("zero matchers match nothing (safety default)", []string{}, "ui", "", false),
		Entry("git pipeline matches by collector ID", nil, "git", "coll-uuid-1", true),
		Entry("git pipeline misses wrong collector ID", nil, "git", "coll-uuid-2", false),
	)
})

var _ = Describe("Assemble", func() {
	cl := merge.CollectorLabels{
		CollectorID: "coll-uuid-1",
		Labels:      map[string]string{"cluster": "test", "role": "metrics"},
	}

	It("produces deterministic output for same inputs", func() {
		pipelines := []merge.Pipeline{
			{Name: "alpha", Contents: "// alpha content", Matchers: []string{`cluster="test"`}, Source: "ui"},
			{Name: "beta", Contents: "// beta content", Matchers: []string{`role="metrics"`}, Source: "ui"},
		}
		r1, err := merge.Assemble("coll-uuid-1", "test/metrics", cl, pipelines, "dev", "2024-01-01T00:00:00Z")
		Expect(err).NotTo(HaveOccurred())
		r2, err := merge.Assemble("coll-uuid-1", "test/metrics", cl, pipelines, "dev", "2024-01-01T00:00:00Z")
		Expect(err).NotTo(HaveOccurred())
		Expect(r1.Content).To(Equal(r2.Content))
		Expect(r1.Hash).To(Equal(r2.Hash))
	})

	It("sorts pipelines by name for determinism", func() {
		pipelines := []merge.Pipeline{
			{Name: "z-pipe", Contents: "// z", Matchers: []string{`cluster="test"`}, Source: "ui"},
			{Name: "a-pipe", Contents: "// a", Matchers: []string{`cluster="test"`}, Source: "ui"},
		}
		r, err := merge.Assemble("coll-uuid-1", "test/metrics", cl, pipelines, "dev", "2024-01-01T00:00:00Z")
		Expect(err).NotTo(HaveOccurred())
		aIdx := strings.Index(r.Content, "pipe_a_pipe")
		zIdx := strings.Index(r.Content, "pipe_z_pipe")
		Expect(aIdx).To(BeNumerically("<", zIdx))
	})

	It("wraps each pipeline in a declare block with instantiation", func() {
		pipelines := []merge.Pipeline{
			{Name: "mypipe", Contents: "// content", Matchers: []string{`cluster="test"`}, Source: "ui"},
		}
		r, err := merge.Assemble("coll-uuid-1", "test/metrics", cl, pipelines, "dev", "2024-01-01T00:00:00Z")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Content).To(ContainSubstring(`declare "pipe_mypipe"`))
		Expect(r.Content).To(ContainSubstring(`pipe_mypipe "default" { }`))
	})

	It("hash equals sha256hex of content", func() {
		pipelines := []merge.Pipeline{
			{Name: "p", Contents: "x", Matchers: []string{`cluster="test"`}, Source: "ui"},
		}
		r, err := merge.Assemble("coll-uuid-1", "test/metrics", cl, pipelines, "dev", "2024-01-01T00:00:00Z")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Hash).To(Equal(merge.HashContent(r.Content)))
	})

	It("excludes unmatched pipelines", func() {
		pipelines := []merge.Pipeline{
			{Name: "matched", Contents: "// yes", Matchers: []string{`cluster="test"`}, Source: "ui"},
			{Name: "unmatched", Contents: "// no", Matchers: []string{`cluster="other"`}, Source: "ui"},
		}
		r, err := merge.Assemble("coll-uuid-1", "test/metrics", cl, pipelines, "dev", "2024-01-01T00:00:00Z")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Content).To(ContainSubstring("pipe_matched"))
		Expect(r.Content).NotTo(ContainSubstring("pipe_unmatched"))
	})

	It("git pipeline matches by collector ID ignoring matchers", func() {
		pipelines := []merge.Pipeline{
			{Name: "git-pipe", Contents: "// git", Source: "git", RepoLinkCollectorID: "coll-uuid-1"},
			{Name: "other-git", Contents: "// other", Source: "git", RepoLinkCollectorID: "coll-uuid-99"},
		}
		r, err := merge.Assemble("coll-uuid-1", "test/metrics", cl, pipelines, "dev", "2024-01-01T00:00:00Z")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Content).To(ContainSubstring("pipe_git_pipe"))
		Expect(r.Content).NotTo(ContainSubstring("pipe_other_git"))
	})
})

var _ = Describe("role enforcement (WithRoleEnforcement, gate G6)", func() {
	reg := testRegistry()

	It("does not filter anything when the option is omitted (pre-W1 behavior preserved)", func() {
		cl := merge.CollectorLabels{
			CollectorID: "coll-uuid-1",
			Labels:      map[string]string{"cluster": "test", "role": "logs"},
		}
		pipelines := []merge.Pipeline{
			{Name: "metrics-pipe", Contents: metricsOnlyPipeline, Matchers: []string{`cluster="test"`}, Source: "ui"},
		}
		r, err := merge.Assemble("coll-uuid-1", "test/logs", cl, pipelines, "dev", "2024-01-01T00:00:00Z")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Content).To(ContainSubstring("pipe_metrics_pipe"))
		Expect(r.Exclusions).To(BeEmpty())
	})

	It("excludes a metrics pipeline from a role=logs collector", func() {
		cl := merge.CollectorLabels{
			CollectorID: "coll-uuid-1",
			Labels:      map[string]string{"cluster": "test", "role": "logs"},
		}
		pipelines := []merge.Pipeline{
			{Name: "metrics-pipe", Contents: metricsOnlyPipeline, Matchers: []string{`cluster="test"`}, Source: "ui"},
		}
		r, err := merge.Assemble("coll-uuid-1", "test/logs", cl, pipelines, "dev", "2024-01-01T00:00:00Z", merge.WithRoleEnforcement(reg))
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Content).NotTo(ContainSubstring("pipe_metrics_pipe"))
	})

	It("includes the same metrics pipeline for a role=metrics collector", func() {
		cl := merge.CollectorLabels{
			CollectorID: "coll-uuid-1",
			Labels:      map[string]string{"cluster": "test", "role": "metrics"},
		}
		pipelines := []merge.Pipeline{
			{Name: "metrics-pipe", Contents: metricsOnlyPipeline, Matchers: []string{`cluster="test"`}, Source: "ui"},
		}
		r, err := merge.Assemble("coll-uuid-1", "test/metrics", cl, pipelines, "dev", "2024-01-01T00:00:00Z", merge.WithRoleEnforcement(reg))
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Content).To(ContainSubstring("pipe_metrics_pipe"))
		Expect(r.Exclusions).To(BeEmpty())
	})

	It("reports the exclusion in AssembleResult.Exclusions and in the header comment", func() {
		cl := merge.CollectorLabels{
			CollectorID: "coll-uuid-1",
			Labels:      map[string]string{"cluster": "test", "role": "logs"},
		}
		pipelines := []merge.Pipeline{
			{Name: "metrics-pipe", Contents: metricsOnlyPipeline, Matchers: []string{`cluster="test"`}, Source: "ui"},
		}
		r, err := merge.Assemble("coll-uuid-1", "test/logs", cl, pipelines, "dev", "2024-01-01T00:00:00Z", merge.WithRoleEnforcement(reg))
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Exclusions).To(HaveLen(1))
		Expect(r.Exclusions[0].PipelineName).To(Equal("metrics-pipe"))
		Expect(r.Exclusions[0].Reason).To(ContainSubstring("metrics"))
		Expect(r.Content).To(ContainSubstring("// Excluded (1)"))
		Expect(r.Content).To(ContainSubstring("metrics-pipe:"))
	})

	It("keeps a compliant pipeline alongside an excluded one, without failing the whole assembly", func() {
		cl := merge.CollectorLabels{
			CollectorID: "coll-uuid-1",
			Labels:      map[string]string{"cluster": "test", "role": "logs"},
		}
		pipelines := []merge.Pipeline{
			{Name: "metrics-pipe", Contents: metricsOnlyPipeline, Matchers: []string{`cluster="test"`}, Source: "ui"},
			{Name: "logs-pipe", Contents: logsOnlyPipeline, Matchers: []string{`cluster="test"`}, Source: "ui"},
		}
		r, err := merge.Assemble("coll-uuid-1", "test/logs", cl, pipelines, "dev", "2024-01-01T00:00:00Z", merge.WithRoleEnforcement(reg))
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Content).To(ContainSubstring("pipe_logs_pipe"))
		Expect(r.Content).NotTo(ContainSubstring("pipe_metrics_pipe"))
		Expect(r.Exclusions).To(HaveLen(1))
		Expect(r.Exclusions[0].PipelineName).To(Equal("metrics-pipe"))
	})

	It("is unrestricted for role=singleton: a pipeline mixing metrics and logs is kept", func() {
		cl := merge.CollectorLabels{
			CollectorID: "coll-uuid-1",
			Labels:      map[string]string{"cluster": "test", "role": "singleton"},
		}
		mixed := metricsOnlyPipeline + "\n" + logsOnlyPipeline
		pipelines := []merge.Pipeline{
			{Name: "mixed-pipe", Contents: mixed, Matchers: []string{`cluster="test"`}, Source: "ui"},
		}
		r, err := merge.Assemble("coll-uuid-1", "test/singleton", cl, pipelines, "dev", "2024-01-01T00:00:00Z", merge.WithRoleEnforcement(reg))
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Content).To(ContainSubstring("pipe_mixed_pipe"))
		Expect(r.Exclusions).To(BeEmpty())
	})

	It("excludes fail-safe on an unrecognized role rather than passing the pipeline through", func() {
		cl := merge.CollectorLabels{
			CollectorID: "coll-uuid-1",
			Labels:      map[string]string{"cluster": "test", "role": "not-a-real-role"},
		}
		pipelines := []merge.Pipeline{
			{Name: "metrics-pipe", Contents: metricsOnlyPipeline, Matchers: []string{`cluster="test"`}, Source: "ui"},
		}
		r, err := merge.Assemble("coll-uuid-1", "test/not-a-real-role", cl, pipelines, "dev", "2024-01-01T00:00:00Z", merge.WithRoleEnforcement(reg))
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Content).NotTo(ContainSubstring("pipe_metrics_pipe"))
		Expect(r.Exclusions).To(HaveLen(1))
	})

	It("treats an unknown component as worst-case for a restrictive role, excluding it", func() {
		cl := merge.CollectorLabels{
			CollectorID: "coll-uuid-1",
			Labels:      map[string]string{"cluster": "test", "role": "metrics"},
		}
		withUnknown := `
totally.bogus.component "x" {
  foo = "bar"
}
` + metricsOnlyPipeline
		pipelines := []merge.Pipeline{
			{Name: "mystery-pipe", Contents: withUnknown, Matchers: []string{`cluster="test"`}, Source: "ui"},
		}
		r, err := merge.Assemble("coll-uuid-1", "test/metrics", cl, pipelines, "dev", "2024-01-01T00:00:00Z", merge.WithRoleEnforcement(reg))
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Content).NotTo(ContainSubstring("pipe_mystery_pipe"))
		Expect(r.Exclusions).To(HaveLen(1))
		Expect(r.Exclusions[0].Reason).To(ContainSubstring("not provable"))
	})

	It("still lists an exclusion in the header even when it is the only pipeline that matched", func() {
		cl := merge.CollectorLabels{
			CollectorID: "coll-uuid-1",
			Labels:      map[string]string{"cluster": "test", "role": "logs"},
		}
		pipelines := []merge.Pipeline{
			{Name: "metrics-pipe", Contents: metricsOnlyPipeline, Matchers: []string{`cluster="test"`}, Source: "ui"},
		}
		r, err := merge.Assemble("coll-uuid-1", "test/logs", cl, pipelines, "dev", "2024-01-01T00:00:00Z", merge.WithRoleEnforcement(reg))
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Content).To(ContainSubstring("// No pipelines matched"))
		Expect(r.Content).To(ContainSubstring("// Excluded (1)"))
		Expect(r.Content).To(ContainSubstring("metrics-pipe:"))
	})
})

var _ = Describe("HashContent", func() {
	It("empty string produces known sha256", func() {
		Expect(merge.HashContent("")).To(Equal("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"))
	})
})

var _ = Describe("assembly errors on duplicate sanitized names", func() {
	It("returns error when two pipelines sanitize to the same block name", func() {
		cl := merge.CollectorLabels{
			CollectorID: "coll-1",
			Labels:      map[string]string{"cluster": "test", "role": "metrics"},
		}
		// "a-b" and "a_b" both sanitize to "a_b" → block name "pipe_a_b"
		pipelines := []merge.Pipeline{
			{Name: "a-b", Contents: "// first", Matchers: []string{`cluster="test"`}, Source: "ui"},
			{Name: "a_b", Contents: "// second", Matchers: []string{`cluster="test"`}, Source: "ui"},
		}
		_, err := merge.Assemble("coll-1", "test/metrics", cl, pipelines, "dev", "2024-01-01T00:00:00Z")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("collision"))
	})
})

var _ = Describe("WithRoleEnforcement with a nil registry", func() {
	// Review finding: a caller that asked for enforcement but passed a nil
	// registry got silence — the control disabled by the very call requesting
	// it. Not passing the option at all remains the supported way to opt out.
	It("fails loudly rather than serving unenforced config that looks enforced", func() {
		_, err := merge.Assemble("c1", "prod/metrics",
			merge.CollectorLabels{CollectorID: "c1", Labels: map[string]string{"role": "metrics"}},
			nil, "test", "2026-01-01T00:00:00Z", merge.WithRoleEnforcement(nil))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("role enforcement was requested"))
	})

	It("still serves unenforced when the option is simply not passed", func() {
		_, err := merge.Assemble("c1", "prod/metrics",
			merge.CollectorLabels{CollectorID: "c1", Labels: map[string]string{"role": "metrics"}},
			nil, "test", "2026-01-01T00:00:00Z")
		Expect(err).NotTo(HaveOccurred())
	})
})
