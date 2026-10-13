package validate_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"shepherd/internal/config"
	"shepherd/internal/validate"
)

// Native Go fuzz targets for the pipeline diagnostic positions (walkthrough
// F2). Plain `go test` runs only the seed corpus (the f.Add calls plus
// testdata/fuzz/<Name>/); fuzz with e.g.
// `go test -run='^$' -fuzz=FuzzUnwrapDiagnostics -fuzztime=60s ./internal/validate`.

// FuzzUnwrapDiagnostics checks the position mapping on arbitrary pipeline
// text and an arbitrary reported position:
//
//   - every position inside the wrapped document maps to a line of contents,
//     never past its end, and to a column inside that line (or just after it);
//   - it inverts WrapForValidation exactly: a position on a line of contents
//     as wrapped maps back to that line and column of contents.
func FuzzUnwrapDiagnostics(f *testing.F) {
	f.Add("a = 1\n\nb = 2", 4, 3)
	f.Add("x", 3, 1)
	f.Add("prometheus.exporter.self \"a\" {\n  bogus = 1\n", 5, 1)
	f.Add("", 1, 1)
	f.Add("\n", 2, 0)
	f.Add("a\r\nb", -1, -1)

	f.Fuzz(func(t *testing.T, contents string, line, col int) {
		lines := strings.Split(contents, "\n")
		wrapped := strings.Split(strings.TrimSuffix(validate.WrapForValidation("p", contents), "\n"), "\n")

		out := validate.UnwrapDiagnostics(contents, []validate.Diagnostic{{Line: line, Col: col, Message: "m", Stage: 1}})
		if len(out) != 1 || out[0].Message != "m" || out[0].Stage != 1 {
			t.Fatalf("UnwrapDiagnostics changed more than the position: %+v", out)
		}
		d := out[0]
		if line <= 0 {
			if d.Line != line {
				t.Fatalf("unknown line %d became %d", line, d.Line)
			}
		} else if d.Line < 1 || d.Line > len(lines) {
			t.Fatalf("wrapped %d:%d mapped to line %d of a %d-line text", line, col, d.Line, len(lines))
		}
		if line >= 1 && line <= len(wrapped) && col >= 1 && col <= len(wrapped[line-1])+1 {
			if d.Col < 1 || d.Col > len(lines[d.Line-1])+1 {
				t.Fatalf("wrapped %d:%d mapped to %d:%d, outside line %q", line, col, d.Line, d.Col, lines[d.Line-1])
			}
		}

		// Inverse: every line of contents (the wrap drops a final newline's
		// empty last line, so it is not a wrapped line of its own).
		own := strings.Split(strings.TrimSuffix(contents, "\n"), "\n")
		for k, l := range own {
			indent := 0
			if l != "" {
				indent = 2
			}
			for _, c := range []int{1, len(l) + 1} {
				got := validate.UnwrapDiagnostics(contents, []validate.Diagnostic{{Line: k + 2, Col: c + indent}})
				if got[0].Line != k+1 || got[0].Col != c {
					t.Fatalf("wrapped %d:%d (line %d:%d of contents) mapped to %d:%d", k+2, c+indent, k+1, c, got[0].Line, got[0].Col)
				}
			}
		}
	})
}

// FuzzValidatePipeline runs the real gate's in-process stages (the Alloy
// syntax parser and the port-shape check; no binary) on arbitrary operator
// text: never a panic, and every diagnostic lands inside the text.
func FuzzValidatePipeline(f *testing.F) {
	f.Add("prometheus.remote_write \"a\" {\n  endpoint {\n    url = = \"x\"\n  }\n}\n")
	f.Add("x")
	f.Add("prometheus.exporter.self \"a\" {\n  bogus = 1\n")
	f.Add(listOfLists)
	f.Add("declare \"inner\" {\n}\n}\npipe_p \"default\" { }\ndeclare \"x\" {")
	f.Add("")

	v := validate.New(&config.ValidateConfig{AlloyBinary: "", StabilityLevel: "experimental", Timeout: 10 * time.Second})
	f.Fuzz(func(t *testing.T, contents string) {
		r := v.ValidatePipeline(context.Background(), "fuzz", contents)
		n := len(strings.Split(contents, "\n"))
		for _, d := range r.Diagnostics {
			if d.Line < 0 || d.Line > n || d.Col < 0 {
				t.Fatalf("diagnostic %d:%d outside a %d-line text: %s", d.Line, d.Col, n, d.Message)
			}
		}
	})
}
