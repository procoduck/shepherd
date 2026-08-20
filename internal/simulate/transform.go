package simulate

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"shepherd/internal/netshape"
	"shepherd/internal/schema"
	"shepherd/internal/visual"
)

// The S3 simulation transform (§6.4 step 1). Four rules run in a fixed order —
// G (discovery and log-source stubs), D (destination endpoints), S (removal of
// secret sources and the references to them), K (the deny-by-default keep
// rule) and P (a self-checked post-condition) — over a deep copy of the
// authored graph.
//
// WHAT THIS TRANSFORM IS, AND WHAT IT IS NOT. It is the CREDENTIAL control: it
// bounds which authored VALUES leave the user's graph. It is NOT the
// reachability control, and no comment, doc or proof in this repo may say it
// is. Reachability is contained by the network the sandbox runs on — Docker's
// `internal: true` sim network, verified by execution in
// e2e/sandbox_egress_test.go. The transform REDUCES reachability (the
// target_set class below removes the trivial literal-address case); the network
// DENIES it.
//
// That split is not a preference. A discovery.relabel rule retargets a scrape
// at RUNTIME — `target_label = "__address__"` with a `replacement` assembled
// from regex captures — so the address never appears as a token in the rendered
// text and no analysis of that text can bound where the scrape lands. The
// static backstop that used to claim otherwise (rule P5, address_not_harness)
// could not see that graph at all while refusing five of six ordinary corpus
// graphs over inert relabel label names; it is deleted. See
// internal/simulate/provenance.go for the longer statement.
//
// K is the safety boundary for credentials, and it CONSTRUCTS rather than
// filters. Every
// surviving node's props are built fresh, copying in only the attribute paths
// the overlay's sim_keep names for that exact component, plus the paths G and D
// wrote themselves. There is no code path that copies an unclassified value
// into the output graph, which is what makes "nothing is ever left as authored"
// a structural fact rather than an aspiration.
//
// A keep entry carries a VALUE class as well as a path, because "this path may
// survive" and "this value may survive" are different questions. `targets` is
// the case that forced the distinction: the path has to survive or a scrape
// pipeline has nothing to scrape, while a literal __address__ inside it names a
// host outright. Its class, target_set, rebuilds each label set with
// __address__ and __scheme__ forced from the harness. Everything else is
// verbatim. That closes the LITERAL address case and nothing more — a relabel
// rule can still write __address__ downstream, which is why the network and not
// this class is what bounds reachability.
//
// It replaced a type-driven sweep that deleted every prop the artifact declared
// `secret`. That predicate held for 338 of the artifact's 6482 declared
// attribute paths; the other 6144 passed through untouched, so a credential in
// a `string`-typed attribute (otelcol.receiver.solace's auth.sasl_xauth2.bearer,
// otelcol.receiver.cloudflare's secret, otelcol.processor.resourcedetection's
// openshift.token) reached the sandbox verbatim, rendered with zero
// diagnostics, and passed real `alloy validate`. Type cannot be the allowlist.
//
// The order is part of the contract, not an implementation accident. G runs
// first because it replaces a stubbed node's props wholesale and so discards
// the credentials a discovery node carried before any later rule has to reason
// about them. D runs before S because §6.4's "URLs already rewritten; other
// secret uses get \"simulated\"" is only true in that order: a destination URL
// bound to a secret node must become the capture URL, not the string
// "simulated". S runs before K so a substituted placeholder is what K sees at a
// kept path. K runs before P so the post-condition sees the final graph.
//
// Every rule fails closed. An unmapped source is cannot_stub; an unmapped
// Destinations component is cannot_rewrite_destination; a component the overlay
// gives no keep list is cannot_keep_prop; one it marks sim_unsupported is
// unsupported_component; a rendered literal whose provenance is not the
// authored graph, the keep list or the harness is unknown_provenance.

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
	// RewritePropDropped is rule K's disclosure: an authored setting the
	// overlay's keep list does not name. §6.5 requires the user be told the
	// simulated pipeline is narrower than the one they authored, and this is
	// the entry that tells them which setting went.
	RewritePropDropped RewriteKind = "prop_dropped"
	// RewriteTargetAddressForced is the target_set class's disclosure: the
	// scrape target the user typed was replaced by the harness's synthetic
	// exporter. It is a separate kind from destination_endpoint because the
	// user has to be able to tell "where my pipeline WRITES was re-pointed"
	// from "what my pipeline READS was re-pointed" — they are different
	// fidelity losses and only one of them is in §6.4's headline.
	RewriteTargetAddressForced RewriteKind = "target_address_forced"
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
	CodeCannotKeepProp           = "cannot_keep_prop"
	CodeUnsupportedComponent     = "unsupported_component"
	CodeUnknownProvenance        = "unknown_provenance"
	// CodeIncompleteAfterKeep is the availability fail-closed check: rule K
	// built a node's props from the overlay's sim_keep allowlist and the
	// result is missing an attribute or block the artifact declares required.
	// The shipped overlay is guarded against this at build time
	// (internal/schema's validateRequiredCoverage); this code is what a
	// hand-edited or future overlay hits instead of shipping a config the
	// sandbox's real Alloy binary would reject with a diagnostic pinned to
	// the user's node for a value the transform itself removed.
	CodeIncompleteAfterKeep = "incomplete_after_keep"
)

// substitutedValue is what a non-secret reference to a removed secret source
// becomes (§6.4: "other secret uses get \"simulated\"").
const substitutedValue = "simulated"

