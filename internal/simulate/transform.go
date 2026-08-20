package simulate

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"shepherd/internal/visual"
)

// The S3 simulation transform (§6.4 step 1). Four rules run in a fixed order —
// G (discovery and log-source stubs), D (destination endpoints), S (the secret
// sweep) and P (a self-checked post-condition) — over a deep copy of the
// authored graph.
//
// The order is part of the contract, not an implementation accident. G runs
// first because it replaces a stubbed node's props wholesale and so discards
// the credentials a discovery node carried before any later rule has to reason
// about them. D runs before S because §6.4's "URLs already rewritten; other
// secret uses get \"simulated\"" is only true in that order: a destination URL
// bound to a secret node must become the capture URL, not the string
// "simulated". S runs before P so the post-condition sees the final graph.
//
// Every rule fails closed. An unmapped source is cannot_stub; an unmapped
// Destinations component is cannot_rewrite_destination. Nothing is ever left as
// authored, because "left as authored" is exactly how a real credential or a
// real endpoint reaches the sandbox.

// TransformRequest is the whole input to Transform. Nothing else is read: no
// clock, no network, no filesystem, no config lookup. That purity is what makes
// §11 item 6's "heavy coverage here, it's the safety boundary" achievable
// without a container.
type TransformRequest struct {
	Graph   visual.GraphDocument // the authored graph
	Schema  visual.SchemaPayload // attribute types and block paths; drives the secret sweep
	Policy  Policy               // overlay simulation policy
	Harness HarnessEndpoints     // injected; never a constant
}

// TransformResult is the transformed graph plus the disclosure list §6.4 step 4
// renders as "What was rewritten".
type TransformResult struct {
	Graph    visual.GraphDocument
	Rewrites []Rewrite
}

// HarnessEndpoints are the simulator's addresses. They are a required argument
// rather than a package constant so the compose service name lives in one
// config default and a Kubernetes deployment is a config change, not a code
// change. A rule that needs an empty field fails with harness_incomplete; there
// is deliberately no fallback.
type HarnessEndpoints struct {
	CaptureBaseURL  string // scheme+host+port, no trailing slash
	OTLPGRPCAddress string // host:port, no scheme — the otelcol gRPC client takes a bare address
	SyslogHost      string
	SyslogPort      int
	CaptureDir      string // tmpfs directory for otelcol.exporter.file
	TargetAddress   string // host:port of the synthetic exporter; becomes every stub's __address__
	LogDir          string // tmpfs directory the synthetic log emitter writes fixture files into
}

// RewriteKind is the closed set of transformations the disclosure can report.
type RewriteKind string

// Rewrite kinds.
const (
	RewriteDestinationEndpoint  RewriteKind = "destination_endpoint"
	RewriteDiscoveryStubbed     RewriteKind = "discovery_stubbed"
	RewriteLogSourceStubbed     RewriteKind = "log_source_stubbed"
	RewriteSecretNodeRemoved    RewriteKind = "secret_node_removed"
	RewriteSecretDropped        RewriteKind = "secret_dropped"
	RewriteSecretRefSubstituted RewriteKind = "secret_ref_substituted" //nolint:gosec // a rewrite kind name, not a credential
	RewriteEdgeDropped          RewriteKind = "edge_dropped"
)

// Rewrite is one entry in the disclosure list. From carries the user's own
// authored destination URL and is populated ONLY for destination_endpoint: the
// three secret_* constructors take no value argument at all, so a credential
// cannot structurally reach this struct. LogValue redacts From on top of that,
// because the disclosure is both rendered in a browser and logged server-side.
type Rewrite struct {
	Kind      RewriteKind `json:"kind"`
	NodeID    string      `json:"node_id"`
	NodeLabel string      `json:"node_label"`
	Component string      `json:"component"`
	Path      []string    `json:"path,omitempty"`
	From      string      `json:"from,omitempty"`
	To        string      `json:"to,omitempty"`
	Detail    string      `json:"detail"`
}

// LogValue redacts From, matching the GraphConfig/SecurityConfig pattern in
// internal/config. A destination URL is the user's to see in their own UI and
// nobody else's to find in a log file.
func (r Rewrite) LogValue() slog.Value {
	from := ""
	if r.From != "" {
		from = "***"
	}
	return slog.GroupValue(
		slog.String("kind", string(r.Kind)),
		slog.String("node_id", r.NodeID),
		slog.String("component", r.Component),
		slog.String("path", strings.Join(r.Path, ".")),
		slog.String("from", from),
		slog.String("to", r.To),
		slog.String("detail", r.Detail),
	)
}

// TransformError is one reason a graph cannot be simulated.
type TransformError struct {
	Code      string `json:"code"`
	NodeID    string `json:"node_id,omitempty"`
	Component string `json:"component,omitempty"`
	Message   string `json:"message"`
}

// TransformErrors is every reason at once. A user with three unstubbable
// sources should see three problems, not fix-and-retry three times.
type TransformErrors []TransformError

func (e TransformErrors) Error() string {
	parts := make([]string, len(e))
	for i, te := range e {
		parts[i] = te.Message
	}
	return strings.Join(parts, "; ")
}

