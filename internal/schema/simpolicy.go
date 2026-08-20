package schema

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The overlay's S3 simulation policy (VB-1 §6.4) is hand-maintained, and
// `make schema-verify` does not cover it: regenerating the artifact from a new
// Alloy release will not notice that a renamed endpoint attribute left a
// sim_destination pointing at nothing, or that a new Destinations component
// arrived with no policy at all. These guards are the only backstop, so they
// are deliberately strict — a violation fails the schema suite rather than
// surfacing later as a simulation that silently ships a real endpoint.
//
// The fixture-name list below duplicates internal/simulate's stub fixture
// library on purpose: importing that package here would cost internal/schema
// its leaf status and create the cycle simulate→schema→simulate. A test in
// internal/simulate asserts the two lists are identical, which is what keeps
// the duplication honest.

// stubFixtureNames is the closed set of fixture names the stub library serves.
var stubFixtureNames = map[string]bool{
	"aws-targets": true, "azure-targets": true, "consul-targets": true,
	"digitalocean-targets": true, "dns-targets": true, "docker-targets": true,
	"ec2-targets": true, "eureka-targets": true, "file-targets": true,
	"gce-targets": true, "hetzner-targets": true, "http-targets": true,
	"ionos-targets": true, "k8s-pod-targets": true, "kuma-targets": true,
	"lightsail-targets": true, "linode-targets": true, "marathon-targets": true,
	"nerve-targets": true, "nomad-targets": true, "openstack-targets": true,
	"ovhcloud-targets": true, "puppet-targets": true, "scaleway-targets": true,
	"serverset-targets": true, "triton-targets": true, "uyuni-targets": true,
	"docker-logs": true, "file-logs": true, "k8s-pod-logs": true,
}

