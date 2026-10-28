package simulate

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/grafana/alloy/syntax/ast"
	"github.com/grafana/alloy/syntax/token"

	"shepherd/internal/alloysyntax"
	"shepherd/internal/visual"
)

// RunInterval is one counted interval of a pipeline (runintervals.go): which
// component sets it, which attribute (block-qualified path, dot-joined), and
// its value.
type RunInterval struct {
	Component string
	Label     string
	Attribute string
	Interval  time.Duration
	// NonLiteral reports that the attribute is set to an expression the run
	// window cannot evaluate, so Interval is the schema default.
	NonLiteral bool
}

// RunWindow is how long a sandbox run lasts and, when that differs from what
// was asked for or still falls short, why.
type RunWindow struct {
	Duration time.Duration
	Note     string
}

// RunWindowFor renders doc with the merged schema and extends requested to
// cover two of the longest intervals in it, capped at ceiling. A graph that
// does not render gets requested unchanged: the run worker fails it with the
// render diagnostics, so there is no window to explain.
func RunWindowFor(doc visual.GraphDocument, merged map[string]any, requested, ceiling time.Duration) (RunWindow, error) {
	unchanged := RunWindow{Duration: requested}
	b, err := json.Marshal(merged)
	if err != nil {
		return unchanged, fmt.Errorf("run window: encode schema: %w", err)
	}
	var payload visual.SchemaPayload
	if err := json.Unmarshal(b, &payload); err != nil {
		return unchanged, fmt.Errorf("run window: decode schema: %w", err)
	}
	policy, err := LoadPolicy(merged)
	if err != nil {
		return unchanged, fmt.Errorf("run window: %w", err)
	}
	rendered := visual.Render(doc, payload)
	if len(rendered.Diagnostics) > 0 {
		return unchanged, nil
	}
	intervals, err := PipelineIntervals([]byte(rendered.Content), merged, policy)
	if err != nil {
		return unchanged, err
	}
	return ExtendRunWindow(requested, ceiling, intervals), nil
}

// ExtendRunWindow extends requested to twice the longest interval, capped at
// ceiling. It never shortens requested.
func ExtendRunWindow(requested, ceiling time.Duration, intervals []RunInterval) RunWindow {
	w := RunWindow{Duration: requested}
	var slowest *RunInterval
	for i := range intervals {
		if slowest == nil || intervals[i].Interval > slowest.Interval {
			slowest = &intervals[i]
		}
	}
	if slowest == nil {
		return w
	}
	want := 2 * slowest.Interval
	if requested >= want {
		return w
	}
	what := describeInterval(*slowest)
	switch {
	case want <= ceiling:
		w.Duration = want
		w.Note = fmt.Sprintf("Extended to %s so the %s runs twice%s.", formatRunLength(want), what, nonLiteralNote(*slowest))
	case requested < ceiling:
		w.Duration = ceiling
		w.Note = fmt.Sprintf("Extended to %s, the maximum run length, for the %s%s; it needs %s to run twice, so it may not run twice in this run.",
			formatRunLength(ceiling), what, nonLiteralNote(*slowest), formatRunLength(want))
	default:
		w.Note = fmt.Sprintf("The run is at the %s maximum run length; the %s%s needs %s to run twice, so it may not run twice in this run.",
			formatRunLength(requested), what, nonLiteralNote(*slowest), formatRunLength(want))
	}
	return w
}

// describeInterval reads `the 60s scrape interval of prometheus.scrape "x"`
// (without the article, which the caller supplies).
func describeInterval(iv RunInterval) string {
	segs := strings.Split(iv.Attribute, ".")
	attr := strings.ReplaceAll(segs[len(segs)-1], "_", " ")
	if len(segs) > 1 {
		attr = strings.Join(segs[:len(segs)-1], " ") + " " + attr
	}
	who := iv.Component
	if iv.Label != "" {
		who += " " + strconv.Quote(iv.Label)
	}
	return fmt.Sprintf("%s %s of %s", formatInterval(iv.Interval), attr, who)
}

func nonLiteralNote(iv RunInterval) string {
	if !iv.NonLiteral {
		return ""
	}
	return fmt.Sprintf(" (its value is not a literal, so the schema default %s is assumed)", formatInterval(iv.Interval))
}

// formatRunLength reads a run length: whole minutes as "2m", else seconds.
func formatRunLength(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return formatSeconds(d)
}