// Error codes. The set is closed; a caller may switch on it exhaustively.
const (
	CodeCannotStub               = "cannot_stub"
	CodeCannotRewriteDestination = "cannot_rewrite_destination"
	CodeHarnessIncomplete        = "harness_incomplete"
	CodeUnknownComponent         = "unknown_component"
	CodeContainmentViolated      = "containment_violated"
)

// substitutedValue is what a non-secret reference to a removed secret source
// becomes (§6.4: "other secret uses get \"simulated\"").
const substitutedValue = "simulated"

const (
	categorySources      = "sources"
	categoryDestinations = "destinations"
	typeSecret           = "secret"
	typeString           = "string"
	// stubStaticComponent is what §6.4's "a discovery.static-equivalent"
	// resolves to. discovery.static does not exist in Alloy v1.18.1 — it is
	// absent from the shipped artifact and from the binary — so the stub is a
	// real discovery.relabel carrying a literal targets list and no rules. Its
	// export is named output, not targets, which is why outbound edges are
	// re-pointed below.
	stubStaticComponent = "discovery.relabel"
	// stubLokiComponent is the log-source stub. loki.source.file declares
	// exactly the port names loki.source.kubernetes does (targets accepts,
	// forward_to produces), so downstream forward_to wires need no rewriting at
	// all — which removes a whole class of edge-remapping bug.
	stubLokiComponent = "loki.source.file"
)

// Transform rewrites an authored graph into one that is safe to run in the
// sandbox. It never mutates req.Graph: a caller rendering the authored graph
// for the Code tab and the transformed graph for the sandbox in the same
// request must see no cross-talk.
//
// On any error the returned Graph is the zero value. A partially-transformed
// graph must not be reachable even by a caller that ignores err.
func Transform(req TransformRequest) (TransformResult, error) {
	out, err := deepCopyGraph(req.Graph)
	if err != nil {
		return TransformResult{}, TransformErrors{{Code: CodeUnknownComponent, Message: err.Error()}}
	}

	var rewrites []Rewrite
	var errs TransformErrors

	errs = append(errs, checkKnownComponents(out, req.Schema)...)

	r, e := applyStubs(&out, req)
	rewrites, errs = append(rewrites, r...), append(errs, e...)

	r, e = rewriteDestinations(&out, req)
	rewrites, errs = append(rewrites, r...), append(errs, e...)

	r, removed := substituteRemovedRefs(&out, req)
	rewrites = append(rewrites, r...)

	// The rules above delete the secret forms they know about by PATH and by
	// REFERENCE. This sweep is what makes "no credential reaches the sandbox" a
	// property of the transform rather than of a case list: it deletes every
	// prop whose DECLARED schema type is secret, at any depth, in any value
	// form — including a sys.env() expression that is neither at a mapped
	// endpoint path nor a reference to a secret-source node.
	//
	// Removing this line is the red proof for §6.4's containment claim
	// (docs/proofs/transform-secret-drop.md). It only proves anything because
	// rules G and D delete by path and reference and never opportunistically by
	// type: a refactor that makes rewriteDestinations strip secret-typed
	// siblings would leave the tests green with this line gone, and the proof
	// would silently stop proving.
	rewrites = append(rewrites, dropSecretTypedProps(&out, req.Schema)...)

	if len(errs) > 0 {
		sortErrors(errs)
		return TransformResult{}, errs
	}
	if e := checkContainment(out, req, removed); len(e) > 0 {
		return TransformResult{}, e
	}
	sortRewrites(rewrites)
	return TransformResult{Graph: out, Rewrites: rewrites}, nil
}

// deepCopyGraph clones through JSON, the same bridge mgmtapi already uses to
// move a schema map into a typed payload. It also normalizes every props value
// to the JSON shapes the renderer expects.
func deepCopyGraph(in visual.GraphDocument) (visual.GraphDocument, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return visual.GraphDocument{}, fmt.Errorf("simulate: copy graph: %w", err)
	}
	var out visual.GraphDocument
	if err := json.Unmarshal(b, &out); err != nil {
		return visual.GraphDocument{}, fmt.Errorf("simulate: copy graph: %w", err)
	}
	return out, nil
}

// checkKnownComponents refuses a graph naming a component the schema does not
// describe. Without a schema there are no declared types, so the secret sweep
// has nothing to sweep against — the transform must not guess.
func checkKnownComponents(doc visual.GraphDocument, schema visual.SchemaPayload) TransformErrors {
	var errs TransformErrors
	for _, n := range doc.Nodes {
		if _, ok := schema.Components[n.Component]; !ok {
			errs = append(errs, TransformError{
				Code: CodeUnknownComponent, NodeID: n.ID, Component: n.Component,
				Message: fmt.Sprintf("component %q is not in the schema for this graph; it cannot be simulated", n.Component),
			})
		}
	}
	return errs
}

// ---------------------------------------------------------------- rule G