// StubFixtureNames returns the fixture names the overlay may reference, sorted.
func StubFixtureNames() []string {
	names := make([]string, 0, len(stubFixtureNames))
	for name := range stubFixtureNames {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// simStubTypes is the closed set of stub kinds. "static" resolves to a
// discovery.relabel carrying a literal targets list — discovery.static does not
// exist in Alloy v1.18.1 — and "loki_file" to a loki.source.file.
var simStubTypes = map[string]bool{"static": true, "loki_file": true}

// simReceivers is the closed set of capture receivers the simulator harness
// serves. "none" marks a local sink (a debug printer) that needs no rewrite.
var simReceivers = map[string]bool{
	"prometheus": true, "loki": true, "pyroscope": true,
	"otlp_http": true, "otlp_grpc": true, "syslog": true,
	"faro": true, "splunkhec": true, "file": true, "none": true,
}

var simSecretModes = map[string]bool{"literal": true, "drop_ref": true}

// simKeepClasses is the closed set of sim_keep value classes, duplicated from
// internal/simulate's ClassVerbatim/ClassTargetSet for the same leaf-package
// reason the fixture list above is duplicated; a spec in internal/simulate
// asserts the two agree.
const (
	classVerbatim  = "verbatim"
	classTargetSet = "target_set"
)

var simKeepClasses = map[string]bool{classVerbatim: true, classTargetSet: true}

// KeepClasses returns the value classes an overlay sim_keep entry may name,
// sorted. It exists so a spec in internal/simulate can assert the duplication
// above has not drifted from the constants rule K actually switches on.
func KeepClasses() []string {
	names := make([]string, 0, len(simKeepClasses))
	for name := range simKeepClasses {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// unmappableDestinations are the Destinations components deliberately left
// without a sim_destination. They speak protocols the HTTP capture harness
// cannot emulate (S3, Kafka, GCP, gRPC load balancing) or carry a required
// secret whose removal would break the render (datadog's api.api_key). Each one
// fails a run closed with cannot_rewrite_destination, which is the honest
// answer: a stub that accepted them would produce an empty capture the user
// would read as a pipeline bug.
var unmappableDestinations = map[string]bool{
	"otelcol.exporter.awss3":             true,
	"otelcol.exporter.datadog":           true,
	"otelcol.exporter.googlecloud":       true,
	"otelcol.exporter.googlecloudpubsub": true,
	"otelcol.exporter.kafka":             true,
	"otelcol.exporter.loadbalancing":     true,
	// splunkhec's splunk.token is required AND secret: rule K can never keep
	// it (secret-typed attributes are dropped unconditionally, whatever the
	// keep list says), so a mapped sim_destination would render a config
	// missing a required attribute every time. Availability fix, VB-1 §6.4.
	"otelcol.exporter.splunkhec": true,
}

// credentialNameRe and addressNameRe are the belt-over-brace guard on the keep
// lists — never the keep lists themselves. A heuristic that decides what to
// DROP is unsound, and measurably so in both directions:
// prometheus.scrape.http_headers is declared `map`, matches neither pattern and
// carries `Authorization: Bearer …`, while prometheus.relabel's
// rule.*.target_label matches addressNameRe and holds a label name. As a guard
// the asymmetry disappears: a false negative costs nothing (the path is dropped
// either way, because nothing put it on a list) and a false positive is
// resolved once, explicitly, in sim_keep_acknowledged.
//
// An acknowledgement is a statement that the path carries no CREDENTIAL and no
// authored ENDPOINT — it is not, and must not be read as, a statement that
// nothing at that path can steer a connection. rule.*.target_label is the
// standing example: it may legally hold "__address__", which retargets a scrape
// at runtime from data. Reachability is bounded by the sandbox's network, not
// here (e2e/sandbox_egress_test.go).
//
// They are re-run over the EXPANDED keep set, keep_subtree expansions included.
// That is what stops an Alloy bump adding a `password` attribute inside
// loki.process's stage tree from being silently absorbed by a subtree keep: it
// fails the build here instead of shipping.
//
// COVERAGE LIMIT, and where the other half lives. This guard only ever sees
// path segments the ARTIFACT declares. A kept attribute whose declared type is
// `map` opens a second segment nobody here can read — the user's own key — so
// `prometheus.remote_write.external_labels` passed this guard while
// `external_labels = {"api_key" = "…"}` carried a credential into the sandbox
// (round-2 HIGH). internal/simulate's rule K re-runs IsCredentialName over
// those user-chosen keys at transform time, which is what makes every segment
// of the effective path guarded rather than only the declared prefix.
var (
	credentialNameRe = regexp.MustCompile(
		`(?i)(password|passwd|passphrase|secret|token|credential|api_?key|apikey|bearer|private_?key|key_?file|sasl|auth|access_?key|signature|cert|(^|_)key$|^tls$|tls_config|sigv4|azuread|google_iam)`)
	addressNameRe = regexp.MustCompile(
		`(?i)(url|uri|endpoint|address|addr|host|server|broker|proxy|dsn|target|port)`)
)

// IsCredentialName reports whether one path segment is credential-shaped. It is
// exported for internal/simulate's rule K, which applies the SAME predicate to
// the user-chosen map keys this package can never see (a declared `map` is one
// segment here and an open key space at runtime). Exported rather than
// duplicated: two regexes for one question drift, and the drift would be
// invisible — the build-time guard would keep passing while the runtime one
// stopped recognising a name.
func IsCredentialName(segment string) bool { return credentialNameRe.MatchString(segment) }

// validateSimPolicy checks the overlay's §6.4 simulation keys against the
// artifact: fixtures resolve, receivers are ones the harness serves, every
// endpoint path names an attribute the artifact declares with a usable type,
// secret modes are in the closed set, every keep path resolves in canonical
// form, no kept path trips the name guard without an acknowledgement, and every
// component in the artifact resolves to exactly one S3 disposition.
func validateSimPolicy(artifactComponents, overlayComponents map[string]any) []string {
	var violations []string
	keys := make([]string, 0, len(artifactComponents))
	for key := range artifactComponents {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		comp, _ := overlayComponents[key].(map[string]any)     //nolint:errcheck // a missing entry has no disposition, which validateDisposition reports
		artComp, _ := artifactComponents[key].(map[string]any) //nolint:errcheck // a non-object component is already malformed artifact
		violations = append(violations, validateDisposition(key, comp)...)
		if comp == nil {
			continue
		}

		if stub, present := comp["discovery_stub"]; present {
			violations = append(violations, validateStub(key, stub)...)
		}
		if mode, present := comp["sim_secret_source"]; present {
			violations = append(violations, validateSecretSource(key, mode)...)
		}
		if keep, present := comp["sim_keep"]; present {
			violations = append(violations, validateKeep(key, keep, comp["sim_keep_acknowledged"], artComp)...)
		}

		category, _ := comp["category"].(string) //nolint:errcheck // absent category simply is not "destinations"
		dest, hasDest := comp["sim_destination"]
		switch {
		case hasDest && category != "destinations":
			violations = append(violations, fmt.Sprintf("sim_destination on non-destination component %q", key))
		case hasDest:
			violations = append(violations, validateDestination(key, dest, artComp)...)
		case category == "destinations" && !unmappableDestinations[key]:
			violations = append(violations, fmt.Sprintf(
				"destination %q has no sim_destination and is not on the deliberately-unmappable list; S3 would have to guess where to point it", key))
		}

		if _, present := comp["sim_keep"]; present {
			violations = append(violations, validateRequiredCoverage(key, comp, artComp)...)
		}
	}
	return violations
}

// validateDisposition is the fail-closed guarantee for an Alloy bump: every
// component the artifact declares must resolve to exactly one S3 disposition,
// and a new one therefore fails `make schema-verify` at build time rather than
// reaching a sandbox run with no rule covering it.
//
// The five dispositions are mutually exclusive with one deliberate exception:
// sim_destination is a rule-D policy for where a node POINTS, and a mapped
// destination still needs a sim_keep saying which of its other settings may
// cross into the sandbox. Nothing else pairs.
func validateDisposition(key string, comp map[string]any) []string {
	present := make([]string, 0, 5)
	for _, name := range []string{"sim_unsupported", "sim_secret_source", "discovery_stub", "sim_destination", "sim_keep"} {
		if _, ok := comp[name]; ok {
			present = append(present, name)
		}
	}
	if len(present) == 0 {
		if unmappableDestinations[key] {
			// Its disposition is the deliberately-unmappable list, which
			// validateSimPolicy's destinations branch already enforces.
			return nil
		}
		return []string{fmt.Sprintf(
			"component %q has no S3 disposition: it needs exactly one of discovery_stub, sim_destination, sim_keep, sim_secret_source or sim_unsupported, "+
				"or S3 would run it with whatever the user authored", key)}
	}
	if len(present) == 2 && present[0] == "sim_destination" && present[1] == "sim_keep" {
		return nil
	}
	if len(present) > 1 {
		return []string{fmt.Sprintf("component %q carries %d S3 dispositions (%s); exactly one applies",
			key, len(present), strings.Join(present, ", "))}
	}
	return nil
}

// validateKeep resolves every sim_keep path against the artifact and re-runs
// the name guard over the expanded set.
func validateKeep(key string, raw, rawAck any, artComp map[string]any) []string {
	spec, ok := raw.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("sim_keep on %q must be an object", key)}
	}
	var violations []string

	acknowledged := map[string]bool{}
	if rawAck != nil {
		entries, isList := rawAck.([]any)
		if !isList {
			violations = append(violations, fmt.Sprintf("sim_keep_acknowledged on %q must be a list", key))
		}
		for _, rawEntry := range entries {
			entry, isObj := rawEntry.(map[string]any)
			if !isObj {
				violations = append(violations, fmt.Sprintf("sim_keep_acknowledged on %q has a non-object entry", key))
				continue
			}
			path, _ := entry["path"].(string)     //nolint:errcheck // reported below as an unmatched acknowledgement
			reason, _ := entry["reason"].(string) //nolint:errcheck // reported below
			if strings.TrimSpace(reason) == "" {
				violations = append(violations, fmt.Sprintf(
					"sim_keep_acknowledged on %q names %q with no reason; an exception nobody had to justify is not an exception", key, path))
				continue
			}
			acknowledged[path] = true
		}
	}

	subtree, _ := spec["keep_subtree"].(bool) //nolint:errcheck // absent means false, which is the narrow reading
	reason, _ := spec["reason"].(string)      //nolint:errcheck // checked below
	rawPaths, _ := spec["paths"].([]any)      //nolint:errcheck // absent means none, which is only valid with keep_subtree

	var expanded []keptPath
	switch {
	case subtree:
		if strings.TrimSpace(reason) == "" {
			violations = append(violations, fmt.Sprintf(
				"sim_keep on %q sets keep_subtree with no reason; a subtree keep is the one construct where a future attribute is not fail-closed", key))
		}
		if len(rawPaths) > 0 {
			violations = append(violations, fmt.Sprintf("sim_keep on %q sets both keep_subtree and paths; they are alternatives", key))
		}
		expanded = expandKeepSubtree(artComp, nil)
	case len(rawPaths) == 0:
		if strings.TrimSpace(reason) == "" {
			violations = append(violations, fmt.Sprintf(
				"sim_keep on %q keeps nothing and gives no reason; dropping every authored setting is a deliberate choice, not a default", key))
		}
	default:
		for _, rawPath := range rawPaths {
			path, class, bad := keepPathSegments(key, rawPath)
			if bad != "" {
				violations = append(violations, bad)
				continue
			}
			canonical, typ, found := canonicalAttributePath(artComp, path)
			switch {
			case !found:
				violations = append(violations, fmt.Sprintf(
					"sim_keep on %q names path %q which the artifact does not declare", key, strings.Join(path, ".")))
				continue
			case !equalSegments(canonical, path):
				violations = append(violations, fmt.Sprintf(
					"sim_keep on %q names path %q but its canonical form is %q; a \"*\" must follow every repeatable block and no other segment",
					key, strings.Join(path, "."), strings.Join(canonical, ".")))
				continue
			case typ == "secret" || typ == "capsule":
				violations = append(violations, fmt.Sprintf(
					"sim_keep on %q names path %q whose declared type is %q; a secret is a credential by definition and a capsule renders as a raw expression",
					key, strings.Join(path, "."), typ))
				continue
			case !simKeepClasses[class]:
				violations = append(violations, fmt.Sprintf(
					"sim_keep on %q names path %q with unknown value class %q; rule K would not know what it is allowed to put there",
					key, strings.Join(path, "."), class))
				continue
			case class == classTargetSet && typ != "list":
				violations = append(violations, fmt.Sprintf(
					"sim_keep on %q gives path %q the %q class but the artifact declares it %q; a target set is a list of label sets",
					key, strings.Join(path, "."), classTargetSet, typ))
				continue
			}
			expanded = append(expanded, keptPath{path: path, class: class})
		}
	}

	violations = append(violations, guardKeptPaths(key, expanded, acknowledged)...)
	return violations
}

// keptPath is one entry of the expanded keep set: the canonical path plus the
// value class governing what may occupy it. The class matters to the guard
// because a class that OVERWRITES the value answers the address question by
// construction, where an acknowledgement only asserts a human looked at it.
type keptPath struct {
	path  []string
	class string
}

// guardKeptPaths is the name guard over the expanded keep set.
func guardKeptPaths(key string, expanded []keptPath, acknowledged map[string]bool) []string {
	var violations []string
	for _, kept := range expanded {
		dotted := strings.Join(kept.path, ".")
		if acknowledged[dotted] {
			continue
		}
		for _, segment := range kept.path {
			if credentialNameRe.MatchString(segment) {
				violations = append(violations, fmt.Sprintf(
					"sim_keep on %q keeps %q, whose segment %q is credential-shaped; drop it or acknowledge it in sim_keep_acknowledged with a reason",
					key, dotted, segment))
				break
			}
		}
		// The target_set class is the one answer to the address guard that is
		// not an assertion about a human's judgement: rule K overwrites
		// __address__ and __scheme__ from HarnessEndpoints unconditionally, so
		// no authored address can occupy the path whatever it is named. An
		// acknowledgement here would be strictly weaker and would have to be
		// repeated on all fourteen components that carry a target set.
		if kept.class == classTargetSet {
			continue
		}
		if addressNameRe.MatchString(kept.path[len(kept.path)-1]) {
			violations = append(violations, fmt.Sprintf(
				"sim_keep on %q keeps %q, whose leaf is address-shaped; drop it, give it a value class that overwrites the address, or acknowledge it in sim_keep_acknowledged with a reason",
				key, dotted))
		}
	}
	return violations
}

// validateRequiredCoverage is the BUILD-TIME half of the availability fix
// (round-2 finding: 39 artifact components rendered a transformed body
// missing a required attribute, because rule K drops anything sim_keep does
// not name and nobody had checked sim_keep against what the artifact actually
// requires). It closes the gap the same way the discovery-stub guard closes
// its own: fail `make schema-verify` instead of failing a user's sandbox run
// with a diagnostic pinned to their node for a value the TRANSFORM removed.
//
// A required path is checked only when it is UNCONDITIONAL — reachable by
// descending through required blocks alone, never an optional one. An
// attribute required only inside a block the user might not even author is a
// different, softer problem (the block simply must be complete if authored at
// all); catching that would mean re-deriving the artifact's own validation at
// build time, which this guard does not attempt.
//
// A required path that is ALSO a wireable input port is not checked: render.go
// resolves an edge onto its port entirely outside of props (resolveEdges),
// so its absence from sim_keep is not evidence the render is broken.
func validateRequiredCoverage(key string, comp, artComp map[string]any) []string {
	category, _ := comp["category"].(string) //nolint:errcheck // absent category cannot be "sources"
	if category == "sources" && (strings.HasPrefix(key, "discovery.") || strings.HasPrefix(key, "loki.source.")) {
		if _, stubbed := comp["discovery_stub"]; !stubbed {
			// applyStubs refuses this at runtime with cannot_stub before rule K
			// ever inspects sim_keep, whatever it says.
			return nil
		}
	}

	keepSpec, _ := comp["sim_keep"].(map[string]any) //nolint:errcheck // a malformed sim_keep is already reported by validateKeep
	if keepSpec == nil {
		return nil
	}
	if subtree, _ := keepSpec["keep_subtree"].(bool); subtree { //nolint:errcheck // absent means false
		return nil // every attribute the artifact declares survives; nothing can be missing
	}

	covered := map[string]bool{}
	if rawPaths, _ := keepSpec["paths"].([]any); rawPaths != nil { //nolint:errcheck // absent means none
		for _, rawPath := range rawPaths {
			path, _, bad := keepPathSegments(key, rawPath)
			if bad != "" {
				continue // malformed shape is already reported by validateKeep
			}
			if canonical, _, found := canonicalAttributePath(artComp, path); found {
				covered[strings.Join(canonical, ".")] = true
			}
		}
	}
	if destSpec, hasDest := comp["sim_destination"].(map[string]any); hasDest { // a malformed sim_destination is already reported by validateDestination
		if rawPaths, _ := destSpec["endpoint_paths"].([]any); rawPaths != nil { //nolint:errcheck // absent means none
			for _, rawPath := range rawPaths {
				if segments, ok := rawPath.([]any); ok {
					var path []string
					for _, seg := range segments {
						if s, ok := seg.(string); ok {
							path = append(path, s)
						}
					}
					covered[strings.Join(path, ".")] = true
				}
			}
		}
		if ensure, _ := destSpec["ensure"].(map[string]any); ensure != nil { //nolint:errcheck // absent means none
			for k := range ensure {
				covered[k] = true
			}
		}
		// The syslog exporter splits host and port across two attributes; D
		// forces "port" outside endpoint_paths (transform.go's rewriteDestinations),
		// so this guard has to know that too or every syslog destination would
		// report a false gap.
		if receiver, _ := destSpec["receiver"].(string); receiver == "syslog" { //nolint:errcheck // absent means not syslog
			covered["port"] = true
		}
	}

	var violations []string
	for _, req := range unconditionalRequiredPaths(artComp, nil) {
		dotted := strings.Join(req.path, ".")
		if covered[dotted] || isPortPath(artComp, req.path) {
			continue
		}
		violations = append(violations, fmt.Sprintf(
			"component %q has a required %s %q that neither sim_keep nor sim_destination covers; rule K would drop it and the sandbox's real Alloy binary would reject the render as missing a required %s — add it to sim_keep or mark the component sim_unsupported with an honest reason",
			key, req.kind, dotted, req.kind))
	}
	return violations
}

// requiredLeaf is one attribute or block the artifact says must be present in
// every valid render — reachable from the component root through required
// blocks only, per validateRequiredCoverage's doc comment.
type requiredLeaf struct {
	path []string
	kind string // "attribute" or "block"
}

// unconditionalRequiredPaths walks the artifact's own attribute/block tree —
// not the overlay's — collecting every required attribute reachable without
// ever stepping into an optional block. It intentionally does not report a
// required block with no required leaves of its own (e.g. an otelcol
// component's `output` block, required so the syntax exists but populated
// entirely by wired ports): nothing about such a block depends on sim_keep,
// so there is nothing here for the guard to say.
func unconditionalRequiredPaths(node map[string]any, prefix []string) []requiredLeaf {
	var out []requiredLeaf
	attrs, _ := node["attributes"].([]any) //nolint:errcheck // absent attributes means none at this level
	for _, rawAttr := range attrs {
		attr, ok := rawAttr.(map[string]any)
		if !ok {
			continue
		}
		if required, _ := attr["required"].(bool); required { //nolint:errcheck // absent means not required
			name, _ := attr["name"].(string) //nolint:errcheck // a nameless attribute cannot be addressed
			out = append(out, requiredLeaf{path: appendSegments(prefix, name), kind: "attribute"})
		}
	}
	blocks, _ := node["blocks"].([]any) //nolint:errcheck // absent blocks means none at this level
	for _, rawBlock := range blocks {
		block, ok := rawBlock.(map[string]any)
		if !ok {
			continue
		}
		if required, _ := block["required"].(bool); !required { //nolint:errcheck // absent means not required
			continue
		}
		name, _ := block["name"].(string)           //nolint:errcheck // a nameless block cannot be addressed
		repeatable, _ := block["repeatable"].(bool) //nolint:errcheck // absent means not repeatable
		child := appendSegments(prefix, name)
		if repeatable {
			child = appendSegments(child, "*")
		}
		out = append(out, unconditionalRequiredPaths(block, child)...)
	}
	return out
}

// isPortPath reports whether the artifact declares path as a wireable input
// port, mirroring internal/visual's portPath fallback (a port with no
// explicit path is addressed by its own prop/export name).
func isPortPath(artComp map[string]any, path []string) bool {
	inputs, _ := artComp["inputs"].([]any) //nolint:errcheck // absent inputs means nothing is wireable
	dotted := strings.Join(path, ".")
	for _, rawPort := range inputs {
		port, ok := rawPort.(map[string]any)
		if !ok {
			continue
		}
		var segs []string
		if rawPath, _ := port["path"].([]any); len(rawPath) > 0 { //nolint:errcheck // absent path falls back to prop below
			for _, s := range rawPath {
				if str, ok := s.(string); ok {
					segs = append(segs, str)
				}
			}
		} else if prop, _ := port["prop"].(string); prop != "" { //nolint:errcheck // an unnamed prop cannot address a path
			segs = []string{prop}
		}
		if len(segs) > 0 && strings.Join(segs, ".") == dotted {
			return true
		}
	}
	return false
}

// expandKeepSubtree materializes the concrete path set a keep_subtree covers,
// in canonical form. Without this the name guard would have nothing to run
// over and a subtree keep would be an unbounded promise. Every expanded path is
// verbatim: a subtree keep names no classes, which is why it is only granted to
// components measured to carry nothing a class would have to neutralise.
func expandKeepSubtree(node map[string]any, prefix []string) []keptPath {
	var out []keptPath
	attrs, _ := node["attributes"].([]any) //nolint:errcheck // absent attributes means none at this level
	for _, rawAttr := range attrs {
		attr, ok := rawAttr.(map[string]any)
		if !ok {
			continue
		}
		name, _ := attr["name"].(string) //nolint:errcheck // a nameless attribute cannot be addressed
		out = append(out, keptPath{path: appendSegments(prefix, name), class: classVerbatim})
	}
	blocks, _ := node["blocks"].([]any) //nolint:errcheck // absent blocks means none at this level
	for _, rawBlock := range blocks {
		block, ok := rawBlock.(map[string]any)
		if !ok {
			continue
		}
		name, _ := block["name"].(string)           //nolint:errcheck // a nameless block cannot be addressed
		repeatable, _ := block["repeatable"].(bool) //nolint:errcheck // absent means not repeatable
		child := appendSegments(prefix, name)
		if repeatable {
			child = appendSegments(child, "*")
		}
		out = append(out, expandKeepSubtree(block, child)...)
	}
	return out
}

// keepPathSegments accepts the dotted shorthand, the explicit segment array and
// the {"path": …, "class": …} object, mirroring simulate.KeepPath's decoding so
// the two cannot drift. A path written in any of the first two forms is
// verbatim, which is how the overwhelming majority of entries are written.
func keepPathSegments(key string, raw any) (path []string, class, violation string) {
	switch v := raw.(type) {
	case string:
		if v == "" {
			return nil, "", fmt.Sprintf("sim_keep on %q has an empty path", key)
		}
		return strings.Split(v, "."), classVerbatim, ""
	case []any:
		segments := make([]string, 0, len(v))
		for _, seg := range v {
			s, ok := seg.(string)
			if !ok {
				return nil, "", fmt.Sprintf("sim_keep on %q has a non-string path segment", key)
			}
			segments = append(segments, s)
		}
		if len(segments) == 0 {
			return nil, "", fmt.Sprintf("sim_keep on %q has an empty path", key)
		}
		return segments, classVerbatim, ""
	case map[string]any:
		inner, ok := v["path"]
		if !ok {
			return nil, "", fmt.Sprintf("sim_keep on %q has an object entry with no path", key)
		}
		segments, _, bad := keepPathSegments(key, inner)
		if bad != "" {
			return nil, "", bad
		}
		named, _ := v["class"].(string) //nolint:errcheck // an absent or non-string class falls back to verbatim and is then reported by the closed-set check
		if named == "" {
			named = classVerbatim
		}
		return segments, named, ""
	}
	return nil, "", fmt.Sprintf("sim_keep on %q has a malformed path", key)
}

// canonicalAttributePath resolves a keep path against the artifact's attribute
// and block tree, ignoring "*" segments on the way in, and returns the path in
// canonical form so the caller can insist the overlay wrote it that way. Rule K
// matches on the canonical form, so a path written any other way would silently
// keep nothing.
func canonicalAttributePath(node map[string]any, path []string) (canonical []string, typ string, found bool) {
	if node == nil || len(path) == 0 {
		return nil, "", false
	}
	head := path[0]
	if len(path) == 1 {
		attrs, _ := node["attributes"].([]any) //nolint:errcheck // absent attributes means the name is not declared
		for _, rawAttr := range attrs {
			attr, ok := rawAttr.(map[string]any)
			if !ok {
				continue
			}
			if name, _ := attr["name"].(string); name == head { //nolint:errcheck // a nameless attribute cannot match
				t, _ := attr["type"].(string) //nolint:errcheck // an untyped attribute reports the empty type
				return []string{head}, t, true
			}
		}
		return nil, "", false
	}
	rest := path[1:]
	if rest[0] == "*" {
		rest = rest[1:]
	}
	blocks, _ := node["blocks"].([]any) //nolint:errcheck // absent blocks means the path cannot descend
	for _, rawBlock := range blocks {
		block, ok := rawBlock.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := block["name"].(string); name != head { //nolint:errcheck // a nameless block cannot match
			continue
		}
		childCanonical, t, ok := canonicalAttributePath(block, rest)
		if !ok {
			return nil, "", false
		}
		prefix := []string{head}
		if repeatable, _ := block["repeatable"].(bool); repeatable { //nolint:errcheck // absent means not repeatable
			prefix = append(prefix, "*")
		}
		return append(prefix, childCanonical...), t, true
	}
	return nil, "", false
}

func appendSegments(prefix []string, more ...string) []string {
	out := make([]string, 0, len(prefix)+len(more))
	out = append(out, prefix...)
	out = append(out, more...)
	return out
}

func equalSegments(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func validateStub(key string, raw any) []string {
	stub, ok := raw.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("discovery_stub on %q must be an object", key)}
	}
	var violations []string
	typ, _ := stub["type"].(string) //nolint:errcheck // an absent or non-string type is reported below
	if !simStubTypes[typ] {
		violations = append(violations, fmt.Sprintf("discovery_stub on %q has unknown type %q", key, typ))
	}
	fixture, _ := stub["fixture"].(string) //nolint:errcheck // an absent or non-string fixture is reported below
	if !stubFixtureNames[fixture] {
		violations = append(violations, fmt.Sprintf("discovery_stub on %q names fixture %q which the stub library does not serve", key, fixture))
	}
	return violations
}

func validateSecretSource(key string, raw any) []string {
	spec, ok := raw.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("sim_secret_source on %q must be an object", key)}
	}
	mode, _ := spec["mode"].(string) //nolint:errcheck // an absent or non-string mode is reported below
	if !simSecretModes[mode] {
		return []string{fmt.Sprintf("sim_secret_source on %q has unknown mode %q", key, mode)}
	}
	return nil
}

