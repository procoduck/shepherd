package simulate

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/grafana/alloy/syntax/ast"
	"github.com/grafana/alloy/syntax/parser"
	"github.com/grafana/alloy/syntax/token"

	"shepherd/internal/schema"
	"shepherd/internal/visual"
)

// Rule P1' — the provenance post-condition, and the piece that replaces a
// tautology.
//
// SCOPE, stated first because a previous version of this file overreached and
// the overreach was the defect: this check bounds which authored VALUES leave
// the user's graph. It does NOT bound where the sandbox can connect. It cannot:
// a relabel rule computes __address__ at RUNTIME from label data, so no
// statement about the rendered text can say where a scrape will land. What
// stops the sandbox reaching anything is the network it runs on
// (e2e/sandbox_egress_test.go proves that by execution, and it is the only
// thing that does).
//
// The deleted rule P5 is the reason that paragraph is here. It refused the run
// on any host-SHAPED literal anywhere in the render that was not a harness
// address, and it was simultaneously permeable and deny-everything: it could
// not see the runtime retarget it was built to catch (a graph writing
// `target_label = "__address__"` with a `replacement` assembled from regex
// captures contains no host-shaped token at all), while it rejected five of six
// ordinary corpus graphs over inert `rule.*.target_label` values at paths the
// overlay deliberately allowlists. Static analysis of rendered text was the
// wrong instrument for reachability; deleting it is the fix, not a regression.
//
// The post-condition this supersedes walked the transformed props asserting
// `declared != secret`: the SAME predicate the old secret sweep applied. It
// could only ever catch a bug in the sweep, never the type-declaration gap the
// sweep was blind to, which is exactly why it passed all three CRITICAL-1
// leaks (otelcol.receiver.solace auth.sasl_xauth2.bearer,
// otelcol.receiver.cloudflare secret, otelcol.processor.resourcedetection
// openshift.token) with zero complaint.
//
// This asks a different question, of a different artefact, computed from a
// different input: parse the RENDERED config and account for every string
// literal in it against a set derived from the AUTHORED graph plus the keep
// list — never from what rule K produced. A rule K that copies an unkept path
// therefore fails here as well as at P1, and a leak that arrives through the
// renderer rather than through props (render.go writes a binding's expression
// verbatim at the node's top level) fails here even though no props key holds
// it.
//
// It is value-granular, not attribute-granular: the same string authored at
// both a kept and an unkept path is accounted for. That is a deliberate, stated
// limit — the alternative is failing legitimate runs, which is the most likely
// source of churn in a check that gates every real simulation.
func checkProvenance(rendered string, req TransformRequest, stubbed map[string]bool) TransformErrors {
	file, err := parser.ParseFile("transformed.alloy", []byte(rendered))
	if err != nil {
		return TransformErrors{{
			Code:    CodeContainmentViolated,
			Message: fmt.Sprintf("transformed render does not parse, so its literals cannot be accounted for: %v", err),
		}}
	}

	allowed := allowedLiterals(req, stubbed)
	v := &literalVisitor{
		allowed: allowed,
		harness: harnessPrefixes(req.Harness),
		unknown: map[string]bool{},
		shaped:  map[string]bool{},
	}
	ast.Walk(v, file)

	var errs TransformErrors
	for _, value := range sortedSet(v.shaped) {
		errs = append(errs, TransformError{
			Code: CodeContainmentViolated,
			// The value itself is never echoed: this error is rendered in a
			// browser and logged server-side, and by construction it is the
			// thing we believe is a credential.
			Message: fmt.Sprintf("transformed render carries a literal with a credential shape (%s)", credentialShapeOf(value)),
		})
	}
	for _, value := range sortedSet(v.unknown) {
		errs = append(errs, TransformError{
			Code: CodeUnknownProvenance,
			Message: fmt.Sprintf(
				"transformed render carries a %d-character string literal that is neither a harness value, a transform constant, nor a value authored at an allowlisted path",
				len(value)),
		})
	}
	return errs
}

