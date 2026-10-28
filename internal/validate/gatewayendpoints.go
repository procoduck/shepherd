package validate

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"net/url"
	pathpkg "path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/grafana/alloy/syntax/ast"
	"github.com/grafana/alloy/syntax/token"
	"golang.org/x/net/idna"
	"gopkg.in/yaml.v3"
)

// Strict tenant mode checks gateway route endpoints. Shepherd's tenant
// routes (internal/gateway: RenderHTTPRoute, PathPrefix) set the tenant
// themselves: a request to the gateway under /otlp/<segment> or
// /faro/<segment> is sent on with the X-Scope-OrgID of the route's tenant,
// whatever the request carried. The routes name no host, so the gateway
// routes such a request however it is reached (an address, another name, a
// Host header). Segments are unique across the installation and never reused
// (0009_tenant_routes: UNIQUE (kind, segment)), so the rule does not depend
// on the host: in a strict org every endpoint, on any host, whose path is
// under a route kind and a string with a route segment's shape must be under
// one of the org's own routes (foreignRoute).
//
// The gateway's hosts, when configured (gateway.routes.public_base_url's
// host and gateway.routes.hosts, internal/tenancy), add a stricter rule
// there: an endpoint on them must be under one of the org's routes, written
// plainly; a proxy or a Host header naming them is refused. An endpoint the
// lint cannot read cannot be proved off another org's route, so it is
// refused whenever the installation has a live tenant route or a configured
// gateway host (checkUnread); with neither, nothing can route by segment.
//
// Which attributes are endpoints is read from every shipped component
// schema, not a hand list: every string or list attribute whose name says it
// is a URL, endpoint, address, host or server (endpointNameRE) is an
// endpoint unless an exclusion below says why it is not (a listen address,
// a TLS server name, a protocol other than HTTP).

// GatewayEndpointRefusal is the phrase every refusal of this check carries.
const GatewayEndpointRefusal = "strict tenant mode checks gateway route endpoints"

// GatewayRoutes is the tenant-route gateway as the lint sees it.
type GatewayRoutes struct {
	// Hosts are the host names and addresses the gateway answers to,
	// normalised (NormalizeGatewayHost). Empty: only the segment rule
	// applies.
	Hosts []string
	// Owns reports whether the org owns the route of kind ("otlp" or
	// "faro") with this segment. A nil Owns owns none.
	Owns func(kind, segment string) bool
	// RoutesInUse is whether the installation has a live (not revoked)
	// tenant route: an endpoint the lint cannot read is then refused.
	RoutesInUse bool
}

func (g GatewayRoutes) configured() bool { return len(g.Hosts) > 0 }

// checkUnread is whether an endpoint the lint cannot read is refused: some
// tenant route may be reached through it.
func (g GatewayRoutes) checkUnread() bool { return g.RoutesInUse || g.configured() }

func (g GatewayRoutes) owns(kind, segment string) bool { return g.Owns != nil && g.Owns(kind, segment) }

// routeKinds are the first path element of a tenant route
// (gateway.RouteSpec.PathPrefix: "/{kind}/{segment}").
var routeKinds = map[string]bool{"otlp": true, "faro": true}

// EndpointAttrKind is how strict tenant mode treats one endpoint attribute.
type EndpointAttrKind int

const (
	// EndpointNone is an attribute whose name does not say it is an
	// endpoint: not checked.
	EndpointNone EndpointAttrKind = iota
	// EndpointUnclassified is never returned for a name endpointNameRE
	// matches; it is the zero of a failed lookup, and the lint treats it as
	// EndpointURL.
	EndpointUnclassified
	// EndpointURL is a URL or host[:port] the component sends requests to.
	EndpointURL
	// EndpointProxy is a proxy the component sends its requests through:
	// refused on the gateway, whose routes are not a proxy.
	EndpointProxy
	// EndpointDomain is a domain the component sends requests to hosts under
	// (Datadog's api site).
	EndpointDomain
	// EndpointExcluded is not an endpoint the component sends data to; the
	// reason says why.
	EndpointExcluded
)

