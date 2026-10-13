package merge

import (
	"regexp"
	"strconv"
	"strings"
)

// BlockSpan is where one pipeline's declare block sits in an assembled
// config (#297). A collector reports a load error against a line of the
// MERGED text it was served ("40:3: Failed to build component: …"); the span
// is what maps that line back to the pipeline whose text contains it.
//
// Spans are metadata about the bytes, never part of them: Assemble records
// them while writing, and Spans recovers the identical map from served
// content alone, because serve_cache stores the content, not the spans.
type BlockSpan struct {
	// PipelineName is the pipeline's display name, as the header lists it.
	PipelineName string
	// BlockName is the declare label, "pipe_" + SanitizeName(PipelineName).
	BlockName string
	// DeclareLine is the 1-based line of `declare "<BlockName>" {`. The
	// pipeline's own text starts on the next line.
	DeclareLine int
	// EndLine is the 1-based line of the block's instantiation,
	// `<BlockName> "default" { }`, the last line the wrapper writes.
	EndLine int
}

// pipelineLines is how many lines of the pipeline's own text the block holds:
// everything between the declare line and the closing brace, which sits on
// the line before the instantiation.
func (s BlockSpan) pipelineLines() int { return s.EndLine - s.DeclareLine - 2 }

// Location is a merged-config line attributed to a pipeline.
type Location struct {
	BlockSpan
	// PipelineLine is the 1-based line of the pipeline's own text, or 0 when
	// the merged line is part of the declare wrapper (the declare line, the
	// closing brace or the instantiation) rather than of the pipeline's text.
	PipelineLine int
}

// declareLineRE matches the wrapper's opening line exactly as Assemble writes
// it. The pipeline's own lines are always indented, so a column-0 match is
// never pipeline text.
var declareLineRE = regexp.MustCompile(`^declare "(pipe_[^"]*)" \{$`)

// headerPipelineRE matches one "//   - <name> (rev N)" line of the header.
var headerPipelineRE = regexp.MustCompile(`^//   - (.*) \(rev -?\d+\)$`)

// Spans recovers the declare-block spans of an assembled config from its
// bytes: the same map Assemble records in AssembleResult.Spans for the same
// content. Content appended after the blocks (beacon.AppendBaseline's
// self-monitoring pipeline) moves no span and belongs to none.
func Spans(content string) []BlockSpan {
	lines := strings.Split(content, "\n")

	var spans []BlockSpan
	for i := 0; i < len(lines); i++ {
		m := declareLineRE.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		block := m[1]
		// The closing brace is the first column-0 "}" after the declare line;
		// the instantiation follows it.
		for j := i + 1; j+1 < len(lines); j++ {
			if lines[j] != "}" {
				continue
			}
			if lines[j+1] != block+` "default" { }` {
				break
			}
			spans = append(spans, BlockSpan{
				PipelineName: block, BlockName: block,
				DeclareLine: i + 1, EndLine: j + 2,
			})
			i = j + 1
			break
		}
	}
	nameSpans(lines, spans)
	return spans
}

// headerListRE matches the header line that opens the pipeline list.
var headerListRE = regexp.MustCompile(`^// Pipelines \((\d+)\):$`)

// nameSpans gives each span the display name the header lists for it,
// leaving the block name on any it cannot name for certain.
//
// The header lists the included pipelines in the order their blocks follow
// (buildHeader and the declare loop walk the same slice, as they have since
// the first served format), so the i-th "//   - <name> (rev N)" entry under
// "// Pipelines (N):" names the i-th block. Pairing by position is exact
// where matching by sanitized name is not: commentSafe prints "a\r\nb" (block
// pipe_a__b) as "a b", which sanitizes to the block of a pipeline really
// named "a b" — keyed by name, one of the two would carry the other's name
// (found by FuzzAssembleSpans).
//
// When the list is not exactly N well-formed entries — content served before
// commentSafe, where a raw line break split an entry across lines, or text
// that is no assembled config at all — names fall back to the sanitized-name
// match, and a block two entries both sanitize to keeps its block name.
func nameSpans(lines []string, spans []BlockSpan) {
	var listed []string
	n := -1
	for _, l := range lines {
		if !strings.HasPrefix(l, "//") {
			break
		}
		if n < 0 {
			if m := headerListRE.FindStringSubmatch(l); m != nil {
				var err error
				if n, err = strconv.Atoi(m[1]); err != nil {
					n = -1
					break
				}
			}
			continue
		}
		if len(listed) == n {
			break
		}
		m := headerPipelineRE.FindStringSubmatch(l)
		if m == nil {
			break
		}
		listed = append(listed, m[1])
	}

	if n >= 0 && len(listed) == n && len(spans) >= n {
		for i, name := range listed {
			spans[i].PipelineName = name
		}
		return
	}

	names := map[string]string{}
	ambiguous := map[string]bool{}
	for _, l := range lines {
		if !strings.HasPrefix(l, "//") {
			break
		}
		if m := headerPipelineRE.FindStringSubmatch(l); m != nil {
			key := "pipe_" + SanitizeName(m[1])
			if _, dup := names[key]; dup {
				ambiguous[key] = true
			}
			names[key] = m[1]
		}
	}
	for i := range spans {
		if name, ok := names[spans[i].BlockName]; ok && !ambiguous[spans[i].BlockName] {
			spans[i].PipelineName = name
		}
	}
}

// Locate attributes a 1-based line of an assembled config to the pipeline
// whose declare block contains it. ok is false for a line no block contains:
// the header comment, the blank lines between blocks, or anything appended
// after them.
func Locate(content string, line int) (Location, bool) {
	for _, s := range Spans(content) {
		if line < s.DeclareLine || line > s.EndLine {
			continue
		}
		loc := Location{BlockSpan: s}
		if rel := line - s.DeclareLine; rel >= 1 && rel <= s.pipelineLines() {
			loc.PipelineLine = rel
		}
		return loc, true
	}
	return Location{}, false
}

// errorPositionRE matches the "line:col:" an Alloy diagnostic carries, bare
// at the start of the message or after a file name or prefix. The trailing
// whitespace keeps a clock time ("12:01:02") from reading as a position.
var errorPositionRE = regexp.MustCompile(`(?:^|[\s:])(\d+):(\d+):(?:\s|$)`)

// ErrorPosition reads the merged-config position a collector's load error
// points at ("40:3: Failed to build component: …"). ok is false when the
// message carries none.
func ErrorPosition(msg string) (line, col int, ok bool) {
	m := errorPositionRE.FindStringSubmatch(msg)
	if m == nil {
		return 0, 0, false
	}
	l, lErr := strconv.Atoi(m[1])
	c, cErr := strconv.Atoi(m[2])
	if lErr != nil || cErr != nil || l < 1 {
		return 0, 0, false
	}
	return l, c, true
}
