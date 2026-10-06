package wizard

import "fmt"

// LogFormatRaw is the log format that ships lines unparsed: no loki.process
// stage at all.
const LogFormatRaw = "raw"

// LogParseStages returns the loki.process stage blocks (indented for the body
// of a loki.process block, one trailing newline) that parse lines of the given
// format. "" and LogFormatRaw return "" — nothing to parse.
//
// logfmt and json extract the line's `level` field and promote it to a
// `level` stream label. A bare `stage.logfmt {}` or `stage.json {}` is not an
// option: both decode cleanly, so `alloy validate` accepts them, but a running
// Alloy refuses to build them ("logfmt mapping or regex is required", "JMES
// expressions or regex is required" — Alloy v1.20.1,
// loki/process/stages/{logfmt,json}.go). And extraction alone would be
// invisible: the extracted map only matters to a later stage, so the parse has
// to feed one for the format choice to change anything. `level` is the field
// both formats conventionally carry and the one label people filter logs by;
// its value set is small, so it is safe as an index label. A line without a
// `level` key gets no label — the stages never drop or rewrite a line.
//
// cri and docker unwrap the container-runtime envelope; those stages take no
// required arguments.
func LogParseStages(format string) (string, error) {
	switch format {
	case "", LogFormatRaw:
		return "", nil
	case "logfmt":
		return `  stage.logfmt {
    mapping = { level = "" }
  }

  stage.labels {
    values = { level = "" }
  }
`, nil
	case "json":
		return `  stage.json {
    expressions = { level = "" }
  }

  stage.labels {
    values = { level = "" }
  }
`, nil
	case "cri", "docker":
		return "  stage." + format + " {}\n", nil
	default:
		return "", fmt.Errorf("log format %q is not supported, want one of: logfmt|json|cri|docker|raw", format)
	}
}