// endpointNameRE is the attribute names that say they are an endpoint.
var endpointNameRE = regexp.MustCompile(`(^|_)(url|uri|uris|endpoint|address|addr|host|hostname|hostnames|server|servers|broker|brokers)($|_)|^api_server$|^site$|^universe_domain$|^connection_string$`)

const (
	listens        = "where the component listens, not where it sends"
	notHTTP        = "speaks a protocol other than HTTP, which the gateway's routes do not serve"
	notAnEndpoint  = "names no host the component sends requests to"
	tlsServerName  = "the TLS server name checked on the certificate; the connection goes to the endpoint, which is checked"
	receiverFamily = "a receiver: its endpoints are where it listens or where it reads data from, and it sends no data to them"
)

// endpointNameExclusions are excluded by name in any component.
var endpointNameExclusions = map[string]string{
	"listen_address":        listens,
	"logs_url_path":         "a path the receiver serves",
	"metrics_url_path":      "a path the receiver serves",
	"traces_url_path":       "a path the receiver serves",
	"server_name":           tlsServerName,
	"tls_server_name":       tlsServerName,
	"hostname_source":       notAnEndpoint,
	"host_networking_host":  "the address recorded for containers on the host network, a target label",
	"host_override":         "the Host header value sent; the connection goes to scrape_uri, which is checked",
	"override_hostname":     notAnEndpoint,
	"endpoint_partition_id": notAnEndpoint,
	"endpoint_patterns":     notAnEndpoint,
	"url_path_patterns":     notAnEndpoint,
	"iam_server_id_header":  "a Vault login header value",
	// Revisit if a route kind ever accepts an arbitrary request body.
	"token_url": "the OAuth2 token endpoint: the client sends it only a form-encoded token request, which no " +
		"OTLP or Faro route accepts; the endpoint the data goes to is checked",
	"host_identifiers":  "names resource attributes, not a host",
	"connection_string": "a database or Event Hubs connection; " + notHTTP,
	"mongodb_uri":       "a MongoDB connection; " + notHTTP,
	"brokers":           "Kafka brokers; " + notHTTP,
	"kafka_uris":        "Kafka brokers; " + notHTTP,
	"zookeeper_uris":    "ZooKeeper; " + notHTTP,
	"redis_addr":        "Redis; " + notHTTP,
	"relay_addr":        "a StatsD relay over UDP; " + notHTTP,
}

// endpointFamilyExclusions are excluded for every attribute of a family.
var endpointFamilyExclusions = map[string]string{
	"otelcol.receiver.": receiverFamily,
}

// endpointExclusions are excluded at one component and block (the last
// block of the path inside the component, "" for its body).
var endpointExclusions = map[string]map[string]map[string]string{
	"loki.source.syslog":                       {"listener": {"address": listens}},
	"otelcol.extension.jaeger_remote_sampling": {"grpc": {"endpoint": listens}, "http": {"endpoint": listens}},
	"otelcol.exporter.datadog":                 {"": {"hostname": "the host name reported to Datadog, not a host requests go to"}},
	"otelcol.exporter.splunkhec":               {"otel_attrs_to_hec_metadata": {"host": "names the attribute HEC's host field is taken from"}},
	"otelcol.exporter.syslog":                  {"": {"endpoint": "a syslog server; " + notHTTP}},
	"otelcol.processor.resourcedetection":      {"system": {"hostname_sources": notAnEndpoint}},
	"prometheus.exporter.memcached":            {"": {"address": "memcached; " + notHTTP}},
	"prometheus.exporter.dnsmasq":              {"": {"address": "a DNS server; " + notHTTP}},
	"prometheus.exporter.snmp":                 {"target": {"address": "an SNMP agent over UDP; " + notHTTP}},
	"prometheus.exporter.unix":                 {"ntp": {"server": "an NTP server over UDP; " + notHTTP}},
	"remote.vault":                             {"auth.azure": {"resource_url": "the Azure resource a token is requested for, not a host requests go to"}},
}

