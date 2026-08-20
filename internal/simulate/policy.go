package simulate

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Policy is the slice of the merged schema payload the S3 transform reads: the
// per-component simulation rules the overlay carries. It is decoded separately
// from visual.SchemaPayload so simulation policy never leaks into the type the
// renderer consumes — the renderer must stay unaware that S3 exists.
type Policy struct {
	Components map[string]ComponentPolicy `json:"components"`
}

// ComponentPolicy is the overlay's simulation policy for one component. Every
// field is a pointer because absence is meaningful: a Destinations component
// with no Destination policy fails the run closed (§6.4), it is not "left as
// authored".
type ComponentPolicy struct {
	Category     string            `json:"category"`
	Stub         *StubSpec         `json:"discovery_stub"`
	Destination  *DestSpec         `json:"sim_destination"`
	SecretSource *SecretSourceSpec `json:"sim_secret_source"`
	Keep         *KeepSpec         `json:"sim_keep"`
	Unsupported  string            `json:"sim_unsupported"`
}

// KeepSpec is the deny-by-default allowlist rule K constructs a node's props
// from. It is the security boundary: an attribute path absent from it is absent
// from the sandbox config, whatever the artifact declares its type to be.
//
// The unit is the block-qualified attribute PATH, not the attribute's declared
// type and not its name. Type is what the previous design used and what failed:
// of the 6482 attribute paths the shipped artifact declares, only 338 are typed
// `secret`, so a credential declared `string` — otelcol.receiver.solace's
// auth.sasl_xauth2.bearer, otelcol.receiver.cloudflare's secret — reached the
// sandbox verbatim. Name is unsound in both directions and measurably so:
// prometheus.scrape.http_headers is declared `map`, matches no credential
// pattern and carries `Authorization: Bearer …`, while prometheus.relabel's
// rule.*.target_label matches an address pattern and is a label name.
//
// A PATH is not the whole story for a `map`-typed attribute, and pretending it
// was is the round-2 HIGH this design had to answer. internal/schema guards
// every segment the artifact declares, but `external_labels` is one segment
// there and `external_labels.<user key>` at runtime, so thirteen kept paths in
// the shipped overlay opened a key space nobody reviewed. Rule K's
// constrainKeys re-runs schema.IsCredentialName over those user-chosen keys, so
// the guarded prefix and the guarded suffix meet. Two paths were dropped from
// prometheus.scrape rather than guarded — `params`, the query-string credential
// mechanism http_headers was already excluded for, and `metrics_path`, which
// takes a query string of its own and is the attribute form of the
// __metrics_path__ meta label target_set removes.
//
// Subtree keeps a whole component body. It is restricted to components measured
// to carry no credential-, address-, header- or filesystem-shaped attribute at
// any depth, and it is the one construct where a FUTURE artifact attribute is
// not fail-closed — which is why internal/schema re-runs the name guard over
// the EXPANDED path set at build time.
type KeepSpec struct {
	Subtree bool       `json:"keep_subtree"`
	Paths   []KeepPath `json:"paths"`
	Reason  string     `json:"reason"`
}

// KeepPath is one allowlisted attribute path in canonical form: a "*" segment
// follows every repeatable block, and no segment follows a block that is not
// repeatable. internal/schema resolves each one against the artifact and fails
// the build when it does not match that form exactly.
//
// Class is the VALUE class governing what may occupy the path once the path
// itself is allowed. A path with no class is verbatim: rule K copies the
// authored literal. The distinction exists because "this path may survive" and
// "this value may survive" are different questions, and prometheus.scrape's
// `targets` is the case that proves it — the path carries the whole point of a
// scrape pipeline and must survive, while the __address__ inside it is the
// CRITICAL-2 exfiltration vector and must not.
type KeepPath struct {
	Path  []string
	Class string
}

// Value classes. Only the two rule K actually distinguishes are defined:
// speculative classes would be untested code sitting inside the safety
// boundary.
const (
	// ClassVerbatim copies the authored literal, having checked it is a
	// literal of the declared type. It is the default for a path with no
	// class, which is how the overwhelming majority of keep entries are
	// written.
	ClassVerbatim = "verbatim"
	// ClassTargetSet rebuilds a Prometheus-style target list: __address__ and
	// __scheme__ are FORCED to the harness's synthetic exporter, the other
	// connection-steering meta labels are removed, the user's own labels
	// survive unless their NAME is credential-shaped, and every label value is
	// accounted for by P1' rather than allowed by virtue of the path. It is
	// what makes a literal `targets` both safe and useful — dropping the
	// attribute outright closes CRITICAL-2 as well, but leaves a user who typed
	// their targets by hand with a config missing a required attribute.
	//
	// It is only granted to components whose `targets` really is a list of
	// PROMETHEUS label sets. prometheus.exporter.blackbox and
	// prometheus.exporter.snmp look identical in the artifact (both declare
	// `targets` as a `list`) and are not: their probe destination rides in an
	// ordinary `address` key, which forcing __address__ does not touch. Both
	// are sim_unsupported for that reason rather than carrying this class.
	ClassTargetSet = "target_set"
)