const (
	categorySources      = "sources"
	categoryDestinations = "destinations"
	typeSecret           = "secret"
	typeString           = "string"
	typeCapsule          = "capsule"
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
	errs = append(errs, checkSupported(out, req)...)

	// written records every path the transform itself wrote, so rule K can tell
	// a harness endpoint it produced from a host the user authored at the same
	// path. Without it K would either drop rule D's own writes or have to trust
	// the value sitting there, and "trust the value sitting there" is the whole
	// defect this design replaces.
	written := writtenPaths{}

	r, e, stubbed := applyStubs(&out, req)
	rewrites, errs = append(rewrites, r...), append(errs, e...)

	r, e = rewriteDestinations(&out, req, written)
	rewrites, errs = append(rewrites, r...), append(errs, e...)

	r, removed := substituteRemovedRefs(&out, req)
	rewrites = append(rewrites, r...)

	// Rule K. Everything above wrote into a deep copy of the authored graph;
	// this is where that copy stops being the output and becomes the source the
	// output is CONSTRUCTED from. Deleting the call is the red proof for §6.4's
	// containment claim (docs/proofs/transform-secret-drop.md).
	r, e = keepProps(&out, req, written, stubbed, refusedNodes(errs))
	rewrites, errs = append(rewrites, r...), append(errs, e...)

	// Availability fail-closed check: a keep list that omits a required
	// attribute or block leaves rule K's own output broken, and without this
	// the transform would report success on a config the sandbox's real
	// Alloy binary is the first thing to reject — with a diagnostic pinned to
	// the user's node for a value the TRANSFORM removed, not the user.
	errs = append(errs, checkRequiredAfterKeep(out, req, stubbed, refusedNodes(errs))...)

	if len(errs) > 0 {
		sortErrors(errs)
		return TransformResult{}, errs
	}
	if e := checkContainment(out, req, removed, written, stubbed); len(e) > 0 {
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

// checkSupported refuses a graph naming a component the overlay marks
// sim_unsupported. These are components the sandbox would answer differently
// from production — otelcol.processor.resourcedetection detects the SIMULATOR's
// host, cloud and Kubernetes environment — or whose required address attribute
// names a production service the keep list cannot let through. §6.5 refuses
// that fidelity lie, so the run stops instead of shipping a plausible answer.
func checkSupported(doc visual.GraphDocument, req TransformRequest) TransformErrors {
	var errs TransformErrors
	for _, n := range doc.Nodes {
		reason := req.Policy.Components[n.Component].Unsupported
		if reason == "" {
			continue
		}
		errs = append(errs, TransformError{
			Code: CodeUnsupportedComponent, NodeID: n.ID, Component: n.Component,
			Message: fmt.Sprintf("cannot simulate %s — %s", n.Component, reason),
		})
	}
	return errs
}

// writtenPaths is the set of concrete props paths the transform wrote itself,
// keyed by node id. Concrete means block instances are numbered
// ("endpoint.0.url"), matching what setAtPath reports.
type writtenPaths map[string]bool

func (w writtenPaths) key(nodeID string, path []string) string {
	return nodeID + "\x00" + strings.Join(path, "\x00")
}

func (w writtenPaths) mark(nodeID string, path []string) { w[w.key(nodeID, path)] = true }

func (w writtenPaths) has(nodeID string, path []string) bool { return w[w.key(nodeID, path)] }

// refusedNodes is the set of node ids an earlier rule has already failed on.
func refusedNodes(errs TransformErrors) map[string]bool {
	out := make(map[string]bool, len(errs))
	for _, e := range errs {
		if e.NodeID != "" {
			out[e.NodeID] = true
		}
	}
	return out
}

// ---------------------------------------------------------------- rule G

// applyStubs replaces every Sources-category node the overlay maps to a
// synthetic source emitting the overlay's fixture. Disabled nodes are stubbed
// too: the sweep and the post-condition walk every node in the result, and a
// node that is disabled today can be re-enabled inside the sandbox by a later
// edit of the same graph.
//
// Eligibility to be examined here is the CATEGORY, not the component name. It
// used to be a "discovery."/"loki.source." prefix check, which meant an
// overlay-mapped stub on any other Sources-category name was silently never
// applied. local.file_match is the measured case: it exports "targets" and
// nothing else, shaped exactly like a discovery node, but carries neither
// prefix, so its discovery_stub entry was dead until this rule started reading
// Category (finding M9). internal/schema's registry guard was fixed to match:
// it used to require the same prefix on every discovery_stub key.
//
// A stubbed node's props are transform-built in full, so rule K skips it: the
// set of stubbed ids is returned for exactly that reason.
func applyStubs(doc *visual.GraphDocument, req TransformRequest) ([]Rewrite, TransformErrors, map[string]bool) {
	var rewrites []Rewrite
	var errs TransformErrors
	stubbed := map[string]StubSpec{}

	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		policy := req.Policy.Components[n.Component]
		if policy.Category != categorySources {
			continue
		}
		if policy.Stub == nil {
			// A discovery.*/loki.source.* node with no stub fails closed: its
			// whole purpose is producing targets or log lines for a downstream
			// pipeline, and §6.4 already gives its author a real answer — S2's
			// relabel simulator — so leaving it silently idle would be the
			// fidelity lie §6.5 refuses ("my pipeline drops everything" read
			// from a receiver nobody fed).
			//
			// Every other unstubbed Sources-category node — a
			// prometheus.exporter.*, an otelcol.receiver.*, a
			// prometheus.operator.*, local.file_match's siblings that are NOT
			// shaped like a discovery node — has no such downstream-only escape
			// hatch, and hard-failing every graph that contains one would make
			// S3 unusable for the single most common ingestion shape (an OTLP
			// receiver feeding a processor chain). It is left in the graph
			// instead, subject to rule K exactly like any other unstubbed node:
			// its sim_keep is empty — never a credential, and never an authored
			// address either, because the two Sources components whose target
			// list carried one (prometheus.exporter.blackbox and
			// prometheus.exporter.snmp, whose probe destination sits in an
			// ordinary `address` key that forcing __address__ never touched) are
			// sim_unsupported and refuse the run. None of its authored settings survive into
			// the render, and rule K's dropDeadWeight removes the node outright
			// once it feeds nothing — so what ships is either an inert component
			// still wired to a downstream, or nothing at all, never whatever the
			// user authored.
			if strings.HasPrefix(n.Component, "discovery.") || strings.HasPrefix(n.Component, "loki.source.") {
				errs = append(errs, TransformError{
					Code: CodeCannotStub, NodeID: n.ID, Component: n.Component,
					Message: fmt.Sprintf("cannot stub %s — use S2 for its downstream rules", n.Component),
				})
			}
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

	ids := make(map[string]bool, len(stubbed))
	for id := range stubbed {
		ids[id] = true
	}

	if len(stubbed) == 0 {
		return rewrites, errs, ids
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

	return rewrites, errs, ids
}

// targetsValue turns a fixture's label sets into the props shape the renderer
// serializes, substituting the harness address for EVERY label value that names
// a host — not only for __address__.
//
// The wider substitution is not tidiness. Several fixtures carry a realistic
// host name in a meta label (eureka's __meta_eureka_app_instance_hostname,
// dns's __meta_dns_name, puppetdb's __meta_puppetdb_certname), because relabel
// rules downstream match on those label names and a stub that invented them
// would make S3 behave differently from production. But the canonical relabel
// idiom for those mechanisms is `target_label = "__address__"` — copying the
// discovered host INTO the address — so a fixture host name is an address one
// user-authored rule away. Nothing downstream would catch that (a relabel
// result is computed at runtime), so the fixture must not carry a real host
// name in the first place. This is hygiene on values the TRANSFORM invents, not
// a reachability control on values the user authored.
func targetsValue(targets []map[string]string, address string) []interface{} {
	out := make([]interface{}, 0, len(targets))
	for _, t := range targets {
		m := make(map[string]interface{}, len(t))
		for k, v := range t {
			if len(netshape.Hosts(v)) > 0 {
				m[k] = address
				continue
			}
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
func rewriteDestinations(doc *visual.GraphDocument, req TransformRequest, written writtenPaths) ([]Rewrite, TransformErrors) {
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
				written.mark(n.ID, applied.path)
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
				written.mark(n.ID, applied.path)
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
				written.mark(n.ID, applied.path)
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

// ---------------------------------------------------------------- rule K

// keepProps CONSTRUCTS every surviving node's props from the overlay's keep
// list. It is the inversion the whole design turns on: the previous rule
// SUBTRACTED (delete every prop declared `secret`), so the 6144 attribute paths
// the artifact does not type `secret` passed through untouched. Here a value
// reaches the sandbox only if something put it on a list, so a credential in a
// `string`-typed attribute is absent because nothing kept it — not because a
// pattern recognised it.
//
// Three things may occupy a path in the constructed props:
//
//  1. a path rule G or D wrote — a harness endpoint, a stub's targets, a forced
//     TLS downgrade. Its provenance is the transform itself.
//  2. an authored path the component's sim_keep names, whose value is a plain
//     literal of the declared type.
//  3. nothing.
//
// Expressions are case 3 without exception. A {"$expr": ...} escape at any
// depth is arbitrary Alloy — sys.env("EXFIL_URL"), a reference to a component
// that reads a file — so no keep list entry admits one. Bindings are the same
// channel by another name (render.go writes bind.Prop = bind.Ref.Expr verbatim
// at the node's top level with no type check at all), so rule K removes every
// binding that survived rule D. That is what closes the binding leak: the
// previous filter dropped a binding only when topLevelType() said `secret`,
// which is "" for every nested or dotted prop and `string` for
// otelcol.receiver.cloudflare's `secret`.
func keepProps(doc *visual.GraphDocument, req TransformRequest, written writtenPaths, stubbed, refused map[string]bool) ([]Rewrite, TransformErrors) {
	var rewrites []Rewrite
	var errs TransformErrors

	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if refused[n.ID] {
			// An earlier rule already refused this node — an unmappable
			// destination, an unstubbable source. Reporting "and it has no keep
			// list" on top would bury the reason the user can act on.
			continue
		}
		if stubbed[n.ID] {
			// G replaced this node's props wholesale with transform-built
			// values; there is no authored value left to classify.
			continue
		}
		comp, known := req.Schema.Components[n.Component]
		if !known {
			continue // already reported by checkKnownComponents
		}
		policy := req.Policy.Components[n.Component]
		if policy.Keep == nil {
			// Fails closed rather than defaulting either way. internal/schema's
			// exhaustiveness guard makes this unreachable for the shipped
			// overlay; it is reachable for a hand-edited one, and a hand-edited
			// overlay is exactly when the deny side has to hold.
			errs = append(errs, TransformError{
				Code: CodeCannotKeepProp, NodeID: n.ID, Component: n.Component,
				Message: fmt.Sprintf(
					"cannot simulate %s — the overlay gives it no sim_keep allowlist, so no authored setting can be shown to be safe", n.Component),
			})
			continue
		}
		k := &keeper{node: *n, keep: policy.Keep, written: written, harness: req.Harness}
		n.Props = k.build(comp.Attributes, comp.Blocks, n.Props, nil, nil)
		rewrites = append(rewrites, k.rewrites...)
		errs = append(errs, k.errs...)
	}

	for _, b := range doc.Bindings {
		owner := visual.GraphNode{ID: b.Node}
		for _, n := range doc.Nodes {
			if n.ID == b.Node {
				owner = n
				break
			}
		}
		rewrites = append(rewrites, Rewrite{
			Kind: RewritePropDropped, NodeID: b.Node, NodeLabel: owner.Label, Component: owner.Component,
			Path:   []string{b.Prop},
			Detail: "a binding is an arbitrary expression the renderer emits verbatim; the sandbox config is built from literals only",
		})
	}
	doc.Bindings = nil

	rewrites = append(rewrites, dropDeadWeight(doc, req)...)

	return rewrites, errs
}

// dropDeadWeight removes a node whose keep list is empty — "nothing this
// component declares may cross into the sandbox" — once nothing consumes what
// it produces.
//
// Without this the husk stays in the config and the sandbox cannot even load
// it: local.file_match with its path_targets dropped fails `alloy validate`
// with "missing required attribute". The narrow condition is what makes the
// removal safe to do silently-but-disclosed — the node feeds nothing, so
// removing it cannot change any surviving component's inputs. A husk that STILL
// feeds something is left in place: its downstream is entitled to the wire, and
// deciding that such a graph cannot be simulated at all is an availability
// question this rule deliberately does not answer.
func dropDeadWeight(doc *visual.GraphDocument, req TransformRequest) []Rewrite {
	dead := map[string]visual.GraphNode{}
	for _, n := range doc.Nodes {
		keep := req.Policy.Components[n.Component].Keep
		if keep == nil || keep.Subtree || len(keep.Paths) > 0 || len(n.Props) > 0 {
			continue
		}
		dead[n.ID] = n
	}
	for _, e := range doc.Edges {
		delete(dead, e.From.Node)
	}
	if len(dead) == 0 {
		return nil
	}

	var rewrites []Rewrite
	nodes := make([]visual.GraphNode, 0, len(doc.Nodes))
	for _, n := range doc.Nodes {
		if _, gone := dead[n.ID]; gone {
			rewrites = append(rewrites, Rewrite{
				Kind: RewritePropDropped, NodeID: n.ID, NodeLabel: n.Label, Component: n.Component,
				Detail: "every authored setting was dropped and nothing consumes this component, so it was removed rather than left as a body the sandbox cannot load",
			})
			continue
		}
		nodes = append(nodes, n)
	}
	doc.Nodes = nodes

	edges := make([]visual.GraphEdge, 0, len(doc.Edges))
	for _, e := range doc.Edges {
		if n, gone := dead[e.To.Node]; gone {
			rewrites = append(rewrites, Rewrite{
				Kind: RewriteEdgeDropped, NodeID: e.To.Node, NodeLabel: n.Label, Component: n.Component,
				Detail: fmt.Sprintf("wire %s was dropped with the removed component", e.ID),
			})
			continue
		}
		edges = append(edges, e)
	}
	doc.Edges = edges

	return rewrites
}

// ------------------------------------------------------ availability check

// checkRequiredAfterKeep is the runtime half of the availability fix: for
// every node rule K built props for, walk the schema against the FINAL props
// — the same tree writeBody renders — and refuse the run if a required
// attribute or block is missing. The shipped overlay cannot reach this (the
// build-time guard in internal/schema's validateRequiredCoverage fails first
// for the same gap); this is the fail-closed backstop for a hand-edited or
// future overlay, matching keepProps's own "fails closed rather than
// defaulting either way" stance on policy.Keep == nil.
func checkRequiredAfterKeep(doc visual.GraphDocument, req TransformRequest, stubbed, refused map[string]bool) TransformErrors {
	var errs TransformErrors
	authored := make(map[string]visual.GraphNode, len(req.Graph.Nodes))
	for _, n := range req.Graph.Nodes {
		authored[n.ID] = n
	}
	for _, n := range doc.Nodes {
		if stubbed[n.ID] || refused[n.ID] {
			// A stubbed node's props are transform-built by rule G against a
			// DIFFERENT component (n.Component is now the stub's), and a
			// refused node's props were never rebuilt by rule K at all — both
			// are already reported, or are not this rule's concern.
			continue
		}
		comp, known := req.Schema.Components[n.Component]
		if !known {
			continue
		}
		ported := requiredPortPaths(comp.Inputs)
		after := requiredAfterKeep(comp.Attributes, comp.Blocks, n.Props, ported, nil)
		if len(after) == 0 {
			continue
		}
		// A gap already present in what the user authored is not rule K's
		// doing — a graph that was incomplete before Transform ever ran gets
		// the same diagnostic an unsimulated one would from real Alloy,
		// correctly naming the user's own node. Only a path that was present
		// as authored and is absent after keepProps built the final props is
		// this rule's concern: that is the shape the round-2 finding named
		// (loki.enrich's target_match_label, set by the user, dropped by an
		// incomplete keep list).
		//
		// Known imprecision: for a repeatable block, an instance keepProps
		// empties out entirely is dropped rather than kept as an empty
		// placeholder (build's own comment), which can shift a later
		// instance's index between the authored and final walks. A shifted
		// index can only ever add a spurious refusal, never hide a real one
		// — failing closed on an already-narrow, defense-in-depth check.
		before := requiredAfterKeep(comp.Attributes, comp.Blocks, authored[n.ID].Props, ported, nil)
		wasGap := make(map[string]bool, len(before))
		for _, g := range before {
			wasGap[g] = true
		}
		var newGaps []string
		for _, g := range after {
			if !wasGap[g] {
				newGaps = append(newGaps, g)
			}
		}
		if len(newGaps) == 0 {
			continue
		}
		errs = append(errs, TransformError{
			Code: CodeIncompleteAfterKeep, NodeID: n.ID, Component: n.Component,
			Message: fmt.Sprintf(
				"cannot simulate %s — after removing settings the sim_keep allowlist does not cover, the config would be missing required %s; this is an overlay gap, not something to fix in your graph",
				n.Component, strings.Join(newGaps, ", ")),
		})
	}
	return errs
}

// requiredPortPaths returns the dotted paths of every wireable input on a
// component. render.go's resolveEdges writes a wired port's value at render
// time, entirely outside of props (nodeRefs), so a required attribute that is
// also a port cannot be judged missing from props alone.
func requiredPortPaths(inputs []visual.PortSchema) map[string]bool {
	out := make(map[string]bool, len(inputs))
	for _, p := range inputs {
		path := p.Path
		if len(path) == 0 {
			name := p.Prop
			if name == "" {
				name = p.Export
			}
			path = []string{name}
		}
		out[strings.Join(path, ".")] = true
	}
	return out
}

// requiredAfterKeep walks the schema against a node's final built props,
// reporting the dotted path of every required, non-ported attribute or block
// rule K left absent. It mirrors keeper.build's own descent (schema-ordered
// attributes, then every instance of every declared block) so a clean walk
// here is equivalent to a clean render.
func requiredAfterKeep(attrs []visual.AttributeSchema, blocks []visual.BlockSchema, props map[string]interface{}, ported map[string]bool, path []string) []string {
	var gaps []string
	for _, a := range attrs {
		full := appendPath(path, a.Name)
		if !a.Required || ported[strings.Join(full, ".")] {
			continue
		}
		if v, present := props[a.Name]; !present || v == nil {
			gaps = append(gaps, strings.Join(full, "."))
		}
	}
	for _, b := range blocks {
		full := appendPath(path, b.Name)
		raw, present := props[b.Name]
		var instances []map[string]interface{}
		if present {
			var ok bool
			instances, _, ok = blockInstancesOf(raw)
			if !ok {
				continue // malformed shape; not this rule's concern
			}
		} else if !b.Required {
			// Optional and absent: nothing to check. A required attribute
			// nested inside an OPTIONAL block (otelcol.receiver.kafka's
			// authentication.sasl.password is the measured case) is only a
			// problem if the user authors that block at all, which is a
			// different, softer question than this unconditional check
			// answers — see the doc comment above.
			continue
		}
		if len(instances) == 0 {
			// Required but absent, or present-and-empty: still descend with
			// one synthetic instance. This is what keeps a required block
			// whose own content is entirely wired ports (otelcol's `output`
			// block: required so the syntax exists, populated by
			// resolveEdges outside of props) from being misreported — nothing
			// inside it is non-ported, so the recursion below finds no gap.
			instances = []map[string]interface{}{{}}
		}
		for idx, inst := range instances {
			gaps = append(gaps, requiredAfterKeep(b.Attributes, b.Blocks, inst, ported, appendPath(full, strconv.Itoa(idx)))...)
		}
	}
	return gaps
}

// keeper builds one node's props and collects the disclosure entries for
// everything it refused to copy.
type keeper struct {
	node     visual.GraphNode
	keep     *KeepSpec
	written  writtenPaths
	harness  HarnessEndpoints
	errs     TransformErrors
	rewrites []Rewrite
}

// build walks the schema — not the props — exactly as the renderer's writeBody
// does, so what it constructs is what the renderer can emit and nothing else. A
// props key the schema does not declare is therefore not copied at all.
//
// canon is the canonical schema path ("endpoint", "*", "url"), which the keep
// list is written in. concrete numbers each block instance ("endpoint", "0",
// "url"), which is what setAtPath reported to writtenPaths.
func (k *keeper) build(attrs []visual.AttributeSchema, blocks []visual.BlockSchema, authored map[string]interface{}, canon, concrete []string) map[string]interface{} {
	out := map[string]interface{}{}
	if authored == nil {
		return out
	}

	for _, a := range attrs {
		raw, present := authored[a.Name]
		if !present || raw == nil {
			continue
		}
		canonPath, concretePath := appendPath(canon, a.Name), appendPath(concrete, a.Name)
		if k.written.has(k.node.ID, concretePath) {
			out[a.Name] = raw
			continue
		}
		class, kept := k.keep.ClassOf(canonPath)
		switch {
		case a.Type == typeSecret || a.Type == typeCapsule:
			// Never keepable whatever the list says. A secret renders as the
			// renderer's own secret_by_value refusal, and a capsule renders as a
			// RAW EXPRESSION — the same code channel bindings are.
			k.drop(canonPath, fmt.Sprintf("declared %s: a %s can only come from a live credential or another component, neither of which exists in the sandbox", a.Type, a.Type))
		case !kept:
			k.drop(canonPath, "not on this component's sim_keep allowlist, so it cannot be shown to be free of credentials or endpoints")
		case containsExpr(raw):
			k.drop(canonPath, "an expression, not a literal; the sandbox config is built from literals only")
		case !literalMatchesType(raw, a.Type):
			k.drop(canonPath, fmt.Sprintf("value is %s but the schema declares %s", jsonKindOf(raw), a.Type))
		case class == ClassTargetSet:
			if built, ok := k.targetSet(raw, canonPath); ok {
				out[a.Name] = built
			}
		default:
			if built, ok := k.constrainKeys(raw, canonPath); ok {
				out[a.Name] = built
			}
		}
	}

	for _, b := range blocks {
		raw, present := authored[b.Name]
		if !present {
			continue
		}
		canonSeg := []string{b.Name}
		if b.Repeatable {
			canonSeg = append(canonSeg, "*")
		}
		instances, wasMap, ok := blockInstancesOf(raw)
		if !ok {
			k.drop(appendPath(canon, b.Name), "a block whose value is not an object or a list of objects")
			continue
		}
		built := make([]interface{}, 0, len(instances))
		for idx, inst := range instances {
			sub := k.build(b.Attributes, b.Blocks, inst,
				appendPath(canon, canonSeg...), appendPath(concrete, b.Name, strconv.Itoa(idx)))
			// An emptied block is dropped, but a block the user authored empty
			// is preserved: some components require an empty block to be
			// present, and rendering it exactly as authored is not a leak.
			if len(sub) > 0 || len(inst) == 0 {
				built = append(built, sub)
			}
		}
		switch {
		case len(built) == 0:
		case wasMap && len(built) == 1:
			out[b.Name] = built[0]
		default:
			out[b.Name] = built
		}
	}

	return out
}

// constrainKeys is rule K's guard over the segments of an attribute path that
// the ARTIFACT does not declare and internal/schema's build-time guard can
// therefore never read: the KEYS inside a kept value.
//
// The hole it closes was measured. A keep entry names a path
// ("prometheus.remote_write.external_labels"), and internal/schema checks every
// segment of that path against IsCredentialName before the overlay may ship it.
// For a `map`-typed attribute the effective path does not stop there — it is
// `external_labels.<whatever the user typed>` — and that last segment reaches
// the sandbox with nobody having looked at it. Thirteen kept paths were
// declared `map` when the review measured this, so thirteen open key spaces sat
// inside a deny-by-default allowlist; twelve remain (prometheus.scrape's
// `params` was dropped from the keep list outright rather than guarded, being
// the canonical query-string credential mechanism). The same predicate applied
// here closes all of them at once, and closes the thirteenth that an Alloy bump
// adds without anyone noticing, which is why this is a walk over the value and
// not a case list.
//
// It is a NARROWING on an already-allowlisted path, never an allowlist of its
// own: a key it does not recognise still only survives because the path it sits
// under was allowlisted. That asymmetry is what makes a name heuristic sound
// here when it is unsound as a drop rule — a false negative leaves the entry
// exactly where the keep list already put it, and a false positive costs one
// disclosed map entry.
//
// The measured false positive is `__meta_puppetdb_certname` (it matches `cert`)
// in a hand-written target set. It costs one label in one relabel simulation,
// it is disclosed as a prop_dropped entry, and a user reaching puppetdb through
// discovery.puppetdb — the ordinary way — never meets it, because rule G builds
// that node's targets and rule K skips stubbed nodes entirely.
//
// Returns false when the value had content and the guard removed all of it, so
// build drops the attribute rather than emitting an empty map the user never
// authored.
func (k *keeper) constrainKeys(v interface{}, canon []string) (interface{}, bool) {
	switch x := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for _, key := range sortedKeys(x) {
			path := appendPath(canon, key)
			if schema.IsCredentialName(key) {
				k.drop(path, "a key the user chose, not one the schema declares, and its name is credential-shaped; "+
					"the overlay's keep list names the attribute but nothing reviewed what a user would put inside it")
				continue
			}
			inner, ok := k.constrainKeys(x[key], path)
			if !ok {
				continue
			}
			out[key] = inner
		}
		return out, len(out) > 0 || len(x) == 0
	case []interface{}:
		out := make([]interface{}, 0, len(x))
		for i, el := range x {
			inner, ok := k.constrainKeys(el, appendPath(canon, strconv.Itoa(i)))
			if !ok {
				continue
			}
			out = append(out, inner)
		}
		return out, len(out) > 0 || len(x) == 0
	}
	return v, true
}

// Prometheus target-set meta labels. The two forced ones are what CRITICAL-2
// turned on: a literal `__address__` on prometheus.scrape named an arbitrary
// host, cleared the transform, cleared real `alloy validate` and cleared the
// simulator's endpoint allowlist. Forcing them makes the scrape land on the
// harness's synthetic exporter whatever LITERAL the user typed — which is a
// fidelity and defence-in-depth win, not a reachability guarantee: a relabel
// rule downstream can write __address__ again from data, and Docker's internal
// network is what answers that.
const (
	labelAddress = "__address__"
	labelScheme  = "__scheme__"
)

// steeringLabels are the remaining meta labels that redirect a request. They
// are removed rather than forced: unlike __address__ and __scheme__ they have
// no required value, and the harness serves its metrics at the default path.
// __param_ is a prefix rather than a name, so it is handled separately.
var steeringLabels = map[string]bool{
	"__metrics_path__": true,
	"__path__":         true,
	"__path_exclude__": true,
}

// targetSet rebuilds one target list under the target_set value class.
//
// It CONSTRUCTS each label set rather than editing the authored one, for the
// same reason rule K constructs props: an authored key nobody classified must
// not survive by default. __address__ and __scheme__ are written last and
// unconditionally, so a target set that omitted them, spelled them oddly, or
// carried five of them still leaves exactly one of each, pointing at the
// harness.
func (k *keeper) targetSet(raw interface{}, canon []string) (interface{}, bool) {
	list, isList := raw.([]interface{})
	if !isList {
		k.drop(canon, "a target set must be a list of label sets")
		return nil, false
	}
	if k.harness.TargetAddress == "" {
		// Fails the run rather than dropping the attribute: `targets` is
		// required on every component this class is granted to, so a dropped
		// one produces a config the sandbox cannot load, and "your simulation
		// silently did nothing" is the fidelity lie §6.5 refuses.
		k.errs = append(k.errs, TransformError{
			Code: CodeHarnessIncomplete, NodeID: k.node.ID, Component: k.node.Component,
			Message: "simulator target_address is not configured; a literal target set cannot be re-pointed at the harness",
		})
		return nil, false
	}

	out := make([]interface{}, 0, len(list))
	for idx, element := range list {
		labels, isMap := element.(map[string]interface{})
		if !isMap {
			k.drop(appendPath(canon, strconv.Itoa(idx)), "a target set entry must be a label set")
			continue
		}
		built := map[string]interface{}{}
		for _, name := range sortedKeys(labels) {
			path := appendPath(canon, strconv.Itoa(idx), name)
			switch {
			case name == labelAddress || name == labelScheme:
				continue // written below, from the harness
			case steeringLabels[name] || strings.HasPrefix(name, "__param_"):
				k.drop(path, "a target meta label that redirects the request; the sandbox scrapes the harness at its own path")
				continue
			case schema.IsCredentialName(name):
				// A target label name is user-chosen, so it is the same
				// unreviewed segment constrainKeys guards on a kept map — and it
				// is the one the round-2 review measured, because the class
				// forced __address__ and then copied every OTHER label
				// verbatim while checkProvenance's allowed set laundered their
				// values (addTargetSetLiterals). `targets = [{__address__ = …,
				// token = "…"}]` was a credential in a kept path.
				k.drop(path, "a target label the user named, whose name is credential-shaped; "+
					"the target_set class rebuilds the label set, so an unreviewed label name is not carried over")
				continue
			}
			value, isString := labels[name].(string)
			if !isString {
				// Alloy's discovery.Target is map[string]string; anything else
				// would not render as a label set anyway.
				k.drop(path, fmt.Sprintf("a target label must be a string, and this is %s", jsonKindOf(labels[name])))
				continue
			}
			built[name] = value
		}

		authoredAddress, _ := labels[labelAddress].(string) //nolint:errcheck // an absent or non-string address simply has nothing to disclose
		built[labelAddress] = k.harness.TargetAddress
		built[labelScheme] = "http"
		out = append(out, built)

		k.rewrites = append(k.rewrites, Rewrite{
			Kind: RewriteTargetAddressForced, NodeID: k.node.ID, NodeLabel: k.node.Label, Component: k.node.Component,
			Path: appendPath(canon, strconv.Itoa(idx), labelAddress), From: authoredAddress, To: k.harness.TargetAddress,
			Detail: "target re-pointed at the simulator's synthetic exporter over plain http; the sandbox reads nothing the user named",
		})
	}
	return out, true
}

func (k *keeper) drop(path []string, detail string) {
	k.rewrites = append(k.rewrites, Rewrite{
		Kind: RewritePropDropped, NodeID: k.node.ID, NodeLabel: k.node.Label, Component: k.node.Component,
		Path: path, Detail: detail,
	})
}

// blockInstancesOf normalizes a props value into block instances the same way
// the renderer's blockInstances does, and reports whether it was authored as a
// single object so the constructed shape matches the authored one.
func blockInstancesOf(v interface{}) (instances []map[string]interface{}, wasMap, ok bool) {
	switch x := v.(type) {
	case nil:
		return nil, false, true
	case map[string]interface{}:
		if _, isExpr := rawExprOf(x); isExpr {
			return nil, false, false
		}
		return []map[string]interface{}{x}, true, true
	case []interface{}:
		out := make([]map[string]interface{}, 0, len(x))
		for _, el := range x {
			m, isMap := el.(map[string]interface{})
			if !isMap {
				return nil, false, false
			}
			out = append(out, m)
		}
		return out, false, true
	}
	return nil, false, false
}

// containsExpr reports whether a props value carries a {"$expr": ...} escape
// anywhere inside it — at the top, inside a list element, or inside a map
// value. One anywhere disqualifies the whole value: a list whose third element
// is sys.env("TOKEN") is not a literal list.
func containsExpr(v interface{}) bool {
	switch x := v.(type) {
	case map[string]interface{}:
		if _, isExpr := rawExprOf(x); isExpr {
			return true
		}
		for _, key := range sortedKeys(x) {
			if containsExpr(x[key]) {
				return true
			}
		}
	case []interface{}:
		for _, el := range x {
			if containsExpr(el) {
				return true
			}
		}
	}
	return false
}

// literalMatchesType mirrors what the renderer's serializeTyped emits WITHOUT a
// prop_type_mismatch diagnostic. Being stricter than the renderer would drop
// values a user can legitimately author; being looser would let rule K copy a
// value the renderer then refuses, which makes the disclosure list lie about
// what reached the sandbox.
func literalMatchesType(v interface{}, typ string) bool {
	switch typ {
	case "duration", typeString:
		switch v.(type) {
		case string, bool, float64:
			return true
		}
		return false
	case "number":
		switch x := v.(type) {
		case float64:
			return true
		case string:
			_, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
			return err == nil
		}
		return false
	case "bool":
		switch x := v.(type) {
		case bool:
			return true
		case string:
			return x == "true" || x == "false"
		}
		return false
	case "list":
		_, ok := v.([]interface{})
		return ok
	case "map":
		_, ok := v.(map[string]interface{})
		return ok
	}
	// An attribute the artifact leaves untyped renders by its JSON shape; there
	// is no mismatch to detect.
	return true
}

func jsonKindOf(v interface{}) string {
	switch v.(type) {
	case string:
		return "a string"
	case bool:
		return "a bool"
	case float64:
		return "a number"
	case []interface{}:
		return "a list"
	case map[string]interface{}:
		return "an object"
	}
	return "an unrecognised value"
}

// ---------------------------------------------------------------- rule P

// checkContainment re-asserts the transform's own claim before returning.
// Running the §11 item 6 assertions inside Transform rather than only in tests
// means the containment claim holds for every real run, not only for inputs
// somebody thought to write a test for: a graph shape nobody anticipated fails
// the run instead of leaking.
//
// P1 is structural over the transformed graph, P1' is over the PARSED render,
// and P2/P2'/P3/P4 are textual — a bug that defeats one is unlikely to defeat
// the others. Every one of them is a CREDENTIAL assertion. None of them is a
// reachability assertion, and the one that used to claim to be (P5,
// address_not_harness) is deleted: see the file header and
// internal/simulate/provenance.go.
//
// Their shared virtue is independence from rule K's own output: P1 re-derives
// the allowed paths from the policy, P1' accounts for literals against the
// AUTHORED graph. That is what the ORIGINAL P1 lost — it walked the transformed
// props asserting `declared != secret`, the same predicate the old sweep
// applied, so it could only ever catch a bug in the sweep and never the
// type-declaration gap. It duly passed all three of the CRITICAL-1 leaks.
func checkContainment(doc visual.GraphDocument, req TransformRequest, removed []removedSource, written writtenPaths, stubbed map[string]bool) TransformErrors {
	var errs TransformErrors

	// P1 — structural: every surviving props path is one the overlay's keep
	// list names or one the transform wrote itself. Re-derived from the policy
	// rather than from rule K's output, so a rule K that copied an unkept path
	// fails here.
	for i := range doc.Nodes {
		n := doc.Nodes[i]
		if stubbed[n.ID] {
			continue
		}
		comp, ok := req.Schema.Components[n.Component]
		if !ok {
			continue
		}
		keep := req.Policy.Components[n.Component].Keep
		walkPropsCanonical(comp.Attributes, comp.Blocks, n.Props, nil, nil,
			func(_ interface{}, declared string, canon, concrete []string) {
				if written.has(n.ID, concrete) {
					return
				}
				if keep.Keeps(canon) && declared != typeSecret && declared != typeCapsule {
					return
				}
				errs = append(errs, TransformError{
					Code: CodeContainmentViolated, NodeID: n.ID, Component: n.Component,
					Message: fmt.Sprintf(
						"transformed graph carries %s at %s, which neither the sim_keep allowlist nor the transform's own writes account for",
						n.Component, strings.Join(canon, ".")),
				})
			})
	}

	// P1'' — the node LABEL, which is the one authored string that reaches the
	// render without passing rule K at all: the renderer writes it into the
	// block header, and checkProvenance cannot see it either because a block
	// label is not a string literal in the parsed AST.
	//
	// THE RESIDUAL IS ACCEPTED, DELIBERATELY, and this is the statement of it.
	// sanitizeLabel lower-cases the label and replaces every character outside
	// [a-z0-9_], which destroys most credential alphabets (base64 padding, the
	// case distinctions a JWT depends on, every punctuation-bearing key format)
	// but leaves a lowercase hex or alphanumeric secret intact. Nothing better
	// is available: the label is the user's own name for their own node, it is
	// never assembled from another field, and no allowlist can be written over
	// free text. So the check below is the shape refusal — the same P4
	// predicate, applied to the AUTHORED label because the sanitized form has
	// already lost the case a shape depends on — and a lowercase secret typed
	// into a node title is a known, disclosed gap rather than an unexamined one.
	for _, n := range doc.Nodes {
		if what := credentialShapeOf(n.Label); what != "" {
			errs = append(errs, TransformError{
				Code: CodeContainmentViolated, NodeID: n.ID, Component: n.Component,
				Message: fmt.Sprintf(
					"node label has a credential shape (%s) and the renderer writes it into the block header; rename the node", what),
			})
		}
	}

	// P1 also covers bindings, which the renderer emits as bare attributes
	// carrying an arbitrary expression. Rule K removes all of them, so any
	// survivor is a bug.
	for _, b := range doc.Bindings {
		errs = append(errs, TransformError{
			Code: CodeContainmentViolated, NodeID: b.Node,
			Message: fmt.Sprintf("transformed graph still binds %q; the sandbox config is built from literals only", b.Prop),
		})
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

	// P1' and P4 — one parse of the render, two questions of it. Provenance
	// (P1') accounts for every string literal against the AUTHORED graph and the
	// keep list; P4 refuses a credential-shaped literal.
	errs = append(errs, checkProvenance(res.Content, req, stubbed)...)

	return errs
}

// ---------------------------------------------------------------- shared

// canonicalVisitor is called for every leaf attribute the schema declares and
// the props carry, with both path forms: canon uses "*" for a repeatable block
// (the form the keep list is written in) and concrete numbers each instance
// (the form setAtPath reports).
type canonicalVisitor func(value interface{}, declaredType string, canon, concrete []string)

// walkPropsCanonical descends props against the schema exactly as the
// renderer's writeBody does, reporting both path forms. It is read-only, unlike
// walkPropsTyped, which exists to delete.
func walkPropsCanonical(attrs []visual.AttributeSchema, blocks []visual.BlockSchema, props map[string]interface{}, canon, concrete []string, visit canonicalVisitor) {
	if props == nil {
		return
	}
	for _, a := range attrs {
		raw, present := props[a.Name]
		if !present {
			continue
		}
		visit(raw, a.Type, appendPath(canon, a.Name), appendPath(concrete, a.Name))
	}
	for _, b := range blocks {
		raw, present := props[b.Name]
		if !present {
			continue
		}
		instances, _, ok := blockInstancesOf(raw)
		if !ok {
			continue
		}
		canonSeg := []string{b.Name}
		if b.Repeatable {
			canonSeg = append(canonSeg, "*")
		}
		for idx, inst := range instances {
			walkPropsCanonical(b.Attributes, b.Blocks, inst,
				appendPath(canon, canonSeg...), appendPath(concrete, b.Name, strconv.Itoa(idx)), visit)
		}
	}
}

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
