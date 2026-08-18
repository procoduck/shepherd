package merge_test

import (
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/merge"
)

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

var _ = Describe("failed recompute keeps previous cache entry", func() {
	// This test verifies that when Assemble returns an error (e.g. name collision),
	// the caller should NOT update the serve cache. The merge engine itself returns
	// an error; the serve-cache write must be guarded by the caller checking this error.
	It("Assemble returns error on collision so cache write can be skipped", func() {
		cl := merge.CollectorLabels{
			CollectorID: "coll-1",
			Labels:      map[string]string{"cluster": "test", "role": "metrics"},
		}
		pipelines := []merge.Pipeline{
			{Name: "x-y", Contents: "// a", Matchers: []string{`cluster="test"`}, Source: "ui"},
			{Name: "x_y", Contents: "// b", Matchers: []string{`cluster="test"`}, Source: "ui"},
		}
		_, err := merge.Assemble("coll-1", "test/metrics", cl, pipelines, "dev", "2024-01-01T00:00:00Z")
		// Caller must NOT update cache when err != nil — this is the contract.
		Expect(err).To(HaveOccurred(), "caller must guard cache write on error")
	})
})
