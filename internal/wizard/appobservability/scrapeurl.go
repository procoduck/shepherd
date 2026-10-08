package appobservability

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// scrapeTarget is a metrics endpoint URL split into the target labels
// Prometheus builds its scrape URL from.
//
// A Prometheus target's __address__ is host:port and nothing else; the
// scheme, path and query travel in __scheme__, __metrics_path__ and
// __param_<name>. This wizard once put the whole URL in __address__. Alloy
// loads that config without complaint (the collector reports APPLIED), then
// refuses the target at run time — "Creating target failed … "http://…" is
// not a valid hostname" — and the job scrapes nothing (2026-10-08
// walkthrough).
type scrapeTarget struct {
	address     string
	scheme      string
	metricsPath string
	params      map[string]string
}

// paramName is what a query parameter's name may be: __param_<name> becomes
// a target label, and a label name outside this set is refused by the
// scrape manager at run time, the same silent failure this type exists to
// prevent.
var paramName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseScrapeURL parses the scrape_url field. It takes a full http(s) URL; a
// bare host[:port] — the only form the old renderer handled, and what some
// stored wizard states hold — is read as http://host[:port]/metrics. The port
// defaults by scheme (80/443), an empty path to /metrics (Prometheus' own
// default), and each query parameter becomes a __param_<name> label, which
// Prometheus adds back to the scrape URL.
//
// Refused, each with the reason: another scheme, no host, a bad port,
// credentials in the URL (they would be stored in the pipeline as plain
// text, and the wizard has no scrape-auth option to put them in instead), a
// fragment (never sent to a server, so almost certainly a mistake), a query
// parameter that repeats or whose name cannot be a label, and any control or
// whitespace character.
func parseScrapeURL(raw string) (scrapeTarget, error) {
	s := strings.TrimSpace(raw)
	fail := func(format string, args ...any) (scrapeTarget, error) {
		return scrapeTarget{}, fmt.Errorf("scrape_url %q: "+format, append([]any{raw}, args...)...)
	}
	if strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fail("must not contain spaces or control characters")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return fail("is not a valid URL (want e.g. http://myapp:9090/metrics): %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fail("scheme must be http or https, got %q", u.Scheme)
	}
	if u.Opaque != "" || u.Hostname() == "" {
		return fail("has no host (want e.g. http://myapp:9090/metrics)")
	}
	if u.User != nil {
		return fail("must not carry credentials: they would be stored in the pipeline as plain text")
	}
	if u.Fragment != "" || strings.HasSuffix(s, "#") {
		return fail("must not have a #fragment: it is never sent to the app")
	}
	if strings.ContainsAny(u.Hostname(), `"\`) {
		return fail("host contains a quote or backslash")
	}

	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fail("port %q is not a number from 1 to 65535", port)
	}

	path := u.Path
	if path == "" {
		path = "/metrics"
	}
	if !printable(path) {
		return fail("path decodes to a control character or invalid UTF-8")
	}

	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fail("query is not valid: %v", err)
	}
	params := make(map[string]string, len(query))
	for name, values := range query {
		if !paramName.MatchString(name) {
			return fail("query parameter %q: a name may only hold letters, digits and _ (and not start with a digit)", name)
		}
		if len(values) != 1 {
			return fail("query parameter %q is given %d times; give it once", name, len(values))
		}
		if !printable(values[0]) {
			return fail("query parameter %q decodes to a control character or invalid UTF-8", name)
		}
		params[name] = values[0]
	}

	return scrapeTarget{
		address:     net.JoinHostPort(u.Hostname(), port),
		scheme:      u.Scheme,
		metricsPath: path,
		params:      params,
	}, nil
}

// printable reports whether s is valid UTF-8 with no control characters —
// what labels() needs to quote it as an Alloy string literal unchanged.
func printable(s string) bool {
	return utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

// labels renders the target as the body of an Alloy object literal, one
// label per line, indented for prometheus.scrape's targets list.
func (t scrapeTarget) labels() string {
	type kv struct{ k, v string }
	pairs := []kv{
		{"__address__", t.address},
		{"__scheme__", t.scheme},
		{"__metrics_path__", t.metricsPath},
	}
	names := make([]string, 0, len(t.params))
	for name := range t.params {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		pairs = append(pairs, kv{"__param_" + name, t.params[name]})
	}
	width := 0
	for _, p := range pairs {
		width = max(width, len(strconv.Quote(p.k)))
	}
	var sb strings.Builder
	sb.WriteString("\n")
	for _, p := range pairs {
		// strconv.Quote on a string with no control characters (refused by
		// parseScrapeURL) escapes only `"` and `\`, both of which an Alloy
		// string literal reads back the same way.
		_, _ = fmt.Fprintf(&sb, "    %-*s = %s,\n", width, strconv.Quote(p.k), strconv.Quote(p.v))
	}
	sb.WriteString("  ")
	return sb.String()
}