func validateDestination(key string, raw any, artComp map[string]any) []string {
	spec, ok := raw.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("sim_destination on %q must be an object", key)}
	}
	var violations []string

	receiver, _ := spec["receiver"].(string) //nolint:errcheck // an absent or non-string receiver is reported below
	if !simReceivers[receiver] {
		violations = append(violations, fmt.Sprintf("sim_destination on %q names receiver %q which the capture harness does not serve", key, receiver))
	}

	paths, _ := spec["endpoint_paths"].([]any) //nolint:errcheck // absent means no paths, which is only valid for receiver "none"
	if receiver != "none" && len(paths) == 0 {
		violations = append(violations, fmt.Sprintf("sim_destination on %q names receiver %q but no endpoint_paths, so nothing would be re-pointed", key, receiver))
	}
	for _, rawPath := range paths {
		segments, ok := rawPath.([]any)
		if !ok || len(segments) == 0 {
			violations = append(violations, fmt.Sprintf("sim_destination on %q has a malformed endpoint path", key))
			continue
		}
		path := make([]string, 0, len(segments))
		for _, seg := range segments {
			s, ok := seg.(string)
			if !ok {
				violations = append(violations, fmt.Sprintf("sim_destination on %q has a non-string path segment", key))
				path = nil
				break
			}
			path = append(path, s)
		}
		if path == nil || artComp == nil {
			continue
		}
		typ, found := attributeTypeAt(artComp, path)
		switch {
		case !found:
			violations = append(violations, fmt.Sprintf(
				"sim_destination on %q names endpoint path %q which the artifact does not declare", key, strings.Join(path, ".")))
		case typ != "string" && typ != "number":
			violations = append(violations, fmt.Sprintf(
				"sim_destination on %q names endpoint path %q whose declared type is %q; only string and number can hold an address",
				key, strings.Join(path, "."), typ))
		}
	}
	return violations
}

