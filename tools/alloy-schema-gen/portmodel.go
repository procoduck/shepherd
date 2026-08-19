// portmodel.go — the port/wire model shared by the extractor and this repo.
//
// This file is deliberately free of any grafana/alloy import so that it
// compiles (and is unit-tested) inside THIS module, while run.sh also copies it
// verbatim into the injected cmd/shepherd-schema-dump package inside the alloy
// checkout. extract.go carries "//go:build ignore" and therefore never sees
// these symbols here; inside the checkout both files are package main together.
//
// Keep it dependency-free: run.sh copies it byte-for-byte.
package main

import "strings"

// WireKind classifies a wire type by which side of an Alloy reference owns it.
//
//   - WireKindData: the value itself travels (discovery.Target). In Alloy the
//     CONSUMER references the producer:
//     prometheus.scrape "x" { targets = discovery.kubernetes.k8s.targets }
//   - WireKindReceiver: a sink handle travels (storage.Appendable,
//     loki.LogsReceiver, otelcol.Consumer, pyroscope.Appendable). In Alloy the
//     PRODUCER references the consumer:
//     prometheus.scrape "x" { forward_to = [prometheus.remote_write.s.receiver] }
//
// In both cases data flows producer -> consumer. The canvas always draws that
// direction, so the renderer — not the canvas — inverts where Alloy requires it
// (visual-builder decision D1).
type WireKind string

const (
	// WireKindData is a wire type whose value is the data (discovery targets).
	WireKindData WireKind = "data"
	// WireKindReceiver is a wire type whose value is a sink handle.
	WireKindReceiver WireKind = "receiver"
)

// Port roles. A role says where the port is drawn and which way data moves; it
// is independent of whether the port came from the Arguments or Exports struct.
const (
	// RoleProduces — data LEAVES the node here. Drawn on the node's right.
	RoleProduces = "produces"
	// RoleAccepts — data ENTERS the node here. Drawn on the node's left.
	RoleAccepts = "accepts"
)

// WireType is a canonical wire-type id plus its kind.
type WireType struct {
	Wire string
	Kind WireKind
}

// GoTypeWireMap maps the Go type of an Arguments/Exports field to a canonical
// wire type (VB-1 §3.1). Keys are matched against reflect.Type.String() with
// slices and pointers stripped. Port NAMES cannot come from the alloy metadata
// package — it reports only the types a component accepts/exports — so names are
// read from the alloy struct tags and this map decides which fields are ports.
var GoTypeWireMap = map[string]WireType{
	"discovery.Target":     {Wire: "targets", Kind: WireKindData},
	"storage.Appendable":   {Wire: "prom.metrics", Kind: WireKindReceiver},
	"loki.LogsReceiver":    {Wire: "loki.logs", Kind: WireKindReceiver},
	"otelcol.Consumer":     {Wire: "otel.any", Kind: WireKindReceiver},
	"pyroscope.Appendable": {Wire: "pyroscope.profiles", Kind: WireKindReceiver},
}

// NonPortGoTypes are Go types that look wire-ish but are plain configuration
// capsules, never a canvas connection.
var NonPortGoTypes = map[string]bool{
	"vcs.GitRepo": true,
}

// RoleFor derives a port's role from its wire kind and whether the field lives
// on the Exports struct (export=true) or the Arguments struct (export=false).
//
//	                     argument (Arguments)   export (Exports)
//	data      (targets)  accepts                produces
//	receiver  (receiver) produces               accepts
//
// A RECEIVER-kind export (prometheus.remote_write.receiver) is where data
// ENTERS the node, so it is drawn on the left even though it is an export.
// An argument taking a receiver list (forward_to) is where data LEAVES.
func RoleFor(kind WireKind, export bool) string {
	if kind == WireKindReceiver {
		if export {
			return RoleAccepts
		}
		return RoleProduces
	}
	if export {
		return RoleProduces
	}
	return RoleAccepts
}

// RefineOtelWire narrows the polymorphic otel.any wire type to a per-signal one
// when the field addressing it is signal-specific. Every otelcol consumer field
// in alloy is either an explicitly named signal (ConsumerArguments.Metrics /
// .Logs / .Traces, faro/beyla/splunkhec/fluentforward equivalents) or the
// genuinely polymorphic ConsumerExports.Input, which stays otel.any.
//
// field is the alloy tag name of the field itself, not the enclosing block.
func RefineOtelWire(wire, field string) string {
	if wire != "otel.any" {
		return wire
	}
	switch field {
	case "metrics":
		return "otel.metrics"
	case "logs":
		return "otel.logs"
	case "traces":
		return "otel.traces"
	}
	return wire
}

// PortName joins a port's block path into the stable dotted id a stored graph
// references. A top-level attribute yields its own name ("forward_to"); a
// consumer inside a tagged block yields "output.metrics".
//
// The renderer MUST NOT emit a dotted name as an attribute — Alloy attribute
// names may not contain a dot. A port with len(path) > 1 is written as nested
// blocks: path ["output","metrics"] becomes `output { metrics = [...] }`.
func PortName(path []string) string { return strings.Join(path, ".") }