// endpointKindsByName are endpoints of a kind other than EndpointURL.
var endpointKindsByName = map[string]EndpointAttrKind{
	"proxy_url":       EndpointProxy,
	"site":            EndpointDomain,
	"universe_domain": EndpointDomain,
}

// ClassifyEndpointAttribute classifies attribute attr of component, directly
// in block (the last block of the path inside the component, "" for its
// body), and says why when it is excluded.
func ClassifyEndpointAttribute(component, block, attr string) (EndpointAttrKind, string) {
	kind, reason, _ := classifyEndpoint(component, block, attr)
	return kind, reason
}

func classifyEndpoint(component, block, attr string) (EndpointAttrKind, string, string) {
	if !endpointNameRE.MatchString(attr) {
		return EndpointNone, "", ""
	}
	if r, ok := endpointExclusions[component][block][attr]; ok {
		return EndpointExcluded, r, component + "/" + block + "/" + attr
	}
	if r, ok := endpointNameExclusions[attr]; ok {
		return EndpointExcluded, r, "name/" + attr
	}
	for f, r := range endpointFamilyExclusions {
		if strings.HasPrefix(component, f) {
			return EndpointExcluded, r, "family/" + f
		}
	}
	if k, ok := endpointKindsByName[attr]; ok {
		return k, "", ""
	}
	return EndpointURL, "", ""
}

// EndpointExclusionKey names the exclusion rule that excludes attr (for the
// spec that every rule still matches a shipped attribute); "" when none does.
func EndpointExclusionKey(component, block, attr string) string {
	_, _, k := classifyEndpoint(component, block, attr)
	return k
}

// EndpointExclusionKeys lists every exclusion rule's key.
func EndpointExclusionKeys() []string {
	var out []string
	for c, blocks := range endpointExclusions {
		for b, attrs := range blocks {
			for a := range attrs {
				out = append(out, c+"/"+b+"/"+a)
			}
		}
	}
	for a := range endpointNameExclusions {
		out = append(out, "name/"+a)
	}
	for f := range endpointFamilyExclusions {
		out = append(out, "family/"+f)
	}
	sort.Strings(out)
	return out
}

// SchemaEndpointAttribute is one string or list attribute of a shipped
// component schema whose name says it is an endpoint.
type SchemaEndpointAttribute = SchemaMapAttribute

// SchemaEndpointAttributes lists every attribute of every component in every
// schema in fsys whose name says it is an endpoint (endpointNameRE).
func SchemaEndpointAttributes(fsys fs.FS) ([]SchemaEndpointAttribute, error) {
	seen := map[SchemaEndpointAttribute]bool{}
	var out []SchemaEndpointAttribute
	err := eachSchemaComponent(fsys, func(name string, c schemaComponent) {
		collectEndpointAttributes(name, "", c.Attributes, c.Blocks, func(a SchemaEndpointAttribute) {
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		})
	})
	return out, err
}

func collectEndpointAttributes(component, path string, attrs []schemaAttr, blocks []schemaBlock, add func(SchemaEndpointAttribute)) {
	for _, a := range attrs {
		switch a.Type {
		case "string", "list", "secret":
			if endpointNameRE.MatchString(a.Name) {
				add(SchemaEndpointAttribute{Component: component, Path: path, Attr: a.Name, Type: a.Type})
			}
		}
	}
	for _, b := range blocks {
		p := b.Name
		if path != "" {
			p = path + "/" + b.Name
		}
		collectEndpointAttributes(component, p, b.Attributes, b.Blocks, add)
	}
}

var (
	endpointAttrsOnce sync.Once
	endpointAttrs     map[string]map[string]map[string]EndpointAttrKind
	endpointAttrsErr  error
)

