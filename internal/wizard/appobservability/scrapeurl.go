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
// parameter that repeats, has no value or whose name cannot be a label, a
// path escape Prometheus cannot reproduce (%2F), a mistyped scheme separator
// ("http:/host"), an unbracketed IPv6 host, and any control or whitespace
// character.
func parseScrapeURL(raw string) (scrapeTarget, error) {
	s := strings.TrimSpace(raw)
	fail := func(format string, args ...any) (scrapeTarget, error) {
		return scrapeTarget{}, fmt.Errorf("scrape_url %q: "+format, append([]any{raw}, args...)...)
	}
	if strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fail("must not contain spaces or control characters")
	}
	if !strings.Contains(s, "://") {
		// "http:/host" is a scheme typo, not a bare host: prefixing it
		// would parse as host "http" with a garbage port, or worse, as
		// something valid that is not what was meant.
		if strings.Contains(s, ":/") {
			return fail("looks like a mistyped scheme separator (want e.g. http://myapp:9090/metrics)")
		}
		s = "http://" + s
	}
	// An IPv6 literal must be bracketed in a URL: "::1:9090" cannot say
	// where the address ends and the port begins.
	hostPort := strings.SplitN(s, "://", 2)[1]
	if i := strings.IndexAny(hostPort, "/?#"); i >= 0 {
		hostPort = hostPort[:i]
	}
	if i := strings.LastIndex(hostPort, "@"); i >= 0 {
		hostPort = hostPort[i+1:]
	}
	if strings.Count(hostPort, ":") > 1 && !strings.HasPrefix(hostPort, "[") {
		return fail("an IPv6 address must be in brackets, e.g. http://[::1]:9090/metrics")
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

	// __metrics_path__ takes the DECODED path: Prometheus sets it as
	// url.URL.Path and escapes it again when it builds the request. So an
	// escape whose decoding changes the path's meaning — %2F, an encoded
	// slash, is the case that matters — cannot be sent as written either
	// way: given decoded, /a%2Fb goes out as /a/b; given escaped, as
	// /a%252Fb (both observed in Alloy v1.20.1's target debug info). url
	// sets RawPath exactly when the path's escaping is not the default one,
	// so that is the case refused, rather than scraping a different URL.
	if !printable(u.Path) {
		return fail("path decodes to a control character or invalid UTF-8")
	}
	if u.RawPath != "" {
		return fail("path %q holds an encoded character (such as %%2F) that Prometheus cannot send as written", u.EscapedPath())
	}
	path := u.Path
	if path == "" {
		path = "/metrics"
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
		// Prometheus leaves an empty __param_* out of the scrape URL
		// (Alloy v1.20.1: `__param_debug = ""` scrapes /m, not /m?debug),
		// so a flag-style parameter would silently vanish.
		if values[0] == "" {
			return fail("query parameter %q has no value, and Prometheus drops an empty parameter from the scrape URL — give it one (e.g. %s=1)", name, name)
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
