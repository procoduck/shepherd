package simulate

import (
	"encoding/json"
	"fmt"
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
