package merge_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/merge"
)

// #297: a collector's load error points at a line in the MERGED config
// ("40:3: Failed to build component: …"). Assemble records where each
// pipeline's declare block sits, and Spans recovers the same map from served
// bytes alone (serve_cache stores content, not spans), so a rejection can be
// attributed to the pipeline whose text contains the line — without changing
// one served byte.
var _ = Describe("declare-block spans (#297)", func() {
	cl := merge.CollectorLabels{CollectorID: "c1", Labels: map[string]string{"cluster": "prod", "role": "metrics"}}
	alpha := merge.Pipeline{
		ID: "a", Name: "alpha", Source: "ui", Revision: 3, Matchers: []string{`cluster="prod"`},
		Contents: "prometheus.scrape \"one\" {\n  targets = []\n  forward_to = []\n}\n",
	}
	beta := merge.Pipeline{
		ID: "b", Name: "Beta Pipe!", Source: "ui", Revision: 1, Matchers: []string{`cluster="prod"`},
		Contents: "// first line\n\nprometheus.scrape \"two\" {\n  targets = []\n  bogus = true\n  forward_to = []\n}",
	}

	assemble := func(ps ...merge.Pipeline) merge.AssembleResult {
		res, err := merge.Assemble("c1", "prod/metrics", cl, ps, "test")
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	lineOf := func(content, needle string) int {
		for i, l := range strings.Split(content, "\n") {
			if strings.Contains(l, needle) {
				return i + 1
			}
		}
		Fail("needle not found: " + needle)
		return 0
	}

	It("does not change the assembled bytes", func() {
		// The span map is metadata: the content and hash are exactly what the
		// declare-wrap produces, with no marker comments added.
		res := assemble(alpha, beta)
		Expect(res.Content).NotTo(ContainSubstring("span"))
		Expect(res.Hash).To(Equal(merge.HashContent(res.Content)))
		Expect(res.Content).To(ContainSubstring("declare \"pipe_alpha\" {\n  prometheus.scrape \"one\" {\n"))
	})

	It("records one span per declare block, and Spans recovers the same map from the bytes", func() {
		res := assemble(beta, alpha)
		Expect(res.Spans).To(HaveLen(2))
		Expect(res.Spans[0].PipelineName).To(Equal("Beta Pipe!"))
		Expect(res.Spans[0].BlockName).To(Equal("pipe_beta_pipe_"))
		Expect(res.Spans[1].PipelineName).To(Equal("alpha"))
		Expect(res.Spans[0].DeclareLine).To(Equal(lineOf(res.Content, `declare "pipe_beta_pipe_"`)))
		Expect(res.Spans[0].EndLine).To(Equal(lineOf(res.Content, `pipe_beta_pipe_ "default"`)))
		Expect(merge.Spans(res.Content)).To(Equal(res.Spans))
	})

	It("maps a merged-config line to the pipeline and the line of the pipeline's own text", func() {
		res := assemble(alpha, beta)
		merged := lineOf(res.Content, "bogus = true")
		loc, ok := merge.Locate(res.Content, merged)
		Expect(ok).To(BeTrue())
		Expect(loc.PipelineName).To(Equal("Beta Pipe!"))
		// "bogus = true" is line 5 of beta's own text.
		Expect(loc.PipelineLine).To(Equal(5))

		first, ok := merge.Locate(res.Content, lineOf(res.Content, `prometheus.scrape "one"`))
		Expect(ok).To(BeTrue())
		Expect(first.PipelineName).To(Equal("alpha"))
		Expect(first.PipelineLine).To(Equal(1))
	})

	It("attributes the wrapper lines to the pipeline with no line of its own text", func() {
		res := assemble(alpha)
		loc, ok := merge.Locate(res.Content, lineOf(res.Content, `pipe_alpha "default"`))
		Expect(ok).To(BeTrue())
		Expect(loc.PipelineName).To(Equal("alpha"))
		Expect(loc.PipelineLine).To(BeZero())
	})

	It("maps nothing for a header line, a line past the blocks, or an empty config", func() {
		res := assemble(alpha)
		_, ok := merge.Locate(res.Content, 1)
		Expect(ok).To(BeFalse(), "the header comment belongs to no pipeline")
		// A baseline pipeline is appended after the blocks (beacon.AppendBaseline).
		withBaseline := res.Content + "\nprometheus.exporter.self \"shepherd\" {}\n"
		_, ok = merge.Locate(withBaseline, lineOf(withBaseline, "prometheus.exporter.self"))
		Expect(ok).To(BeFalse())
		Expect(merge.Spans(withBaseline)).To(Equal(res.Spans), "appending after the blocks moves no span")

		empty := assemble()
		Expect(empty.Spans).To(BeEmpty())
		_, ok = merge.Locate(empty.Content, 2)
		Expect(ok).To(BeFalse())
	})

	DescribeTable("ErrorPosition reads the line:col an Alloy load error starts with",
		func(msg string, wantLine, wantCol int, wantOK bool) {
			line, col, ok := merge.ErrorPosition(msg)
			Expect(ok).To(Equal(wantOK))
			Expect(line).To(Equal(wantLine))
			Expect(col).To(Equal(wantCol))
		},
		Entry("bare position", "40:3: Failed to build component: decoding configuration", 40, 3, true),
		Entry("file-prefixed", "remote.alloy:12:5: unrecognized attribute name \"bogus\"", 12, 5, true),
		Entry("after a prefix", "loading config: 7:1: expected block", 7, 1, true),
		Entry("no position", "context deadline exceeded", 0, 0, false),
		Entry("a clock time is not a position", "at 12:01:02 something failed", 0, 0, false),
	)
})
