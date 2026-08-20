package simsvc

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/grafana/alloy/syntax/ast"
	"github.com/grafana/alloy/syntax/parser"
	"github.com/grafana/alloy/syntax/token"
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
// host outside the allowlist.
var ErrEndpointNotAllowed = errors.New("endpoint_not_allowed")

// hostPortLiteral matches a bare host:port string literal, the shape
// otelcol.exporter.otlp's client.endpoint takes. A bare hostname with no port
// is deliberately NOT matched: it is indistinguishable from an ordinary string
// value such as a job name, and a check that guesses would reject valid
// configs while still not being a security boundary.
var hostPortLiteral = regexp.MustCompile(`^[A-Za-z0-9_.\-]+:[0-9]{1,5}$`)

// CheckEndpoints is the endpoint allowlist: a second, independent gate between
// a user's graph and the network.
//
// It parses the submitted config and rejects any http(s) URL or host:port
// literal whose host is not one of the harness's own names. The PRIMARY
// control is still network isolation — the simulator has no route off its
// internal network at all — because a text gate must never be the only thing
// standing between user code and a real endpoint. This exists so that a bug in
// the transform that leaves a production endpoint in place fails loudly here
// instead of quietly writing to it if the network posture ever slips.
func CheckEndpoints(config string, allowedHosts []string) error {
	file, err := parser.ParseFile("submitted.alloy", []byte(config))
	if err != nil {
		return fmt.Errorf("invalid_config: %w", err)
	}
	allowed := map[string]struct{}{}
	for _, h := range allowedHosts {
		allowed[strings.ToLower(h)] = struct{}{}
	}

	v := &endpointVisitor{allowed: allowed, bad: map[string]struct{}{}}
	ast.Walk(v, file)
	if len(v.bad) == 0 {
		return nil
	}
	names := make([]string, 0, len(v.bad))
	for n := range v.bad {
		names = append(names, n)
	}
	sort.Strings(names)
	return fmt.Errorf("%w: config names host(s) outside the sandbox harness: %s", ErrEndpointNotAllowed, strings.Join(names, ", "))
}

type endpointVisitor struct {
	allowed map[string]struct{}
	bad     map[string]struct{}
}

func (v *endpointVisitor) Visit(node ast.Node) ast.Visitor {
	lit, ok := node.(*ast.LiteralExpr)
	if !ok || lit.Kind != token.STRING {
		return v
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return v
	}
	if host, checked := extractHost(value); checked {
		if _, ok := v.allowed[strings.ToLower(host)]; !ok {
			v.bad[host] = struct{}{}
		}
	}
	return v
}

// extractHost returns the host a literal names and whether the literal is one
// the allowlist covers at all.
func extractHost(value string) (string, bool) {
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		u, err := url.Parse(value)
		if err != nil {
			// An unparseable URL cannot be shown to be safe, so it is not.
			return value, true
		}
		return u.Hostname(), true
	}
	if hostPortLiteral.MatchString(value) {
		host, _, err := net.SplitHostPort(value)
		if err != nil {
			return value, true
		}
		return host, true
	}
	return "", false
}