// endpointAttributes is every shipped schema's endpoint attributes as the
// lint looks them up: component -> last block -> attribute -> kind (not
// excluded ones). Were two paths with the same last block to disagree, an
// endpoint wins over an exclusion.
func endpointAttributes() (map[string]map[string]map[string]EndpointAttrKind, error) {
	endpointAttrsOnce.Do(func() {
		idx, err := loadedSchemaIndex()
		if err != nil {
			endpointAttrsErr = err
			return
		}
		out := map[string]map[string]map[string]EndpointAttrKind{}
		for _, a := range idx.endpointAttrs {
			kind, _ := ClassifyEndpointAttribute(a.Component, a.Block(), a.Attr)
			if kind == EndpointExcluded || kind == EndpointNone {
				continue
			}
			if out[a.Component] == nil {
				out[a.Component] = map[string]map[string]EndpointAttrKind{}
			}
			if out[a.Component][a.Block()] == nil {
				out[a.Component][a.Block()] = map[string]EndpointAttrKind{}
			}
			out[a.Component][a.Block()][a.Attr] = kind
		}
		endpointAttrs = out
	})
	return endpointAttrs, endpointAttrsErr
}

// hasEndpointAttribute reports whether a shipped schema gives component an
// endpoint attribute (the gateway_endpoints check covers it).
func hasEndpointAttribute(component string) bool {
	m, err := endpointAttributes()
	if err != nil {
		return false
	}
	return len(m[component]) > 0 || endpointTargets[component] != ""
}

// endpointTargets are the components whose targets list names endpoints,
// by the target key that holds one.
var endpointTargets = map[string]string{blackboxExporter: "address"}

// NormalizeGatewayHost is a configured gateway host as the lint compares
// hosts: lower case, the trailing dot dropped, an IDN in its ASCII form, an
// IP address in its canonical form. A URL, a port or a path is refused: the
// check compares hosts, on every port.
func NormalizeGatewayHost(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("empty host")
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		s = s[1 : len(s)-1]
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap().String(), nil
	}
	if strings.ContainsAny(s, "/:?#@ \t\\%[]") {
		return "", fmt.Errorf("%q is not a host name or IP address (no scheme, port or path)", raw)
	}
	return normalizeHostName(s), nil
}

// normalizeHostName is a host name as a client connects to it (net/http's
// idnaASCII: ASCII as is, else IDNA lookup form; a name it cannot convert
// is used as written), lower-cased with trailing dots dropped.
func normalizeHostName(h string) string {
	if !isASCII(h) {
		if a, err := idna.Lookup.ToASCII(h); err == nil {
			h = a
		}
	}
	return strings.TrimRight(strings.ToLower(h), ".")
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// hostIsGateway reports whether host (as a URL spells it) is one of hosts.
func hostIsGateway(host string, hosts []string) bool {
	h := strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if h == "" {
		return false
	}
	var cands []string
	if a, err := netip.ParseAddr(h); err == nil {
		cands = append(cands, a.Unmap().WithZone("").String())
	} else {
		n := normalizeHostName(h)
		cands = append(cands, n)
		// A resolver may read a number-and-dot name the way inet_aton
		// does (127.1, 0x7f.1, 2130706433).
		if a, ok := inetAton(n); ok {
			cands = append(cands, a.String())
		}
	}
	for _, c := range cands {
		for _, g := range hosts {
			if c == g {
				return true
			}
		}
	}
	return false
}

// inetAton parses s as BSD inet_aton does: one to four parts, each decimal,
// octal (leading 0) or hex (0x), the last filling the remaining bytes.
func inetAton(s string) (netip.Addr, bool) {
	parts := strings.Split(s, ".")
	if len(parts) == 0 || len(parts) > 4 {
		return netip.Addr{}, false
	}
	vals := make([]uint64, len(parts))
	for i, p := range parts {
		if p == "" {
			return netip.Addr{}, false
		}
		v, err := strconv.ParseUint(p, 0, 32)
		if err != nil {
			return netip.Addr{}, false
		}
		vals[i] = v
	}
	var n uint64
	for i := 0; i < len(vals)-1; i++ {
		if vals[i] > 0xff {
			return netip.Addr{}, false
		}
		n |= vals[i] << (8 * (3 - i))
	}
	last := vals[len(vals)-1]
	if last >= 1<<(8*(4-len(vals)+1)) {
		return netip.Addr{}, false
	}
	n |= last
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(n)) //nolint:gosec // n < 1<<32: every part is bounded above
	return netip.AddrFrom4(b), true
}

