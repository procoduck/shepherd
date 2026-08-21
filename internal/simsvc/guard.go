package simsvc

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/grafana/alloy/syntax/ast"
	"github.com/grafana/alloy/syntax/parser"
	"github.com/grafana/alloy/syntax/token"

	"shepherd/internal/netshape"
)

// ErrServiceAccountToken is the refusal a mounted service account token
// produces at start-up.
var ErrServiceAccountToken = errors.New("service account token present")

// CheckServiceAccountToken refuses to let the process start when a Kubernetes
// service account token is mounted.
//
// §6.4 lists "no service account token" first among the containment controls,
// and automountServiceAccountToken: false is exactly the kind of pod-spec key
// that gets dropped in a values file with nothing noticing. A process that
// refuses to boot makes that misconfiguration loud — and, unlike a YAML
// assertion, testable.
func CheckServiceAccountToken(path string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("refusing to start: %w at %s", ErrServiceAccountToken, path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("simsvc: stat service account token path: %w", err)
	}
	return nil
}

// ErrEndpointNotAllowed is returned by CheckEndpoints for a config naming a
// host outside the allowlist, or computing one instead of naming it.
var ErrEndpointNotAllowed = errors.New("endpoint_not_allowed")

// CheckEndpoints is the endpoint allowlist: declared DEFENCE IN DEPTH on the
// config the simulator receives, deny-by-default.
//
// Read the next paragraph before citing this function as containment. It is NOT
// the control that stops the sandbox reaching a real endpoint, and no comment,
// doc or proof may present it as one. It analyses TEXT, and a config can name
// an address without any literal naming it: a discovery.relabel rule writing
// `target_label = "__address__"` with a `replacement` assembled from regex
// captures retargets the scrape at runtime, and this function sees no host at
// all. What denies the sandbox a route is the network it runs on — the
// simulator's `internal: true` Docker network, verified by execution in
// e2e/sandbox_egress_test.go, whose P-deny-ip probe goes red the moment that
// network is made reachable.
//
// What it does do: parse the submitted config and refuse it unless every
// host-shaped token in every string literal is one of the harness's own names,
// and unless every expression in it is a plain literal or a reference to
// another component in the same config. That makes a transform bug that left a
// NAMED production endpoint in the config fail loudly here rather than quietly
// write to it if the network posture ever slips. It bounds the literal case; it
// does not bound the computed one.
//
// The previous form allowed anything it did not recognise, and finding H6
// measured what that cost: bracketed IPv6 host:port, bare dotted host names,
// any scheme other than http(s), hosts buried in a DSN and endpoints computed
// by sys.env() all passed. Those were not five bugs. They were one — an
// allowlist that only rejected shapes it had been taught — and inverting the
// question fixes all five at once.
//
// Inverting only became affordable when the S3 transform started CONSTRUCTING
// the config from an allowlist instead of filtering it (internal/simulate rule
// K). While 6144 authored attribute paths passed through untouched, refusing
// every dotted name would have rejected job names, file names and relabel
// regexes by the dozen. Now near-zero legitimate literals look host-shaped, and
// the ones that do are the transform's own harness addresses.
func CheckEndpoints(config string, allowedHosts []string) error {
	file, err := parser.ParseFile("submitted.alloy", []byte(config))
	if err != nil {
		return fmt.Errorf("invalid_config: %w", err)
	}
	allowed := map[string]struct{}{}
	for _, h := range allowedHosts {
		for _, host := range hostForms(h) {
			allowed[host] = struct{}{}
		}
	}

	v := &endpointVisitor{allowed: allowed, bad: map[string]struct{}{}, computed: map[string]struct{}{}}
	ast.Walk(v, file)
	if len(v.bad) == 0 && len(v.computed) == 0 {
		return nil
	}
	var parts []string
	if len(v.bad) > 0 {
		parts = append(parts, "config names host(s) outside the sandbox harness: "+strings.Join(sortedSet(v.bad), ", "))
	}
	if len(v.computed) > 0 {
		parts = append(parts, "config computes value(s) instead of naming them, so no allowlist can cover them: "+
			strings.Join(sortedSet(v.computed), ", "))
	}
	return fmt.Errorf("%w: %s", ErrEndpointNotAllowed, strings.Join(parts, "; "))
}

// hostForms expands one configured allowlist entry into the forms a literal can
// spell it: the entry itself, and — when the entry is an address rather than a
// bare name — the host netshape reports for it. Without this, configuring
// "127.0.0.1:9110" would allow nothing at all.
func hostForms(entry string) []string {
	entry = strings.ToLower(strings.TrimSpace(entry))
	if entry == "" {
		return nil
	}
	out := []string{entry}
	for _, host := range netshape.Hosts(entry) {
		if host != entry {
			out = append(out, host)
		}
	}
	return out
}

type endpointVisitor struct {
	allowed  map[string]struct{}
	bad      map[string]struct{}
	computed map[string]struct{}
}