// attributeTypeAt resolves a sim_destination path against the artifact's own
// attribute and block tree, treating "*" as "any instance of this repeatable
// block". The types come from the artifact rather than from the overlay so a
// path that stops matching after a schema regeneration is caught.
func attributeTypeAt(node map[string]any, path []string) (string, bool) {
	if len(path) == 0 {
		return "", false
	}
	head := path[0]
	if len(path) == 1 {
		attrs, _ := node["attributes"].([]any) //nolint:errcheck // absent attributes means the name is not declared
		for _, rawAttr := range attrs {
			attr, ok := rawAttr.(map[string]any)
			if !ok {
				continue
			}
			if name, _ := attr["name"].(string); name == head { //nolint:errcheck // a nameless attribute cannot match
				typ, _ := attr["type"].(string) //nolint:errcheck // an untyped attribute reports the empty type, which the caller rejects
				return typ, true
			}
		}
		return "", false
	}
	rest := path[1:]
	if rest[0] == "*" {
		rest = rest[1:]
	}
	blocks, _ := node["blocks"].([]any) //nolint:errcheck // absent blocks means the path cannot descend
	for _, rawBlock := range blocks {
		block, ok := rawBlock.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := block["name"].(string); name == head { //nolint:errcheck // a nameless block cannot match
			return attributeTypeAt(block, rest)
		}
	}
	return "", false
}
