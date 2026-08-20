// Package netshape finds the host names a string literal could steer a network
// connection to.
//
// WHAT IT IS FOR, AND WHAT IT IS NOT. This package recognises shapes in TEXT.
// It cannot, and its callers must not claim it can, bound where a running Alloy
// config will connect: a relabel rule computes __address__ at runtime from
// label data, so the address the sandbox dials need never appear as a literal
// anywhere. Reachability in the S3 sandbox is contained by the network — the
// simulator's `internal: true` Docker network, proven by execution in
// e2e/sandbox_egress_test.go. An earlier version of this doc comment said this
// package was half of "the two gates that stand between a user's graph and the
// network". It was not, that framing produced a static rule (simulate's P5,
// since deleted) that was permeable and deny-everything at once, and it is
// corrected here rather than left to be re-read as an architecture.
//
// What the two remaining callers actually use it for:
//
//   - internal/simsvc CheckEndpoints — a declared defence-in-depth text gate on
//     the config the simulator receives. It fails a config loudly when a
//     transform bug leaves a named production endpoint in it. It is not what
//     stops the sandbox reaching that endpoint.
//   - internal/simulate targetsValue — hygiene on values the TRANSFORM itself
//     invents. Six discovery stub fixtures carry realistic host names in meta
//     labels; the harness address is substituted for any label value that looks
//     like a host so no real name is shipped under any label.
//
// Both share these SHAPES and nothing else — they run in different processes at
// different moments against different allow-sets — which is why the table lives
// in one leaf package with its own exhaustive spec. Finding H6 measured the
// cost of the alternative: the simulator's allowlist recognised http(s) URLs
// and the regex ^[A-Za-z0-9_.\-]+:[0-9]{1,5}$ and nothing else, so five shapes
// walked straight through it —
//
//	[2001:db8::1]:9090                     bracketed IPv6 host:port
//	broker.evil.example.com                a bare dotted name, no port
//	ftp://evil.example.com/x               any scheme that is not http(s)
//	u:p@tcp(db.evil.example.com:3306)/x    a host inside a DSN
//	sys.env("EXFIL_URL")                   not a literal at all
//
// The last of those is not this package's problem — it is an expression, and
// CheckEndpoints refuses expressions outright — but the other four are, and
// they are one problem, not four patches.
//
// Deny-by-default is what makes the bare-hostname rule affordable at all. The
// old comment on hostPortLiteral argued that a bare name "is indistinguishable
// from an ordinary string value such as a job name, and a check that guesses
// would reject valid configs" — true when 6144 authored attribute paths passed
// through the transform untouched. It is no longer true: rule K constructs the
// config from an allowlist, so a dotted name reaching the render is already
// anomalous, and refusing it costs a legitimate run almost nothing while
// closing the shape that finding H6 proved was the easiest bypass of the lot.
package netshape

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	// schemeURL matches a URL with ANY scheme, not just http(s). ftp://,
	// mysql://, redis:// and thrift:// all reach a host just as well.
	schemeURL = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.\-]*://[^\s"'<>\\]+`)

	// dsnHost matches the `@host` of a connection string —
	// "user:pass@tcp(db.example.com:3306)/schema",
	// "postgres://u:p@db.example.com:5432/x", "amqp://u:p@broker.example.com/".
	// The optional `driver(` group is Go's MySQL DSN form; without it the
	// driver name itself is reported as the host, which is not wrong (the real
	// host is still reported by the host:port rule) but makes the refusal
	// message name something that is not a host.
	dsnHost = regexp.MustCompile(`@(?:[A-Za-z0-9_]+\()?(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9_.\-]+)`)

	// bracketedHostPort matches an IPv6 literal with a port, the shape
	// otelcol's client.endpoint takes for a v6 address and the shape the
	// previous regex could not express at all.
	bracketedHostPort = regexp.MustCompile(`\[[0-9A-Fa-f:.]+\]:[0-9]{1,5}`)

	// hostPort matches a bare host:port. The host must START WITH A LETTER so
	// that a clock ("12:30"), a ratio ("1:4") or a duration-ish string is not
	// read as a host — those carry no letters at all in their first segment,
	// and a real host name that begins with a digit is rare enough that losing
	// it costs a disclosure, not a leak.
	hostPort = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9_.\-]*:[0-9]{1,5}\b`)

	// bareName matches a dotted host name with no port and no scheme, and is
	// applied to the WHOLE literal only: as a substring rule it would fire on
	// every regex, file name and label value in a relabel stage. Two or more
	// labels, a final label of at least two ASCII letters, no underscore and no
	// regex metacharacter anywhere.
	bareName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9\-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9\-]*[A-Za-z0-9])?)*\.[A-Za-z]{2,}$`)

	// ipv4 matches a dotted-quad, which bareName's letter-final-label rule
	// deliberately misses. An IP literal is the most direct address form there
	// is, so it gets its own rule rather than a loosened bareName.
	ipv4 = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)
)

// Hosts returns every host name the literal could reach, lowercased and
// deduplicated in first-seen order. An empty result means the literal names no
// host under any of the shapes above — which is the only way a caller may treat
// a literal as safe.
//
// Callers must treat a NON-empty result as "these must all be allowed", not as
// "one of these is the host": a DSN carrying two hosts must clear the allowlist
// twice.
//
// Known and deliberate limits, both answered by the network rather than here:
//
//   - a SINGLE-LABEL name with no port ("vault", "broker") is not reported. It
//     is indistinguishable from a job name, a label value or a relabel action,
//     and refusing every bare word would refuse every config.
//   - an address the config COMPUTES — assembled by a relabel rule from label
//     data, or concatenated — is not a literal at all and cannot be reported by
//     any function of this signature.
func Hosts(literal string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(h string) {
		h = strings.ToLower(strings.Trim(h, "[]."))
		if h == "" || seen[h] {
			return
		}
		seen[h] = true
		out = append(out, h)
	}

	for _, match := range schemeURL.FindAllString(literal, -1) {
		u, err := url.Parse(match)
		if err != nil || u.Hostname() == "" {
			// A URL that will not parse cannot be shown to name an allowed
			// host, so it names a disallowed one. Reporting the raw match keeps
			// the refusal message specific.
			add(match)
			continue
		}
		add(u.Hostname())
	}
	for _, match := range dsnHost.FindAllStringSubmatch(literal, -1) {
		add(match[1])
	}
	for _, match := range bracketedHostPort.FindAllString(literal, -1) {
		add(match[:strings.LastIndex(match, ":")])
	}
	for _, match := range hostPort.FindAllString(literal, -1) {
		add(match[:strings.LastIndex(match, ":")])
	}
	for _, match := range ipv4.FindAllString(literal, -1) {
		add(match)
	}
	// The trailing dot is the DNS root label: "api-server.example.com." and
	// "api-server.example.com" resolve to the same machine, and SRV-record
	// discovery emits the first form. Stripping it here rather than loosening
	// bareName keeps the anchored match strict.
	if bareName.MatchString(strings.TrimSuffix(literal, ".")) {
		add(literal)
	}
	return out
}