func (v *endpointVisitor) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
	case *ast.CallExpr:
		// sys.env("EXFIL_URL") is a CallExpr, and finding H6 proved the old
		// visitor simply ignored it: it looked only at LiteralExpr, so an
		// endpoint the config COMPUTES was never examined at all. Refusing
		// calls outright removes the whole channel rather than blocklisting
		// sys.env by name.
		//
		// "A transformed config contains no calls" was true when that reasoning
		// was written and is false now: B-CONCAT. render.go's refValue (the
		// targets-port branch, render.go:412-420) emits
		// `array.concat(ref1, ref2, ...)` whenever more than one discovery
		// source fans into one prometheus.scrape (or other targets-typed)
		// input, and the M13 fix made that the ONLY correct rendering — a
		// `[a, b]` list literal there is config Alloy v1.18.1 refuses to run
		// (list-of-lists). So a fan-in graph is exactly the shape rule K now
		// produces, not an anomaly, and refusing every call made it
		// unsandboxable.
		//
		// The carve-out stays narrow on purpose: it is transparent (recurses
		// into nothing, flags nothing) only for the literal callee
		// `array.concat`, and only when EVERY argument is a pure component
		// reference — an identifier or dotted access chain with no literal,
		// no nested call, no binary expression anywhere in it. That is
		// exactly and only what render.go's reference() (render.go:804)
		// writes for a wire: `component.label.port`, never anything else. A
		// reference names no host — hosts arrive as string literals, which
		// the LiteralExpr case below still checks against the allowlist — so
		// letting pure-reference array.concat through adds no channel; it
		// only stops refusing the one shape the renderer legitimately emits.
		// Anything that does not match — a different callee, a literal or
		// computed argument, a nested call — still falls through to the
		// default below and is refused: when in doubt about an argument's
		// shape, this stays fail-closed.
		if isTransparentArrayConcat(n) {
			return nil
		}
		v.computed[callName(n)] = struct{}{}
		return nil
	case *ast.BinaryExpr:
		// "http://" + sys.env("H") and its cousins. Same reasoning: a
		// constructed config never concatenates.
		v.computed["a computed expression"] = struct{}{}
		return nil
	case *ast.LiteralExpr:
		if n.Kind != token.STRING {
			return v
		}
		value, err := strconv.Unquote(n.Value)
		if err != nil {
			// A literal that will not unquote cannot be shown to be safe.
			v.bad[n.Value] = struct{}{}
			return v
		}
		for _, host := range netshape.Hosts(value) {
			if _, ok := v.allowed[host]; !ok {
				v.bad[host] = struct{}{}
			}
		}
	}
	return v
}

// isTransparentArrayConcat reports whether call is exactly `array.concat(...)`
// with one or more arguments, every one of which is a pure component
// reference per isPureComponentRef. See the CallExpr case in Visit for why
// this, and only this, is safe to let through. "Safe" here means RELATIVE to
// the pre-existing bare-reference behavior, not absolute: a pure reference's
// runtime value is exactly as unbounded by this text check as any bare
// component reference the visitor already passes unflagged — what bounds
// where either can steer traffic is the network, not this function (see the
// CheckEndpoints doc's literal-vs-computed contract above).
func isTransparentArrayConcat(call *ast.CallExpr) bool {
	access, ok := call.Value.(*ast.AccessExpr)
	if !ok || access.Name == nil || access.Name.Name != "concat" {
		return false
	}
	ident, ok := access.Value.(*ast.IdentifierExpr)
	if !ok || ident.Ident == nil || ident.Ident.Name != "array" {
		return false
	}
	if len(call.Args) == 0 {
		// array.concat() with no arguments names nothing, but it is also not
		// the fan-in shape this carve-out exists for; refuse rather than
		// special-case an empty call no renderer emits.
		return false
	}
	for _, arg := range call.Args {
		if !isPureComponentRef(arg) {
			return false
		}
	}
	return true
}

// isPureComponentRef reports whether expr is a component reference chain —
// an identifier, or a chain of dotted accesses rooted in one — with no
// literal, call, index, binary, unary or parenthesized expression anywhere
// inside it. render.go's reference() (render.go:804) is the only producer of
// these wire values and it never writes anything else:
// `component.label.port`, dot-joined identifiers, always. Any other shape
// falls through to false, which is what keeps isTransparentArrayConcat
// fail-closed.
func isPureComponentRef(expr ast.Expr) bool {
	switch n := expr.(type) {
	case *ast.IdentifierExpr:
		return true
	case *ast.AccessExpr:
		return isPureComponentRef(n.Value)
	default:
		return false
	}
}

// callName names the function a refused CallExpr invoked, for the message. It
// is best-effort: an unrecognised callee shape still refuses the config.
func callName(call *ast.CallExpr) string {
	switch value := call.Value.(type) {
	case *ast.IdentifierExpr:
		return value.Ident.Name + "(...)"
	case *ast.AccessExpr:
		if inner, ok := value.Value.(*ast.IdentifierExpr); ok {
			return inner.Ident.Name + "." + value.Name.Name + "(...)"
		}
	}
	return "a function call"
}

func sortedSet(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