// applyStubs replaces every Sources-category discovery.* and loki.source.* node
// with a synthetic source emitting the overlay's fixture. Disabled nodes are
// stubbed too: the sweep and the post-condition walk every node in the result,
// and a node that is disabled today can be re-enabled inside the sandbox by a
// later edit of the same graph.
func applyStubs(doc *visual.GraphDocument, req TransformRequest) ([]Rewrite, TransformErrors) {
	var rewrites []Rewrite
	var errs TransformErrors
	stubbed := map[string]StubSpec{}

	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		policy := req.Policy.Components[n.Component]
		if policy.Category != categorySources {
			continue
		}
		if !strings.HasPrefix(n.Component, "discovery.") && !strings.HasPrefix(n.Component, "loki.source.") {
			continue
		}
		if policy.Stub == nil {
			// §6.4, verbatim. A receiver nobody feeds sits healthy and idle and
			// captures nothing, which a user reads as "my pipeline drops
			// everything" — the fidelity lie §6.5 refuses.
			errs = append(errs, TransformError{
				Code: CodeCannotStub, NodeID: n.ID, Component: n.Component,
				Message: fmt.Sprintf("cannot stub %s — use S2 for its downstream rules", n.Component),
			})
			continue
		}
		spec := *policy.Stub
		authored := n.Component

		switch spec.Type {
		case StubTypeStatic:
			targets, ok := StubTargets(spec.Fixture)
			if !ok {
				errs = append(errs, TransformError{
					Code: CodeCannotStub, NodeID: n.ID, Component: authored,
					Message: fmt.Sprintf("cannot stub %s — fixture %q is not in the stub fixture library", authored, spec.Fixture),
				})
				continue
			}
			if req.Harness.TargetAddress == "" {
				errs = append(errs, TransformError{
					Code: CodeHarnessIncomplete, NodeID: n.ID, Component: authored,
					Message: "simulator target_address is not configured; discovery cannot be stubbed",
				})
				continue
			}
			n.Component = stubStaticComponent
			n.Props = map[string]interface{}{"targets": targetsValue(targets, req.Harness.TargetAddress)}
			stubbed[n.ID] = spec
			rewrites = append(rewrites, Rewrite{
				Kind: RewriteDiscoveryStubbed, NodeID: n.ID, NodeLabel: n.Label, Component: authored,
				To: stubStaticComponent,
				Detail: fmt.Sprintf("replaced by %s emitting the %q fixture; authored settings, bindings and inbound wires were dropped",
					stubStaticComponent, spec.Fixture),
			})
		case StubTypeLokiFile:
			if _, ok := StubLogLines(spec.Fixture); !ok {
				errs = append(errs, TransformError{
					Code: CodeCannotStub, NodeID: n.ID, Component: authored,
					Message: fmt.Sprintf("cannot stub %s — fixture %q is not in the stub fixture library", authored, spec.Fixture),
				})
				continue
			}
			if req.Harness.LogDir == "" {
				errs = append(errs, TransformError{
					Code: CodeHarnessIncomplete, NodeID: n.ID, Component: authored,
					Message: "simulator log_dir is not configured; log sources cannot be stubbed",
				})
				continue
			}
			n.Component = stubLokiComponent
			n.Props = map[string]interface{}{"targets": []interface{}{map[string]interface{}{
				"__path__": strings.TrimSuffix(req.Harness.LogDir, "/") + "/" + StubLogFileName(spec.Fixture),
				"job":      spec.Fixture,
			}}}
			stubbed[n.ID] = spec
			rewrites = append(rewrites, Rewrite{
				Kind: RewriteLogSourceStubbed, NodeID: n.ID, NodeLabel: n.Label, Component: authored,
				To: stubLokiComponent,
				Detail: fmt.Sprintf("replaced by %s tailing the %q fixture the harness writes; authored settings, bindings and inbound wires were dropped",
					stubLokiComponent, spec.Fixture),
			})
		default:
			errs = append(errs, TransformError{
				Code: CodeCannotStub, NodeID: n.ID, Component: authored,
				Message: fmt.Sprintf("cannot stub %s — unknown stub type %q", authored, spec.Type),
			})
		}
	}

	if len(stubbed) == 0 {
		return rewrites, errs
	}

	labels := map[string]string{}
	comps := map[string]string{}
	for _, n := range doc.Nodes {
		labels[n.ID] = n.Label
		comps[n.ID] = n.Component
	}

	kept := make([]visual.GraphEdge, 0, len(doc.Edges))
	for _, e := range doc.Edges {
		if _, ok := stubbed[e.To.Node]; ok {
			// A stub emits its fixture; it reads nothing. Keeping the wire would
			// reference a port the stub no longer accepts.
			rewrites = append(rewrites, Rewrite{
				Kind: RewriteEdgeDropped, NodeID: e.To.Node, NodeLabel: labels[e.To.Node], Component: comps[e.To.Node],
				Detail: fmt.Sprintf("wire %s into port %q was dropped: the stubbed source produces the fixture instead", e.ID, e.To.Port),
			})
			continue
		}
		if spec, ok := stubbed[e.From.Node]; ok && spec.Type == StubTypeStatic {
			// discovery.relabel exports output, not targets.
			e.From.Port = "output"
		}
		kept = append(kept, e)
	}
	doc.Edges = kept

	binds := make([]visual.GraphBinding, 0, len(doc.Bindings))
	for _, b := range doc.Bindings {
		if _, ok := stubbed[b.Node]; ok {
			continue
		}
		binds = append(binds, b)
	}
	doc.Bindings = binds

	return rewrites, errs
}

// targetsValue turns a fixture's label sets into the props shape the renderer
// serializes, substituting the harness address for the fixture's placeholder.
func targetsValue(targets []map[string]string, address string) []interface{} {
	out := make([]interface{}, 0, len(targets))
	for _, t := range targets {
		m := make(map[string]interface{}, len(t))
		for k, v := range t {
			m[k] = v
		}
		m["__address__"] = address
		out = append(out, m)
	}
	return out
}