// endpointTarget is one endpoint as the lint reads it: the hosts it may
// connect to, and, for an http(s) or scheme-less endpoint, the path it
// sends to.
type endpointTarget struct {
	hosts []string
	// routed: path is the request path (http, https or no scheme).
	routed bool
	path   string
	// rawPath is set when the path is percent-encoded.
	encoded bool
}

// parseEndpoint reads s as a URL (scheme://…) or a gRPC target or
// host[:port][/path], as Go's url.Parse (which Alloy's clients use) does.
func parseEndpoint(s string) (endpointTarget, error) {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	for _, p := range []string{"unix:", "unix-abstract:", "unixpacket:"} {
		if strings.HasPrefix(lower, p) {
			return endpointTarget{}, nil
		}
	}
	if i := strings.Index(s, "://"); i > 0 {
		u, err := url.Parse(s)
		if err != nil {
			return endpointTarget{}, err
		}
		t := endpointTarget{hosts: []string{u.Hostname()}}
		switch u.Scheme {
		case "http", "https":
			t.routed = true
			t.path = u.Path
			t.encoded = u.RawPath != "" || strings.Contains(u.EscapedPath(), "%")
		default:
			// A gRPC target (dns:///host:port, passthrough:///host:port) or
			// another scheme: the host may be in the path.
			if p := strings.TrimLeft(u.Path, "/"); p != "" {
				h := p
				if j := strings.IndexByte(h, '/'); j >= 0 {
					h = h[:j]
				}
				if hu, err := url.Parse("//" + h); err == nil {
					t.hosts = append(t.hosts, hu.Hostname())
				}
			}
		}
		return t, nil
	}
	if strings.HasPrefix(lower, "dns:") {
		s = s[len("dns:"):]
	}
	u, err := url.Parse("//" + s)
	if err != nil {
		return endpointTarget{}, err
	}
	return endpointTarget{
		hosts: []string{u.Hostname()}, routed: true, path: u.Path,
		encoded: u.RawPath != "" || strings.Contains(u.EscapedPath(), "%"),
	}, nil
}

// segmentShapeRE and the length bounds are gateway.ValidateSegment's (a
// spec pins the two together): the shape of every tenant route segment.
var segmentShapeRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// IsRouteSegmentShape reports whether s has the shape of a tenant route
// segment (gateway.ValidateSegment).
func IsRouteSegmentShape(s string) bool {
	return len(s) >= 3 && len(s) <= 63 && segmentShapeRE.MatchString(s)
}

// pathForms are the forms a gateway may read path p in: as written, with
// backslashes as slashes, and each with empty, "." and ".." segments
// resolved (path.Clean). p is already percent-decoded (url.URL.Path).
func pathForms(p string) []string {
	forms := []string{p}
	if strings.Contains(p, "\\") {
		forms = append(forms, strings.ReplaceAll(p, "\\", "/"))
	}
	for _, f := range forms[:len(forms):len(forms)] {
		forms = append(forms, pathpkg.Clean("/"+f))
	}
	return forms
}

// foreignRoute says why path p is under a tenant route the org does not own,
// or "": in any form a gateway may read it in (pathForms, case folded), its
// first two elements are a route kind and a string with a route segment's
// shape that is not one of the org's routes of that kind. Segments are
// unique across the installation and never reused, so this holds on every
// host, however the request reaches the gateway.
func (g GatewayRoutes) foreignRoute(p string) string {
	for _, f := range pathForms(p) {
		segs := strings.SplitN(strings.TrimPrefix(f, "/"), "/", 3)
		if len(segs) < 2 {
			continue
		}
		kind, seg := strings.ToLower(segs[0]), strings.ToLower(segs[1])
		if routeKinds[kind] && IsRouteSegmentShape(seg) && !g.owns(kind, seg) {
			return fmt.Sprintf("its path is under the %s route segment %q, which is not one of this organisation's "+
				"tenant routes", kind, seg)
		}
	}
	return ""
}

