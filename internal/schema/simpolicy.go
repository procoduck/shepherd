package schema

import (
	"fmt"
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
}

// validateSimPolicy checks the overlay's §6.4 simulation keys against the
// artifact: fixtures resolve, receivers are ones the harness serves, every
// endpoint path names an attribute the artifact declares with a usable type,
// secret modes are in the closed set, and no Destinations component is left
// both unmapped and unlisted.
func validateSimPolicy(artifactComponents, overlayComponents map[string]any) []string {
	var violations []string
	keys := make([]string, 0, len(overlayComponents))
	for key := range overlayComponents {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		comp, ok := overlayComponents[key].(map[string]any)
		if !ok {
			continue
		}
		artComp, _ := artifactComponents[key].(map[string]any) //nolint:errcheck // a missing key is already reported as an orphaned overlay key

		if stub, present := comp["discovery_stub"]; present {
			violations = append(violations, validateStub(key, stub)...)
		}
		if mode, present := comp["sim_secret_source"]; present {
			violations = append(violations, validateSecretSource(key, mode)...)
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
	}
	return violations
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