// ---------------------------------------------------------------- rule D

// rewriteDestinations points every Destinations node at the capture harness. An
// unmapped Destinations component is an error rather than a pass-through:
// otelcol.exporter.datadog proves why — its api.api_key is secret AND required,
// so rule S would delete it and the render would stop validating, while leaving
// it alone would ship a real API key into the sandbox.
func rewriteDestinations(doc *visual.GraphDocument, req TransformRequest) ([]Rewrite, TransformErrors) {
	var rewrites []Rewrite
	var errs TransformErrors

	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		policy := req.Policy.Components[n.Component]
		if policy.Category != categoryDestinations {
			continue
		}
		if policy.Destination == nil {
			errs = append(errs, TransformError{
				Code: CodeCannotRewriteDestination, NodeID: n.ID, Component: n.Component,
				Message: fmt.Sprintf("cannot rewrite destination %s — the capture harness cannot emulate it, so a sandbox run would report an empty capture that is not a pipeline fault", n.Component),
			})
			continue
		}
		spec := *policy.Destination

		if spec.Receiver == ReceiverNone {
			rewrites = append(rewrites, Rewrite{
				Kind: RewriteDestinationEndpoint, NodeID: n.ID, NodeLabel: n.Label, Component: n.Component,
				Detail: "local sink; nothing to rewrite",
			})
			continue
		}

		value, err := receiverEndpoint(spec.Receiver, req.Harness, n.Label)
		if err != "" {
			errs = append(errs, TransformError{
				Code: CodeHarnessIncomplete, NodeID: n.ID, Component: n.Component, Message: err,
			})
			continue
		}

		if n.Props == nil {
			n.Props = map[string]interface{}{}
		}
		for _, path := range spec.EndpointPaths {
			for _, applied := range setAtPath(n.Props, path, value) {
				rewrites = append(rewrites, Rewrite{
					Kind: RewriteDestinationEndpoint, NodeID: n.ID, NodeLabel: n.Label, Component: n.Component,
					Path: applied.path, From: applied.previous, To: value,
					Detail: "endpoint re-pointed at the simulator's capture receiver",
				})
			}
		}

		// The syslog exporter splits host and port across two attributes, which
		// is also why the harness cannot be described by a single base URL.
		if spec.Receiver == ReceiverSyslog {
			if req.Harness.SyslogPort == 0 {
				errs = append(errs, TransformError{
					Code: CodeHarnessIncomplete, NodeID: n.ID, Component: n.Component,
					Message: "simulator syslog_port is not configured",
				})
				continue
			}
			for _, applied := range setAtPath(n.Props, []string{"port"}, float64(req.Harness.SyslogPort)) {
				rewrites = append(rewrites, Rewrite{
					Kind: RewriteDestinationEndpoint, NodeID: n.ID, NodeLabel: n.Label, Component: n.Component,
					Path: applied.path, From: applied.previous, To: strconv.Itoa(req.Harness.SyslogPort),
					Detail: "syslog port re-pointed at the simulator's capture receiver",
				})
			}
		}

		for _, key := range sortedKeys(spec.Ensure) {
			forced := spec.Ensure[key]
			for _, applied := range setAtPath(n.Props, strings.Split(key, "."), forced) {
				rewrites = append(rewrites, Rewrite{
					Kind: RewriteDestinationEndpoint, NodeID: n.ID, NodeLabel: n.Label, Component: n.Component,
					Path: applied.path, To: fmt.Sprint(forced),
					Detail: "forced for the unauthenticated in-pod capture endpoint",
				})
			}
		}

		// A binding whose prop names the last segment of a mapped endpoint path
		// points the destination somewhere real. The renderer emits bindings at
		// the node's top level only, so such a binding renders as a bogus
		// top-level attribute — the transform still has to remove it, and must
		// not assume bindings are well-placed.
		tails := map[string]bool{}
		for _, path := range spec.EndpointPaths {
			tails[path[len(path)-1]] = true
		}
		binds := make([]visual.GraphBinding, 0, len(doc.Bindings))
		for _, b := range doc.Bindings {
			if b.Node != n.ID || !tails[b.Prop] {
				binds = append(binds, b)
				continue
			}
			rewrites = append(rewrites, Rewrite{
				Kind: RewriteDestinationEndpoint, NodeID: n.ID, NodeLabel: n.Label, Component: n.Component,
				Path: []string{b.Prop}, From: strings.TrimSpace(b.Ref.Expr), To: value,
				Detail: "endpoint binding replaced by the simulator's capture receiver",
			})
		}
		doc.Bindings = binds
	}
	return rewrites, errs
}