// endpointProblem says why strict tenant mode refuses endpoint s of kind, or
// "".
func (g GatewayRoutes) endpointProblem(s string, kind EndpointAttrKind) string {
	if kind == EndpointDomain {
		d := normalizeHostName(strings.TrimSpace(s))
		for _, h := range g.Hosts {
			if d != "" && (h == d || strings.HasSuffix(h, "."+d)) {
				return fmt.Sprintf("%q is the gateway's domain (%s), and requests go to hosts under it", s, h)
			}
		}
		return ""
	}
	t, err := parseEndpoint(s)
	if err != nil {
		if !g.checkUnread() {
			return ""
		}
		return fmt.Sprintf("%q cannot be read as a URL or host:port, so the tenant check cannot tell which route "+
			"it reaches", s)
	}
	onGateway := false
	for _, h := range t.hosts {
		if hostIsGateway(h, g.Hosts) {
			onGateway = true
		}
	}
	if kind == EndpointProxy {
		if onGateway {
			return fmt.Sprintf("%q sends requests through the gateway, whose tenant routes are not a proxy", s)
		}
		return ""
	}
	if t.routed {
		if p := g.foreignRoute(t.path); p != "" {
			return fmt.Sprintf("%q: %s", s, p)
		}
	}
	if !onGateway {
		return ""
	}
	if !t.routed {
		return fmt.Sprintf("%q is on the gateway but names no route path, so it reaches no route of this "+
			"organisation", s)
	}
	if t.encoded || strings.Contains(t.path, "\\") || strings.Contains(t.path, "//") {
		return fmt.Sprintf("%q is on the gateway, and its path is not written plainly (percent-encoding, a "+
			"backslash or an empty segment), so the tenant check cannot tell which route it reaches", s)
	}
	segs := strings.Split(strings.TrimPrefix(t.path, "/"), "/")
	for _, seg := range segs {
		if seg == "." || seg == ".." {
			return fmt.Sprintf("%q is on the gateway, and its path has a %q segment, so the tenant check cannot "+
				"tell which route it reaches", s, seg)
		}
	}
	if len(segs) < 2 || !routeKinds[segs[0]] || segs[1] == "" {
		return fmt.Sprintf("%q is on the gateway but not under a tenant route (/otlp/<segment> or "+
			"/faro/<segment>) of this organisation", s)
	}
	if !g.owns(segs[0], segs[1]) {
		return fmt.Sprintf("%q is on the gateway under the %s route %q, which is not one of this organisation's "+
			"routes", s, segs[0], segs[1])
	}
	return ""
}

// endpointKind is attribute attr's endpoint kind in block of component.
func endpointKind(component, block, attr string) EndpointAttrKind {
	m, err := endpointAttributes()
	if err != nil {
		// runLint read the schemas first; unreadable, every attribute named
		// like an endpoint is one (fail closed).
		if endpointNameRE.MatchString(attr) {
			return EndpointURL
		}
		return EndpointNone
	}
	return m[component][block][attr]
}

// gatewayEndpoint checks attribute a, in the component the walk is in, when
// the schemas make it an endpoint.
func (l *tenantLinter) gatewayEndpoint(a *ast.AttributeStmt, sc scope) {
	if l.policy.literalsOnly {
		return
	}
	owner, block := sc.ownerBlock()
	if owner == "" {
		return
	}
	parts := []string{owner}
	if block != "" {
		parts = append(parts, block)
	}
	where := strings.Join(append(parts, a.Name.Name), " ")
	if key := endpointTargets[owner]; key != "" && block == "" && a.Name.Name == "targets" {
		l.endpointTargetList(a.Value, key, where, sc)
		return
	}
	kind := endpointKind(owner, block, a.Name.Name)
	if kind == EndpointNone || kind == EndpointExcluded {
		return
	}
	if arr, ok := unparen(a.Value).(*ast.ArrayExpr); ok {
		for _, el := range arr.Elements {
			l.endpointValue(el, kind, where, sc)
		}
		return
	}
	l.endpointValue(a.Value, kind, where, sc)
}

