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
		res, err := merge.Assemble("c1", "prod/metrics", cl, ps)
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

	// Found by FuzzAssembleSpans (seed testdata/fuzz/FuzzAssembleSpans/
	// header-name-collision). The header prints "a\r\nb" as "a b", which
	// sanitizes to the block of a pipeline really named "a\x01b"; keyed by
	// sanitized name, Spans gave that block the last header entry's name.
	// Red run, executed: block pipe_a_b was attributed to the name printed
	// for "a\r\nb".
	It("never attributes a block to another pipeline's header entry", func() {
		crlf := merge.Pipeline{ID: "c", Name: "a\r\nb", Source: "git", RepoLinkCollectorID: "c1", Contents: "x {}\n"}
		plain := merge.Pipeline{ID: "p", Name: "a\x01b", Source: "ui", Matchers: []string{`cluster="prod"`}, Contents: "y {}\n"}
		res := assemble(crlf, plain)
		got := merge.Spans(res.Content)
		Expect(got).To(HaveLen(2))
		byBlock := map[string]string{}
		for _, s := range got {
			byBlock[s.BlockName] = s.PipelineName
		}
		// pipe_a__b carries the header's rendering of "a\r\nb".
		Expect(byBlock).To(Equal(map[string]string{"pipe_a_b": "a\x01b", "pipe_a__b": "a b"}))
	})

	// The header prints the git pipeline "a\r\nb" (block pipe_a__b) and the UI
	// pipeline "a b" (block pipe_a_b) identically. Each block must still get
	// its own entry: the reconcile RPC reports `rejected in pipeline "<name>"`
	// and the UI matches that name exactly. Red run, executed against the
	// ambiguity fallback (99c4c17): pipe_a_b came back named "pipe_a_b".
	It("names each block by its own header entry when two entries print alike", func() {
		crlf := merge.Pipeline{ID: "c", Name: "a\r\nb", Source: "git", RepoLinkCollectorID: "c1", Contents: "x {}\n"}
		spaced := merge.Pipeline{ID: "s", Name: "a b", Source: "ui", Matchers: []string{`cluster="prod"`}, Contents: "y {}\n"}
		res := assemble(crlf, spaced)
		got := merge.Spans(res.Content)
		Expect(got).To(HaveLen(2))
		byBlock := map[string]string{}
		for _, s := range got {
			byBlock[s.BlockName] = s.PipelineName
		}
		Expect(byBlock).To(Equal(map[string]string{"pipe_a_b": "a b", "pipe_a__b": "a b"}))

		loc, ok := merge.Locate(res.Content, lineOf(res.Content, "  y {}"))
		Expect(ok).To(BeTrue())
		Expect(loc.BlockName).To(Equal("pipe_a_b"))
		Expect(loc.PipelineName).To(Equal("a b"))
	})

	// Content served before commentSafe (#75) could carry a raw line break in
	// a header entry, splitting it across lines and ending the comment. The
	// list then is not N well-formed entries, so positions cannot be trusted:
	// names fall back to the sanitized-name match.
	It("falls back to the sanitized-name match when the header list is malformed", func() {
		content := "// Generated by Shepherd\n" +
			"// Pipelines (2):\n" +
			"//   - zeta (rev 2)\n" +
			"//   - a\n" +
			"b (rev 1)\n" +
			"\n" +
			"declare \"pipe_a_b\" {\n  x {}\n}\npipe_a_b \"default\" { }\n" +
			"\n" +
			"declare \"pipe_zeta\" {\n  y {}\n}\npipe_zeta \"default\" { }\n"
		got := merge.Spans(content)
		Expect(got).To(HaveLen(2))
		// Paired by position, zeta would name pipe_a_b.
		Expect(got[0].PipelineName).To(Equal("pipe_a_b"), "the broken entry names no block")
		Expect(got[1].PipelineName).To(Equal("zeta"))
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