type literalVisitor struct {
	allowed map[string]bool
	harness []string
	unknown map[string]bool
	shaped  map[string]bool
}

func (v *literalVisitor) Visit(node ast.Node) ast.Visitor {
	lit, ok := node.(*ast.LiteralExpr)
	if !ok || lit.Kind != token.STRING {
		return v
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		v.unknown[lit.Value] = true
		return v
	}

	if credentialShapeOf(value) != "" {
		v.shaped[value] = true
		return v
	}
	if v.allowed[value] {
		return v
	}
	for _, prefix := range v.harness {
		if strings.HasPrefix(value, prefix) {
			return v
		}
	}
	v.unknown[value] = true
	return v
}

// credentialShapePrefixes — rule P4. Defence in depth, and the one place this
// design trades a false-positive risk for one: a relabel regex that legitimately
// matched "Bearer " would break a real user's simulation. The set is
// deliberately tiny and high-confidence — none of these ever appears in a
// relabel regex, a job name or a stage expression — and it is not to be grown
// casually.
//
//nolint:gosec // G101 sees credential-shaped strings, which is the point: these are the PREFIXES a leak would start with, not credentials
var credentialShapePrefixes = map[string]string{
	"-----BEGIN ": "a PEM private key or certificate block",
	"eyJ":         "a JWT",
	"AKIA":        "an AWS access key id",
	"ghp_":        "a GitHub personal access token",
	"xoxb-":       "a Slack bot token",
	"xoxp-":       "a Slack user token",
	"xoxa-":       "a Slack app token",
	"xoxr-":       "a Slack refresh token",
	"xoxs-":       "a Slack workspace token",
	"Bearer ":     "an HTTP Authorization bearer header",
}

// credentialShapeOf names the credential shape a literal has, or "" for none.
func credentialShapeOf(value string) string {
	for prefix, what := range credentialShapePrefixes {
		if strings.HasPrefix(value, prefix) {
			return what
		}
	}
	return ""
}