// formatInterval reads an interval the way it is usually written: "60s",
// "90s", "5m", "1h", "250ms".
func formatInterval(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d >= 2*time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	case d < time.Second:
		return d.String()
	default:
		return formatSeconds(d)
	}
}

func formatSeconds(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%ds", d/time.Second)
	}
	return d.String()
}

// PipelineIntervals lists every counted interval (runintervals.go) the
// components of an Alloy config set, at the top level and inside declare
// bodies. An unset attribute takes its schema default; an attribute in a
// nested block is read only where the block is written. A component the
// sandbox transform stubs or removes (ComponentPolicy.ReplacedInSandbox: a
// discovery_stub, or a sim_secret_source such as remote.kubernetes.secret) is
// skipped: its own cadence never runs there.
func PipelineIntervals(content []byte, merged map[string]any, policy Policy) ([]RunInterval, error) {
	file, err := alloysyntax.ParseFile("<run window>", content)
	if err != nil {
		return nil, fmt.Errorf("run window: parse pipeline: %w", err)
	}
	components, _ := merged["components"].(map[string]any) //nolint:errcheck // absent components means no schema, so no intervals
	var out []RunInterval
	var walk func(body ast.Body)
	walk = func(body ast.Body) {
		for _, stmt := range body {
			block, ok := stmt.(*ast.BlockStmt)
			if !ok {
				continue
			}
			name := strings.Join(block.Name, ".")
			if name == "declare" {
				walk(block.Body)
				continue
			}
			spec, ok := components[name].(map[string]any)
			if !ok {
				continue
			}
			if policy.Components[name].ReplacedInSandbox() {
				continue
			}
			collectIntervals(name, block.Label, block.Body, spec, nil, &out)
		}
	}
	walk(file.Body)
	return out, nil
}

// collectIntervals reads the counted duration attributes spec declares at
// prefix, from body (which is the written block at that path).
func collectIntervals(component, label string, body ast.Body, spec map[string]any, prefix []string, out *[]RunInterval) {
	written := map[string]ast.Expr{}
	var blocks []*ast.BlockStmt
	for _, stmt := range body {
		switch s := stmt.(type) {
		case *ast.AttributeStmt:
			written[s.Name.Name] = s.Value
		case *ast.BlockStmt:
			blocks = append(blocks, s)
		}
	}
	for _, raw := range asList(spec["attributes"]) {
		attr, _ := raw.(map[string]any)  //nolint:errcheck // a malformed entry is skipped below
		name, _ := attr["name"].(string) //nolint:errcheck // as above
		if name == "" || attr["type"] != "duration" {
			continue
		}
		path := append(append([]string{}, prefix...), name)
		class, ok := ClassifyIntervalAttribute(component, path)
		if !ok || !class.Counted {
			continue
		}
		def, hasDefault := parseDuration(attr["default"])
		iv := RunInterval{Component: component, Label: label, Attribute: strings.Join(path, ".")}
		if expr, set := written[name]; set {
			if d, ok := literalDuration(expr); ok {
				iv.Interval = d
			} else if hasDefault {
				iv.Interval, iv.NonLiteral = def, true
			} else {
				continue
			}
		} else if hasDefault {
			iv.Interval = def
		} else {
			continue
		}
		if iv.Interval > 0 {
			*out = append(*out, iv)
		}
	}
	for _, rawBlock := range asList(spec["blocks"]) {
		bspec, _ := rawBlock.(map[string]any) //nolint:errcheck // a malformed entry is skipped below
		bname, _ := bspec["name"].(string)    //nolint:errcheck // as above
		if bname == "" {
			continue
		}
		for _, b := range blocks {
			if strings.Join(b.Name, ".") == bname {
				collectIntervals(component, label, b.Body, bspec, append(append([]string{}, prefix...), bname), out)
			}
		}
	}
}

func asList(v any) []any {
	l, _ := v.([]any) //nolint:errcheck // absent is an empty list
	return l
}

func parseDuration(v any) (time.Duration, bool) {
	s, ok := v.(string)
	if !ok || s == "" {
		return 0, false
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, false
	}
	return d, true
}

func literalDuration(expr ast.Expr) (time.Duration, bool) {
	lit, ok := expr.(*ast.LiteralExpr)
	if !ok || lit.Kind != token.STRING {
		return 0, false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return 0, false
	}
	return parseDuration(s)
}