// unreadRefusal is the refusal of an endpoint value the lint cannot read.
func unreadRefusal(where string) string {
	return fmt.Sprintf("%s is set from a value the tenant check cannot read, so it cannot tell which tenant route "+
		"it reaches — %s, and refuses it; write the endpoint as a literal", where, GatewayEndpointRefusal)
}

// endpointTargetList checks a targets list whose key names an endpoint: it
// must be a list of object literals (when unread values are refused,
// checkUnread), each key read as an endpoint.
func (l *tenantLinter) endpointTargetList(v ast.Expr, key, where string, sc scope) {
	g := l.policy.Gateway
	arr, ok := unparen(v).(*ast.ArrayExpr)
	if !ok {
		if g.checkUnread() {
			l.refuseAs(v, FindingRoute, fmt.Sprintf("%s is not a literal list, so the tenant check cannot tell which tenant route a "+
				"target reaches — %s, and refuses it; write the targets as a literal list", where, GatewayEndpointRefusal))
		}
		return
	}
	for _, el := range arr.Elements {
		obj, ok := unparen(el).(*ast.ObjectExpr)
		if !ok {
			if g.checkUnread() {
				l.refuseAs(el, FindingRoute, fmt.Sprintf("%s has a target that is not an object literal, so the tenant check cannot "+
					"tell which tenant route it reaches — %s, and refuses it", where, GatewayEndpointRefusal))
			}
			continue
		}
		for _, f := range obj.Fields {
			if f.Name.Name == key {
				l.endpointValue(f.Value, EndpointURL, where+" "+key, sc)
			}
		}
	}
}

// endpointValue checks one endpoint value: every string it can be (resolve,
// declare and module arguments followed to literals). One it cannot read is
// refused when the installation uses tenant routes (checkUnread) unless the
// literal start of a concatenation already fixes its whole path
// (provenPath).
func (l *tenantLinter) endpointValue(v ast.Expr, kind EndpointAttrKind, where string, sc scope) {
	g := l.policy.Gateway
	values, ok := l.resolve(v, sc, 0)
	if !ok {
		if !g.checkUnread() || l.provenPath(v, kind, sc) {
			return
		}
		l.refuseAs(v, FindingRoute, unreadRefusal(where))
		return
	}
	for _, s := range values {
		if !l.budget.charge(1 + len(s)/64) {
			return
		}
		if p := g.endpointProblem(s, kind); p != "" {
			l.refuseAs(v, FindingRoute, fmt.Sprintf("%s: %s — %s, and refuses it; send to one of this organisation's tenant routes",
				where, p, GatewayEndpointRefusal))
		}
	}
}

// provenPath reports whether an endpoint the lint cannot read in full still
// cannot reach another org's route: v is a concatenation whose leading
// operands it can read, and each value they can be is an http(s) URL whose
// path is complete in it (a "?" or "#" follows the path) and passes
// endpointProblem. A path not complete in the literal start proves nothing:
// what follows may add any element, ".." included.
func (l *tenantLinter) provenPath(v ast.Expr, kind EndpointAttrKind, sc scope) bool {
	if kind != EndpointURL {
		return false
	}
	var operands []ast.Expr
	var flatten func(e ast.Expr, depth int) bool
	flatten = func(e ast.Expr, depth int) bool {
		if depth > maxNesting {
			return false
		}
		if b, ok := unparen(e).(*ast.BinaryExpr); ok && b.Kind == token.ADD {
			return flatten(b.Left, depth+1) && flatten(b.Right, depth+1)
		}
		operands = append(operands, e)
		return true
	}
	if !flatten(v, 0) || len(operands) < 2 {
		return false
	}
	prefixes := []string{""}
	for _, op := range operands {
		vals, ok := l.resolve(op, sc, 0)
		if !ok {
			break
		}
		if len(prefixes)*len(vals) > maxFolded {
			return false
		}
		var next []string
		for _, p := range prefixes {
			for _, s := range vals {
				if len(p)+len(s) > maxValueBytes {
					return false
				}
				next = append(next, p+s)
			}
		}
		prefixes = next
	}
	for _, p := range prefixes {
		if !l.prefixProven(p) {
			return false
		}
	}
	return true
}

