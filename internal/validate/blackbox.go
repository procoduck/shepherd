package validate

import (
	"fmt"
	"strings"

	"github.com/grafana/alloy/syntax/ast"
	"gopkg.in/yaml.v3"
)

// blackboxExporter is the component whose probes the lint reads.
const blackboxExporter = "prometheus.exporter.blackbox"

// A blackbox probe sends requests the pipeline defines: an http module may
// send any header (headers, http_client_config.http_headers) with any body,
// and a tcp module's query_response may send any text, expanding regular
// expression references from what the server answered. So in strict tenant
// mode prometheus.exporter.blackbox's modules must be a literal config (the
// lint reads it as YAML, as blackbox_exporter does) that names the
// X-Scope-OrgID header nowhere — no key and no string, compared
// case-insensitively after YAML decoding — and has no query_response;
// config_file, read on the collector, is refused. Shepherd's blackbox
// wizard writes exactly such a config.

// blackboxConfig checks a prometheus.exporter.blackbox block b (named name).
func (l *tenantLinter) blackboxConfig(b *ast.BlockStmt, name string, sc scope) {
	if name != blackboxExporter {
		return
	}
	attrs := attrsOf(b)
	if v := attrs["config_file"]; v != nil {
		l.refuse(v, blackboxExporter+" config_file is read on the collector, so the tenant check cannot read the "+
			"probes it defines — strict tenant mode refuses it; write the modules in config")
	}
	v := attrs["config"]
	if v == nil {
		return
	}
	texts, ok := l.resolve(v, sc, 0)
	if !ok {
		l.refuse(v, blackboxExporter+" config cannot be verified — strict tenant mode refuses it; write the modules "+
			"as a literal string")
		return
	}
	for _, t := range texts {
		if !l.budget.charge(1 + len(t)/64) {
			return
		}
		if problem := blackboxProblem(t); problem != "" {
			l.refuse(v, fmt.Sprintf("%s config %s — strict tenant mode refuses it", blackboxExporter, problem))
			return
		}
		if problem := l.policy.Gateway.blackboxGatewayProblem(t); problem != "" {
			l.refuse(v, fmt.Sprintf("%s config %s — %s, and refuses it", blackboxExporter, problem, GatewayEndpointRefusal))
			return
		}
	}
}

// blackboxProblem says what strict tenant mode refuses in a blackbox
// config, or "".
func blackboxProblem(text string) string {
	var doc any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return fmt.Sprintf("does not parse as YAML (%v), so the probes it defines cannot be verified", err)
	}
	return blackboxWalk(doc, 0)
}

func blackboxWalk(v any, depth int) string {
	if depth > maxNesting {
		return "nests too deeply to verify"
	}
	names := func(s string) string {
		if strings.Contains(strings.ToLower(s), strings.ToLower(tenantHeader)) {
			return "names the " + tenantHeader + " header, which a probe would send"
		}
		return ""
	}
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if p := names(k); p != "" {
				return p
			}
			if k == "query_response" {
				return "has a tcp query_response, which sends text the tenant check cannot verify"
			}
			if p := blackboxWalk(val, depth+1); p != "" {
				return p
			}
		}
	case map[any]any:
		for k, val := range x {
			ks := fmt.Sprint(k)
			if p := names(ks); p != "" {
				return p
			}
			if ks == "query_response" {
				return "has a tcp query_response, which sends text the tenant check cannot verify"
			}
			if p := blackboxWalk(val, depth+1); p != "" {
				return p
			}
		}
	case []any:
		for _, el := range x {
			if p := blackboxWalk(el, depth+1); p != "" {
				return p
			}
		}
	case string:
		return names(x)
	case []byte:
		return names(string(x))
	}
	return ""
}