// harnessPrefixes are the strings a literal may legitimately start with because
// the transform built it from a harness address: the capture base URL plus a
// capture path, the capture directory plus a file name, the log directory plus
// a fixture name. A prefix rather than an equality because rules D and G append
// to these, and an equality set would have to enumerate every capture path —
// which would make this check a copy of rule D's own table instead of an
// independent statement about addresses.
func harnessPrefixes(h HarnessEndpoints) []string {
	candidates := []string{
		strings.TrimSuffix(h.CaptureBaseURL, "/"),
		h.OTLPGRPCAddress, h.SyslogHost,
		strings.TrimSuffix(h.CaptureDir, "/"),
		h.TargetAddress,
		strings.TrimSuffix(h.LogDir, "/"),
	}
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

// allowedLiterals is the set of string values the transform may legitimately
// emit, computed from the AUTHORED graph, the overlay policy and the stub
// fixture library. Nothing in it is read from the transformed graph.
func allowedLiterals(req TransformRequest, stubbed map[string]bool) map[string]bool {
	allow := map[string]bool{
		substitutedValue: true,
		// §6.4's TLS downgrades and the renderer's own bool/number spellings.
		"true": true, "false": true, "": true,
		// The target_set class's forced scheme and the label names it writes.
		// The harness ADDRESS it writes is covered by harnessPrefixes; these
		// three are constants of the transform itself.
		"http": true, labelAddress: true, labelScheme: true,
	}
	if req.Harness.SyslogPort != 0 {
		allow[strconv.Itoa(req.Harness.SyslogPort)] = true
	}

	// Values rule D forces on a destination for the unauthenticated in-pod
	// capture endpoint.
	for _, name := range sortedComponentNames(req.Policy) {
		spec := req.Policy.Components[name].Destination
		if spec == nil {
			continue
		}
		for _, forced := range spec.Ensure {
			addLiteral(allow, forced)
		}
	}

	// Everything rule G can write: every stub fixture's label sets, every
	// fixture name (which becomes a job label and a file name) and the file
	// names themselves.
	for _, fixture := range StubFixtureNames() {
		allow[fixture] = true
		allow[StubLogFileName(fixture)] = true
		targets, ok := StubTargets(fixture)
		if !ok {
			continue
		}
		for _, t := range targets {
			for k, val := range t {
				allow[k], allow[val] = true, true
			}
		}
	}

	// Every value the user authored at a path the keep list names. Node labels
	// too: the renderer emits them as block labels and rule D interpolates the
	// sanitized form into the file exporter's path.
	for _, n := range req.Graph.Nodes {
		allow[n.Label], allow[visual.SanitizeLabel(n.Label)] = true, true
		if stubbed[n.ID] {
			continue
		}
		comp, known := req.Schema.Components[n.Component]
		if !known {
			continue
		}
		keep := req.Policy.Components[n.Component].Keep
		walkPropsCanonical(comp.Attributes, comp.Blocks, n.Props, nil, nil,
			func(value interface{}, _ string, canon, _ []string) {
				class, kept := keep.ClassOf(canon)
				switch {
				case !kept:
				case class == ClassTargetSet:
					// The authored __address__ and __scheme__ are exactly what
					// the target_set class exists to destroy, so they must not
					// be laundered into the allowed set by virtue of the PATH
					// being kept. Only the user's own labels are.
					addTargetSetLiterals(allow, value)
				default:
					addLiteral(allow, value)
				}
			})
	}
	return allow
}

// addTargetSetLiterals adds the label names and values a target_set path may
// legitimately contribute, which is every label the class does not force or
// remove. The harness address it writes instead is covered by harnessPrefixes.
func addTargetSetLiterals(allow map[string]bool, v interface{}) {
	list, ok := v.([]interface{})
	if !ok {
		return
	}
	for _, element := range list {
		labels, isMap := element.(map[string]interface{})
		if !isMap {
			continue
		}
		for name, value := range labels {
			if name == labelAddress || name == labelScheme ||
				steeringLabels[name] || strings.HasPrefix(name, "__param_") ||
				schema.IsCredentialName(name) {
				// Each of these is a label the target_set class removes rather
				// than copies, so nothing it carried has provenance. Leaving
				// them in the allowed set is what made this check launder the
				// value it was supposed to account for: `targets = [{token =
				// "…"}]` was allowed because the PATH was kept, which is
				// precisely the reasoning P1' exists to avoid.
				continue
			}
			allow[name] = true
			addLiteral(allow, value)
		}
	}
}

// addLiteral adds every string a value can contribute to the render, including
// map KEYS: the renderer quotes a key that is not a bare identifier, so
// {"x-token": "…"} puts "x-token" in the output as a string literal.
//
// It skips an entry whose key is credential-shaped, mirroring rule K's
// constrainKeys — and the mirroring is the point. P1' is worth having only
// while it is computed independently of what rule K produced, so it has to
// decide for itself that a user-chosen `api_key` inside a kept `map` has no
// provenance. A keeper that stopped guarding those keys would then fail here
// with unknown_provenance instead of shipping, which is the second gate the
// red proof in docs/proofs/transform-secret-drop.md demonstrates.
func addLiteral(allow map[string]bool, v interface{}) {
	switch x := v.(type) {
	case string:
		allow[x] = true
	case bool:
		allow[strconv.FormatBool(x)] = true
	case float64:
		allow[strconv.FormatFloat(x, 'f', -1, 64)] = true
		if x == float64(int64(x)) {
			allow[strconv.FormatInt(int64(x), 10)] = true
		}
	case []interface{}:
		for _, el := range x {
			addLiteral(allow, el)
		}
	case map[string]interface{}:
		for key, val := range x {
			if schema.IsCredentialName(key) {
				continue
			}
			allow[key] = true
			addLiteral(allow, val)
		}
	}
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