// receiverEndpoint returns the address for a capture receiver, or a message
// naming the harness field that is missing. The otlp_grpc case is bare host:port
// with no scheme, because that is the shape the otelcol gRPC client takes.
func receiverEndpoint(receiver string, h HarnessEndpoints, label string) (string, string) {
	base := strings.TrimSuffix(h.CaptureBaseURL, "/")
	needBase := func(suffix string) (string, string) {
		if base == "" {
			return "", "simulator capture_base_url is not configured"
		}
		return base + suffix, ""
	}
	switch receiver {
	case ReceiverPrometheus:
		return needBase(CapturePathPrometheus)
	case ReceiverLoki:
		return needBase(CapturePathLoki)
	case ReceiverPyroscope:
		return needBase(CapturePathPyroscope)
	case ReceiverOTLPHTTP:
		return needBase(CapturePrefixOTLPHTTP)
	case ReceiverFaro:
		return needBase(CapturePathFaro)
	case ReceiverSplunkHEC:
		return needBase(CapturePathSplunkHEC)
	case ReceiverOTLPGRPC:
		if h.OTLPGRPCAddress == "" {
			return "", "simulator otlp_grpc_address is not configured"
		}
		return h.OTLPGRPCAddress, ""
	case ReceiverSyslog:
		if h.SyslogHost == "" {
			return "", "simulator syslog_host is not configured"
		}
		return h.SyslogHost, ""
	case ReceiverFile:
		if h.CaptureDir == "" {
			return "", "simulator capture_dir is not configured"
		}
		return strings.TrimSuffix(h.CaptureDir, "/") + "/" + visual.SanitizeLabel(label) + ".json", ""
	}
	return "", fmt.Sprintf("simulation receiver %q is not one the capture harness serves", receiver)
}

// applied records one concrete write performed by setAtPath.
type applied struct {
	path     []string
	previous string
}

// setAtPath writes value at a schema path, creating the blocks it passes
// through. A "*" segment expands over every instance of a repeatable block, and
// over a freshly created first instance when the block is absent: a destination
// the user never gave a URL still has to point at the harness, or the sandbox
// run reports an empty capture that looks like a pipeline fault.
func setAtPath(container map[string]interface{}, path []string, value interface{}) []applied {
	return setAtPathIn(container, path, value, nil)
}

func setAtPathIn(container map[string]interface{}, path []string, value interface{}, prefix []string) []applied {
	if len(path) == 0 || container == nil {
		return nil
	}
	head := path[0]
	if len(path) == 1 {
		prev := describeValue(container[head])
		container[head] = value
		return []applied{{path: appendPath(prefix, head), previous: prev}}
	}
	if path[1] == "*" {
		instances := blockInstanceList(container, head)
		out := []applied{}
		for i, inst := range instances {
			out = append(out, setAtPathIn(inst, path[2:], value, appendPath(prefix, head, strconv.Itoa(i)))...)
		}
		return out
	}
	inst := blockInstanceSingle(container, head)
	return setAtPathIn(inst, path[1:], value, appendPath(prefix, head, "0"))
}