// UnmarshalJSON accepts the dotted shorthand ("endpoint.*.queue_config.capacity"),
// the explicit segment array (["stage.json", "*", "expressions"]) and the object
// form ({"path": "targets", "class": "target_set"}). The array form exists
// because Alloy block names may themselves contain dots — loki.process declares
// 24 of them — so the shorthand is not universally expressible. A shorthand that
// splits wrong resolves to nothing and fails the schema guard rather than
// silently keeping the wrong path.
func (k *KeepPath) UnmarshalJSON(data []byte) error {
	var dotted string
	if err := json.Unmarshal(data, &dotted); err == nil {
		k.Path, k.Class = strings.Split(dotted, "."), ClassVerbatim
		return nil
	}
	var segments []string
	if err := json.Unmarshal(data, &segments); err == nil {
		k.Path, k.Class = segments, ClassVerbatim
		return nil
	}
	var obj struct {
		Path  json.RawMessage `json:"path"`
		Class string          `json:"class"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("simulate: sim_keep path must be a string, a list of segments, or an object: %w", err)
	}
	var inner KeepPath
	if err := inner.UnmarshalJSON(obj.Path); err != nil {
		return err
	}
	k.Path = inner.Path
	k.Class = obj.Class
	if k.Class == "" {
		k.Class = ClassVerbatim
	}
	return nil
}

// Keeps reports whether a canonical attribute path is on the allowlist.
func (s *KeepSpec) Keeps(path []string) bool {
	_, ok := s.ClassOf(path)
	return ok
}

// ClassOf returns the value class governing a canonical attribute path, and
// whether the path is on the allowlist at all. A subtree keep is verbatim
// throughout: the components it is granted to were measured to carry no
// address- or credential-shaped attribute at any depth, so there is nothing
// there for a class to neutralise.
func (s *KeepSpec) ClassOf(path []string) (string, bool) {
	if s == nil {
		return "", false
	}
	if s.Subtree {
		return ClassVerbatim, true
	}
	for i := range s.Paths {
		if equalPath(s.Paths[i].Path, path) {
			class := s.Paths[i].Class
			if class == "" {
				class = ClassVerbatim
			}
			return class, true
		}
	}
	return "", false
}

func equalPath(a, b []string) bool {
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

// StubSpec names the synthetic source a discovery or log-source node is
// replaced by. Type is "static" (a discovery.relabel carrying a literal targets
// list — Alloy v1.18.1 has no discovery.static) or "loki_file" (a
// loki.source.file tailing a fixture the harness writes).
type StubSpec struct {
	Type    string `json:"type"`
	Fixture string `json:"fixture"`
}

// DestSpec describes how one Destinations component is pointed at the capture
// harness. EndpointPaths address attributes inside the component body, with "*"
// standing for every instance of a repeatable block. Ensure forces literal
// attribute values at dotted paths and exists only for the TLS downgrades the
// unauthenticated in-pod capture endpoints require.
type DestSpec struct {
	Receiver      string         `json:"receiver"`
	EndpointPaths [][]string     `json:"endpoint_paths"`
	Ensure        map[string]any `json:"ensure"`
}

// SecretSourceSpec marks a component whose whole purpose is to supply secrets.
// Mode "literal" means references to it can be replaced by the string
// "simulated"; "drop_ref" means the export is a capsule with no literal
// equivalent, so every reference is deleted instead.
type SecretSourceSpec struct {
	Mode string `json:"mode"`
}

// Secret-source modes.
const (
	SecretModeLiteral = "literal"
	SecretModeDropRef = "drop_ref"
)

// Stub types.
const (
	StubTypeStatic   = "static"
	StubTypeLokiFile = "loki_file"
)

// Receivers the capture harness can emulate. A DestSpec naming anything else is
// an overlay bug the schema registry's guard rejects before it can reach a run.
const (
	ReceiverPrometheus = "prometheus"
	ReceiverLoki       = "loki"
	ReceiverPyroscope  = "pyroscope"
	ReceiverOTLPHTTP   = "otlp_http"
	ReceiverOTLPGRPC   = "otlp_grpc"
	ReceiverSyslog     = "syslog"
	ReceiverFaro       = "faro"
	ReceiverSplunkHEC  = "splunkhec"
	ReceiverFile       = "file"
	ReceiverNone       = "none"
)

// LoadPolicy decodes the merged schema map (artifact deep-merged with the
// overlay, as schema.Registry.Get returns it) into a Policy. The JSON
// round-trip is the same bridge mgmtapi already uses to turn that map into a
// visual.SchemaPayload, so the two decodings cannot drift on shape.
func LoadPolicy(merged map[string]any) (Policy, error) {
	b, err := json.Marshal(merged)
	if err != nil {
		return Policy{}, fmt.Errorf("simulate: marshal schema: %w", err)
	}
	var p Policy
	if err := json.Unmarshal(b, &p); err != nil {
		return Policy{}, fmt.Errorf("simulate: decode simulation policy: %w", err)
	}
	return p, nil
}