// prefixProven reports whether every URL starting with prefix p has the
// path p fixes, and that endpoint passes the check.
func (l *tenantLinter) prefixProven(p string) bool {
	i := strings.Index(p, "://")
	if i <= 0 {
		return false
	}
	scheme := strings.ToLower(p[:i])
	if scheme != "http" && scheme != "https" {
		return false
	}
	rest := p[i+3:]
	end := strings.IndexAny(rest, "?#")
	if end < 0 {
		return false
	}
	return l.policy.Gateway.endpointProblem(p[:i+3+end], EndpointURL) == ""
}

// hostHeader checks a Host (or :authority) request header: one naming a
// gateway host is refused (point the endpoint at the gateway instead, where
// its route is checked), and one the lint cannot read is refused when the
// gateway's hosts are configured.
func (l *tenantLinter) hostHeader(v ast.Expr, sc scope) {
	g := l.policy.Gateway
	if l.policy.literalsOnly || !g.configured() {
		return
	}
	values, ok := l.resolve(v, sc, 0)
	if !ok {
		l.refuseAs(v, FindingRoute, "a Host header set from a value the tenant check cannot read may name the gateway — "+
			GatewayEndpointRefusal+", and refuses it; write it as a literal")
		return
	}
	for _, s := range values {
		h := strings.TrimSpace(s)
		if hu, err := url.Parse("//" + h); err == nil {
			h = hu.Hostname()
		}
		if hostIsGateway(h, g.Hosts) {
			l.refuseAs(v, FindingRoute, fmt.Sprintf("a Host header %q names the gateway, so the request would reach a tenant route "+
				"the endpoint does not name — %s, and refuses it; send to one of this organisation's tenant routes "+
				"on the gateway instead", s, GatewayEndpointRefusal))
		}
	}
}

// blackboxGatewayProblem says what strict tenant mode refuses in a blackbox
// config (YAML, which blackboxProblem has parsed) when the gateway is
// configured: a key or string naming a gateway host — a module's proxy_url
// or OAuth2 token_url, a header a probe sends — or "". Each string is read as
// an endpoint and searched for each host in its ASCII and Unicode forms.
func (g GatewayRoutes) blackboxGatewayProblem(text string) string {
	var doc any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return "does not parse as YAML"
	}
	names := make([]string, 0, 2*len(g.Hosts))
	for _, h := range g.Hosts {
		names = append(names, h)
		if u, err := idna.Lookup.ToUnicode(h); err == nil && u != h {
			names = append(names, u)
		}
	}
	var problem string
	check := func(s string) bool {
		if t, err := parseEndpoint(s); err == nil && t.routed && strings.Contains(s, "/") {
			if p := g.foreignRoute(t.path); p != "" {
				problem = fmt.Sprintf("has %q: %s, which a probe would send requests to", s, p)
				return true
			}
		}
		if !g.configured() {
			return false
		}
		low := strings.ToLower(s)
		for _, n := range names {
			if strings.Contains(low, n) {
				problem = fmt.Sprintf("names the gateway host %s, which a probe would send requests to", n)
				return true
			}
		}
		if t, err := parseEndpoint(s); err == nil {
			for _, h := range t.hosts {
				if hostIsGateway(h, g.Hosts) {
					problem = fmt.Sprintf("names the gateway host (%q), which a probe would send requests to", s)
					return true
				}
			}
		}
		return false
	}
	var walk func(v any, depth int) bool
	walk = func(v any, depth int) bool {
		if depth > maxNesting {
			problem = "nests too deeply to verify"
			return true
		}
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				if check(k) || walk(val, depth+1) {
					return true
				}
			}
		case map[any]any:
			for k, val := range x {
				if check(fmt.Sprint(k)) || walk(val, depth+1) {
					return true
				}
			}
		case []any:
			for _, el := range x {
				if walk(el, depth+1) {
					return true
				}
			}
		case string:
			return check(x)
		}
		return false
	}
	walk(doc, 0)
	return problem
}