// blockInstanceList normalizes container[name] into a list of block instances,
// creating one empty instance when the block is absent.
func blockInstanceList(container map[string]interface{}, name string) []map[string]interface{} {
	switch v := container[name].(type) {
	case map[string]interface{}:
		if _, isExpr := rawExprOf(v); !isExpr {
			return []map[string]interface{}{v}
		}
	case []interface{}:
		out := make([]map[string]interface{}, 0, len(v))
		for _, el := range v {
			if m, ok := el.(map[string]interface{}); ok {
				out = append(out, m)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	fresh := map[string]interface{}{}
	container[name] = []interface{}{fresh}
	return []map[string]interface{}{fresh}
}

func blockInstanceSingle(container map[string]interface{}, name string) map[string]interface{} {
	return blockInstanceList(container, name)[0]
}

// ---------------------------------------------------------------- rule S1

// substituteRemovedRefs removes every secret-source node and rewrites what
// referenced it. Nothing in the shipped artifact exports an arbitrary map, and
// all fourteen config-category components declare empty outputs, so §6.4's
// "stub component returning harness-known dummy values" cannot be a real Alloy
// component. It is resolved here as removal plus substitution, which is also the
// only reading consistent with §11 item 6's zero-secret-typed-literals
// post-condition: a literal stub value on a secret-typed attribute would itself
// violate it.
//
// Returns the removed nodes so the post-condition can assert their reference
// tokens are absent from the rendered text.
func substituteRemovedRefs(doc *visual.GraphDocument, req TransformRequest) ([]Rewrite, []removedSource) {
	var rewrites []Rewrite
	var removed []removedSource

	for _, n := range doc.Nodes {
		policy := req.Policy.Components[n.Component]
		if policy.SecretSource == nil {
			continue
		}
		removed = append(removed, removedSource{
			id: n.ID, label: n.Label, component: n.Component, mode: policy.SecretSource.Mode,
			token: n.Component + "." + visual.SanitizeLabel(n.Label) + ".",
		})
	}
	if len(removed) == 0 {
		return nil, nil
	}

	byID := map[string]removedSource{}
	for _, rs := range removed {
		byID[rs.id] = rs
		rewrites = append(rewrites, Rewrite{
			Kind: RewriteSecretNodeRemoved, NodeID: rs.id, NodeLabel: rs.label, Component: rs.component,
			Detail: "secret source removed; the sandbox never learns the value it would have supplied",
		})
	}

	nodes := make([]visual.GraphNode, 0, len(doc.Nodes))
	labels := map[string]string{}
	comps := map[string]string{}
	for _, n := range doc.Nodes {
		labels[n.ID], comps[n.ID] = n.Label, n.Component
		if _, gone := byID[n.ID]; !gone {
			nodes = append(nodes, n)
		}
	}
	doc.Nodes = nodes

	edges := make([]visual.GraphEdge, 0, len(doc.Edges))
	for _, e := range doc.Edges {
		if _, gone := byID[e.From.Node]; gone {
			rewrites = append(rewrites, Rewrite{
				Kind: RewriteEdgeDropped, NodeID: e.From.Node, NodeLabel: labels[e.From.Node], Component: comps[e.From.Node],
				Detail: fmt.Sprintf("wire %s was dropped with the removed secret source", e.ID),
			})
			continue
		}
		if _, gone := byID[e.To.Node]; gone {
			rewrites = append(rewrites, Rewrite{
				Kind: RewriteEdgeDropped, NodeID: e.To.Node, NodeLabel: labels[e.To.Node], Component: comps[e.To.Node],
				Detail: fmt.Sprintf("wire %s was dropped with the removed secret source", e.ID),
			})
			continue
		}
		edges = append(edges, e)
	}
	doc.Edges = edges

	nodeByID := map[string]visual.GraphNode{}
	for _, n := range doc.Nodes {
		nodeByID[n.ID] = n
	}

	binds := make([]visual.GraphBinding, 0, len(doc.Bindings))
	for _, b := range doc.Bindings {
		rs, matched := matchRemovedBinding(removed, b)
		if !matched {
			binds = append(binds, b)
			continue
		}
		owner := nodeByID[b.Node]
		declared := topLevelType(req.Schema, owner.Component, b.Prop)
		if canSubstitute(rs.mode, declared) {
			if owner.Props == nil {
				owner.Props = map[string]interface{}{}
				setNodeProps(doc, b.Node, owner.Props)
			}
			owner.Props[b.Prop] = substitutedValue
			rewrites = append(rewrites, Rewrite{
				Kind: RewriteSecretRefSubstituted, NodeID: b.Node, NodeLabel: owner.Label, Component: owner.Component,
				Path: []string{b.Prop}, To: substitutedValue,
				Detail: fmt.Sprintf("binding to the removed %s was replaced by a placeholder", rs.component),
			})
			continue
		}
		rewrites = append(rewrites, Rewrite{
			Kind: RewriteSecretDropped, NodeID: b.Node, NodeLabel: owner.Label, Component: owner.Component,
			Path:   []string{b.Prop},
			Detail: fmt.Sprintf("binding to the removed %s was dropped", rs.component),
		})
	}
	doc.Bindings = binds

	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		comp, ok := req.Schema.Components[n.Component]
		if !ok {
			continue
		}
		node := *n
		walkPropsTyped(comp.Attributes, comp.Blocks, n.Props, nil, func(container map[string]interface{}, key, declared string, path []string) bool {
			rs, hit := referencesRemoved(removed, container[key])
			if !hit {
				return false
			}
			if canSubstitute(rs.mode, declared) {
				container[key] = substitutedValue
				rewrites = append(rewrites, Rewrite{
					Kind: RewriteSecretRefSubstituted, NodeID: node.ID, NodeLabel: node.Label, Component: node.Component,
					Path: path, To: substitutedValue,
					Detail: fmt.Sprintf("reference to the removed %s was replaced by a placeholder", rs.component),
				})
				return false
			}
			delete(container, key)
			rewrites = append(rewrites, Rewrite{
				Kind: RewriteSecretDropped, NodeID: node.ID, NodeLabel: node.Label, Component: node.Component,
				Path:   path,
				Detail: fmt.Sprintf("reference to the removed %s was dropped", rs.component),
			})
			return true
		})
	}

	return rewrites, removed
}

type removedSource struct {
	id, label, component, mode, token string
}

// canSubstitute reports whether a reference may become the literal "simulated".
// Only a plain string can: a secret-typed attribute must stay absent (a literal
// there is exactly what render's secret_by_value refuses), a capsule has no
// literal equivalent under drop_ref, and a list- or map-typed attribute given a
// bare string would render as a type mismatch. Anything else is deleted, which
// is the more restrictive answer.
func canSubstitute(mode, declaredType string) bool {
	if mode != SecretModeLiteral {
		return false
	}
	return declaredType == typeString || declaredType == ""
}

// matchRemovedBinding matches by node id AND by token, because a binding can
// name the source either way: the inspector records Ref.Node, but a hand-edited
// or re-parsed graph can carry only the expression.
func matchRemovedBinding(removed []removedSource, b visual.GraphBinding) (removedSource, bool) {
	for _, rs := range removed {
		if b.Ref.Node == rs.id || strings.Contains(b.Ref.Expr, rs.token) {
			return rs, true
		}
	}
	return removedSource{}, false
}

// referencesRemoved reports whether a props value mentions a removed secret
// source anywhere inside it — as a $expr escape at any depth, as a capsule-typed
// raw string, or nested inside a list element or map value.
func referencesRemoved(removed []removedSource, v interface{}) (removedSource, bool) {
	switch x := v.(type) {
	case string:
		for _, rs := range removed {
			if strings.Contains(x, rs.token) {
				return rs, true
			}
		}
	case map[string]interface{}:
		if expr, ok := rawExprOf(x); ok {
			for _, rs := range removed {
				if strings.Contains(expr, rs.token) {
					return rs, true
				}
			}
			return removedSource{}, false
		}
		for _, key := range sortedKeys(x) {
			if rs, hit := referencesRemoved(removed, x[key]); hit {
				return rs, true
			}
		}
	case []interface{}:
		for _, el := range x {
			if rs, hit := referencesRemoved(removed, el); hit {
				return rs, true
			}
		}
	}
	return removedSource{}, false
}

// ---------------------------------------------------------------- rule S2

// dropSecretTypedProps deletes every prop whose declared schema type is secret,
// at any depth and in any value form, and every binding onto a secret-typed
// top-level attribute. This is the type-driven half of rule S and the only code
// path that removes a credential which is neither a reference to a secret
// source nor at a known endpoint path.
//
// Keys are deleted, never set to nil: serializeTyped skips nil, but a surviving
// key makes the structural post-condition false and would need a value-aware
// exception.
func dropSecretTypedProps(doc *visual.GraphDocument, schema visual.SchemaPayload) []Rewrite {
	var rewrites []Rewrite

	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		comp, ok := schema.Components[n.Component]
		if !ok {
			continue
		}
		node := *n
		walkPropsTyped(comp.Attributes, comp.Blocks, n.Props, nil, func(container map[string]interface{}, key, declared string, path []string) bool {
			if declared != typeSecret {
				return false
			}
			delete(container, key)
			rewrites = append(rewrites, Rewrite{
				Kind: RewriteSecretDropped, NodeID: node.ID, NodeLabel: node.Label, Component: node.Component,
				Path:   path,
				Detail: "declared a secret in the schema; dropped so no credential reaches the sandbox",
			})
			return true
		})
	}

	nodeByID := map[string]visual.GraphNode{}
	for _, n := range doc.Nodes {
		nodeByID[n.ID] = n
	}
	binds := make([]visual.GraphBinding, 0, len(doc.Bindings))
	for _, b := range doc.Bindings {
		owner, known := nodeByID[b.Node]
		if !known || topLevelType(schema, owner.Component, b.Prop) != typeSecret {
			binds = append(binds, b)
			continue
		}
		// Removed outright rather than blanked: an empty Ref.Expr would trip the
		// renderer's empty_binding_expr diagnostic.
		rewrites = append(rewrites, Rewrite{
			Kind: RewriteSecretDropped, NodeID: b.Node, NodeLabel: owner.Label, Component: owner.Component,
			Path:   []string{b.Prop},
			Detail: "binding onto a secret-typed attribute; dropped so no credential reaches the sandbox",
		})
	}
	doc.Bindings = binds

	return rewrites
}

// ---------------------------------------------------------------- rule P

// checkContainment re-asserts the transform's own claim before returning.
// Running the §11 item 6 assertions inside Transform rather than only in tests
// means the containment claim holds for every real run, not only for inputs
// somebody thought to write a test for: a graph shape nobody anticipated fails
// the run instead of leaking. P1 is structural and P2/P2'/P3 are textual on
// purpose — a bug that defeats one is unlikely to defeat the other.
func checkContainment(doc visual.GraphDocument, req TransformRequest, removed []removedSource) TransformErrors {
	var errs TransformErrors

	// P1 — structural: no surviving prop is declared secret.
	for i := range doc.Nodes {
		n := doc.Nodes[i]
		comp, ok := req.Schema.Components[n.Component]
		if !ok {
			continue
		}
		props := n.Props
		walkPropsTyped(comp.Attributes, comp.Blocks, props, nil, func(_ map[string]interface{}, _, declared string, path []string) bool {
			if declared == typeSecret {
				errs = append(errs, TransformError{
					Code: CodeContainmentViolated, NodeID: n.ID, Component: n.Component,
					Message: fmt.Sprintf("transformed graph still carries a secret-typed value at %s", strings.Join(path, ".")),
				})
			}
			return false
		})
	}

	// P1 also covers bindings: the renderer emits them as bare attributes, so a
	// binding onto a secret-typed attribute is a credential in the config even
	// though no props key holds it.
	for _, b := range doc.Bindings {
		for _, n := range doc.Nodes {
			if n.ID != b.Node || topLevelType(req.Schema, n.Component, b.Prop) != typeSecret {
				continue
			}
			errs = append(errs, TransformError{
				Code: CodeContainmentViolated, NodeID: n.ID, Component: n.Component,
				Message: fmt.Sprintf("transformed graph still binds the secret-typed attribute %q", b.Prop),
			})
		}
	}

	res := visual.Render(doc, req.Schema)
	for _, d := range res.Diagnostics {
		switch d.Code {
		case "label_collision":
			// Colliding labels make Render emit nothing, so the textual checks
			// below would pass vacuously. Fail closed instead.
			errs = append(errs, TransformError{
				Code: CodeContainmentViolated, NodeID: d.NodeID,
				Message: "transformed graph has colliding labels, so containment cannot be verified against the rendered text",
			})
		case "secret_by_value":
			// P3.
			errs = append(errs, TransformError{
				Code: CodeContainmentViolated, NodeID: d.NodeID, Message: d.Message,
			})
		}
	}

	// P2 — reference: no removed secret source is still addressed by name.
	for _, rs := range removed {
		if strings.Contains(res.Content, rs.token) {
			errs = append(errs, TransformError{
				Code: CodeContainmentViolated, NodeID: rs.id, Component: rs.component,
				Message: fmt.Sprintf("transformed render still references the removed secret source %s", rs.token),
			})
		}
	}

	// P2' — name: no secret-source component appears in the render at all.
	// Coarser than P2 and cheap; catches a reference the label token would miss.
	for _, name := range sortedComponentNames(req.Policy) {
		if req.Policy.Components[name].SecretSource == nil {
			continue
		}
		if strings.Contains(res.Content, name+".") || strings.Contains(res.Content, name+` "`) {
			errs = append(errs, TransformError{
				Code: CodeContainmentViolated, Component: name,
				Message: fmt.Sprintf("transformed render still mentions the secret-source component %s", name),
			})
		}
	}

	return errs
}

// ---------------------------------------------------------------- shared

// propVisitor is called for every props key the schema declares as an
// attribute. It returns true when it removed the key, which is what lets the
// walker prune a block instance that its own edits emptied — and only then, so
// a block the user authored empty renders exactly as authored.
type propVisitor func(container map[string]interface{}, key, declaredType string, path []string) bool

// walkPropsTyped descends props against the schema the same way the renderer's
// writeBody does: schema-ordered attributes at this level, then every instance
// of every declared block. Only what the renderer can emit is visited, which is
// what makes a clean walk equivalent to a clean render.
func walkPropsTyped(attrs []visual.AttributeSchema, blocks []visual.BlockSchema, props map[string]interface{}, path []string, visit propVisitor) bool {
	if props == nil {
		return false
	}
	removedAny := false
	for _, a := range attrs {
		if _, present := props[a.Name]; !present {
			continue
		}
		if visit(props, a.Name, a.Type, appendPath(path, a.Name)) {
			removedAny = true
		}
	}
	for _, b := range blocks {
		raw, present := props[b.Name]
		if !present {
			continue
		}
		switch v := raw.(type) {
		case map[string]interface{}:
			if _, isExpr := rawExprOf(v); isExpr {
				continue
			}
			if walkPropsTyped(b.Attributes, b.Blocks, v, appendPath(path, b.Name, "0"), visit) {
				removedAny = true
				if len(v) == 0 {
					delete(props, b.Name)
				}
			}
		case []interface{}:
			changed := false
			kept := make([]interface{}, 0, len(v))
			for i, el := range v {
				m, isMap := el.(map[string]interface{})
				if !isMap {
					kept = append(kept, el)
					continue
				}
				if walkPropsTyped(b.Attributes, b.Blocks, m, appendPath(path, b.Name, strconv.Itoa(i)), visit) {
					changed = true
					if len(m) == 0 {
						continue
					}
				}
				kept = append(kept, m)
			}
			if changed {
				removedAny = true
				if len(kept) == 0 {
					delete(props, b.Name)
				} else {
					props[b.Name] = kept
				}
			}
		}
	}
	return removedAny
}

// topLevelType returns the declared type of a component's top-level attribute,
// or "" when the schema does not declare it. Bindings are emitted at the node's
// top level, so this is the type a binding actually lands on.
func topLevelType(schema visual.SchemaPayload, component, prop string) string {
	comp, ok := schema.Components[component]
	if !ok {
		return ""
	}
	for _, a := range comp.Attributes {
		if a.Name == prop {
			return a.Type
		}
	}
	return ""
}

func setNodeProps(doc *visual.GraphDocument, id string, props map[string]interface{}) {
	for i := range doc.Nodes {
		if doc.Nodes[i].ID == id {
			doc.Nodes[i].Props = props
			return
		}
	}
}

// rawExprOf mirrors the renderer's {"$expr": "..."} escape detection.
func rawExprOf(v interface{}) (string, bool) {
	m, ok := v.(map[string]interface{})
	if !ok || len(m) != 1 {
		return "", false
	}
	s, ok := m["$expr"].(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", false
	}
	return strings.TrimSpace(s), true
}

// describeValue renders a previous prop value for the disclosure list. It is
// only ever called on destination endpoint paths, whose declared type is string
// or number.
func describeValue(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	}
	if expr, ok := rawExprOf(v); ok {
		return expr
	}
	return fmt.Sprint(v)
}

func appendPath(prefix []string, more ...string) []string {
	out := make([]string, 0, len(prefix)+len(more))
	out = append(out, prefix...)
	out = append(out, more...)
	return out
}

func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedComponentNames(p Policy) []string {
	names := make([]string, 0, len(p.Components))
	for name := range p.Components {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// sortRewrites makes the disclosure list a function of the graph's content
// rather than of the order its nodes happen to sit in the JSON array — required
// by the permutation-invariance property the renderer already holds to, and by
// the e2e assertion that counts entries.
func sortRewrites(rs []Rewrite) {
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.NodeID != b.NodeID {
			return a.NodeID < b.NodeID
		}
		pa, pb := strings.Join(a.Path, "."), strings.Join(b.Path, ".")
		if pa != pb {
			return pa < pb
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Detail < b.Detail
	})
}

func sortErrors(es TransformErrors) {
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].NodeID != es[j].NodeID {
			return es[i].NodeID < es[j].NodeID
		}
		if es[i].Code != es[j].Code {
			return es[i].Code < es[j].Code
		}
		return es[i].Message < es[j].Message
	})
}
