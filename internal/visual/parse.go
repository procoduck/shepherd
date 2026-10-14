package visual

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/grafana/alloy/syntax/ast"
	"github.com/grafana/alloy/syntax/parser"
	"github.com/grafana/alloy/syntax/printer"
	"github.com/grafana/alloy/syntax/token"
)

// ParseResult is the output of ParseAlloy.
type ParseResult struct {
	Doc     GraphDocument
	Opaque  bool   // true if the file couldn't be fully mapped to a graph
	Warning string // human-readable note when parsing is partial
	// Bindings are the references to another node that are NOT wires — a
	// remote.kubernetes.secret's `data`, or a port referenced inside a larger
	// expression — wherever they sit in a body, nested blocks and function
	// calls included. They are a PROJECTION for the read-only graph view: the
	// expression itself stays in Props as {"$expr": ...} so Render reproduces
	// it, which is also why they are kept out of Doc.Bindings — that channel
	// renders `prop = expr` at the node's top level, wrong for a nested prop and
	// a duplicate for a top-level one. Every consumer that turns a GraphView
	// response back into an editable draft must drop them
	// (web/src/visual/graphViewFlow.ts, builderDraftFromGraphView).
	Bindings []GraphBinding
	// Parents maps each node inside an expanded module to its group node's id
	// (ParseOptions.ExpandModules; nil otherwise). Like Bindings it is a
	// read-only graph view projection, kept off GraphNode on purpose: the
	// builder's documents (wizard_state, Render's input) have no place for a
	// module. VisualService.GraphView sends it as GraphNode.parent.
	Parents map[string]string
}

// configBlocks are the top-level Alloy blocks that are not components. They
// have no place in the graph model, so a file containing one is only partially
// mappable. Used when no schema is supplied to say which names are components.
var configBlocks = map[string]bool{
	"logging": true, "tracing": true, "http": true, "declare": true,
	"argument": true, "export": true, "foreach": true, "remotecfg": true,
	"livedebugging": true, "import.file": true, "import.git": true,
	"import.http": true, "import.string": true,
}

// ParseOptions selects what ParseAlloyWithOptions maps beyond the builder's
// own graph model. The zero value is ParseAlloy: exactly the parse the builder
// round-trips through (parse_roundtrip_test.go), which nothing here changes.
type ParseOptions struct {
	// ExpandModules is the read-only graph view's parse (VisualService.GraphView
	// only — never Render, Validate, DiffRevisions or the builder). Each
	// `declare` becomes a group node (component "declare", label = the module
	// name) whose components are mapped like top-level ones and carry the
	// group's id in ParseResult.Parents, with ids prefixed by the module path so
	// they are unique across modules. A module's `argument` and `export` blocks
	// become ports on the group: a reference to argument.X.value that is a port
	// of its consumer is an edge to or from the group's X port, an export's
	// value an edge between the group's port and the component it names, and
	// each module instance gets an edge from (or to) the group port for every
	// wired argument it sets and every wired export — so a component inside a
	// module can be traced through the instance to its destination. Any other
	// argument reference is a display-only binding on the group (#341). A
	// graph with a module stays Opaque: the builder cannot edit one, and its
	// fallback load refuses an opaque graph.
	ExpandModules bool
}

// MaxModuleDepth bounds how many `declare` blocks ParseAlloyWithOptions
// expands inside one another; a deeper one is kept as an opaque node.
const MaxModuleDepth = 8

// ParseAlloy converts raw Alloy pipeline text into a best-effort GraphDocument.
//
// Top-level component blocks become nodes; their bodies become props, with
// nested blocks kept as nested values (D2) and non-literal expressions kept as
// {"$expr": "..."} so nothing is quietly rewritten. References between
// components become edges in dataflow direction (D1) when a schema is supplied
// — the schema is what says whether an export carries data (the consumer holds
// the reference) or is a receiver (the producer does). Without one, edges are
// oriented referenced → referencing, which is right for data exports only.
//
// Anything that cannot be mapped is reported: the statement is either kept as
// an "opaque.*" node or named in Warning, and Opaque is set. This is one-way
// and lossy by design — it backs the read-only graph view of a hand-written
// pipeline (§4.3). A pipeline built in the builder is reloaded from its
// wizard_state instead (D3).
func ParseAlloy(content, schemaVersion string, schema ...SchemaPayload) ParseResult {
	return ParseAlloyWithOptions(content, schemaVersion, ParseOptions{}, schema...)
}

// ParseAlloyWithOptions is ParseAlloy with the graph view's extras (see
// ParseOptions). With the zero ParseOptions it is ParseAlloy.
func ParseAlloyWithOptions(content, schemaVersion string, opts ParseOptions, schema ...SchemaPayload) ParseResult {
	gp := &graphParser{
		opts:       opts,
		blocks:     map[string]*ast.BlockStmt{},
		nodeScope:  map[string]*parseScope{},
		instanceOf: map[string]*module{},
	}
	if len(schema) > 0 && len(schema[0].Components) > 0 {
		gp.schema, gp.hasSchema = schema[0], true
	}
	f, err := parser.ParseFile("<pipeline>", []byte(content))
	if err != nil {
		return ParseResult{
			Doc: GraphDocument{
				Kind:          "alloy-graph/v1",
				SchemaVersion: schemaVersion,
				Meta:          Meta{CreatedWith: "shepherd-parser"},
			},
			Opaque:  true,
			Warning: fmt.Sprintf("syntax error: %v", err),
		}
	}
	doc := GraphDocument{
		Kind:          "alloy-graph/v1",
		SchemaVersion: schemaVersion,
		Viewport:      Viewport{Zoom: 1},
		Meta:          Meta{CreatedWith: "shepherd-parser"},
	}

	gp.addBody(&doc, &parseScope{nodeByRef: map[string]string{}, modules: map[string]*module{}}, f.Body)
	gp.wireAll(&doc)

	return ParseResult{Doc: doc, Opaque: gp.opaque, Warning: gp.warning(), Bindings: gp.bindings, Parents: gp.parents}
}

// parseScope is one body's namespace: the top level, or a module's body.
// References resolve within it only — an Alloy module cannot see the
// components around it, nor they its components.
type parseScope struct {
	parent *parseScope
	// module is the module whose body this is; nil at the top level.
	module *module
	depth  int
	// path is the module path, sanitized and joined with "__" (empty at the
	// top level): the prefix that keeps inner node ids unique across modules.
	path string
	// nodeByRef maps "component.label" → node id, for edge resolution.
	nodeByRef map[string]string
	// modules are the declares defined directly in this body.
	modules map[string]*module
}

func (sc *parseScope) group() string {
	if sc.module == nil {
		return ""
	}
	return sc.module.groupID
}

// lookupModule finds the module a block name instantiates, from this body
// outwards.
func (sc *parseScope) lookupModule(name string) *module {
	for cur := sc; cur != nil; cur = cur.parent {
		if m, ok := cur.modules[name]; ok {
			return m
		}
	}
	return nil
}

// module is one expanded `declare`.
type module struct {
	name    string
	groupID string
	body    *parseScope
	// args and exports are the module's ports, keyed by name; the orders are
	// source order, which is the order their instance edges are added in.
	args        map[string]*modulePort
	argOrder    []string
	exports     map[string]*modulePort
	exportOrder []string
	exportStmts []*ast.BlockStmt
}

// modulePort is an argument or export of a module. wired is set once a
// reference inside the module made it an edge; outward says which way data
// crosses the module boundary through it — out for a list of receivers passed
// in or an export of data, in for data passed in or an exported receiver.
type modulePort struct {
	wired   bool
	outward bool
}

type opaqueNode struct {
	id, name string
	scope    *parseScope
}

type graphParser struct {
	schema    SchemaPayload
	hasSchema bool
	dropped   []string
	opaque    bool
	bindings  []GraphBinding

	opts ParseOptions
	// x, y is the next grid slot.
	x, y      float64
	blocks    map[string]*ast.BlockStmt
	nodeScope map[string]*parseScope
	// opaqueNodes are resolved to module instances once every declare is
	// known (a module may be used before it is declared).
	opaqueNodes []opaqueNode
	instanceOf  map[string]*module
	modules     []*module
	parents     map[string]string
}

// addBody maps one body's statements to nodes: the top level, or (graph view
// only) a module's body.
func (gp *graphParser) addBody(doc *GraphDocument, sc *parseScope, body ast.Body) {
	for _, stmt := range body {
		block, ok := stmt.(*ast.BlockStmt)
		if !ok {
			attr, isAttr := stmt.(*ast.AttributeStmt)
			switch {
			case isAttr && sc.module != nil:
				gp.drop("attribute %q in module %q has no place in the graph", attr.Name.Name, sc.module.name)
			case isAttr:
				gp.drop("top-level attribute %q has no place in the graph", attr.Name.Name)
			default:
				gp.drop("a top-level statement could not be mapped to a node")
			}
			continue
		}
		name := strings.Join(block.Name, ".")
		if gp.opts.ExpandModules {
			switch {
			case name == "declare" && block.Label != "" && sc.depth < MaxModuleDepth:
				gp.addModule(doc, sc, block)
				continue
			case name == "declare" && block.Label != "":
				gp.drop("module %q is nested more than %d deep and is shown as written", block.Label, MaxModuleDepth)
			case sc.module != nil && block.Label != "" && (name == "argument" || name == "export"):
				gp.addPort(sc.module, name, block)
				continue
			}
		}
		component := name
		if !gp.isComponent(name) {
			// Kept, not dropped: the caller can badge it, and the "opaque."
			// prefix is what tells VisualBuilderPage to refuse to edit.
			component = "opaque." + name
			gp.drop("%q is not a component and cannot be edited as a node", name)
		}
		label := block.Label
		if label == "" {
			label = "default"
		}
		id := gp.nodeID(doc, sc, component, label)

		props := gp.props(block.Body, name)
		if component != name {
			// An opaque block's props are a best-effort reading of a body the
			// graph model does not describe (a `declare`'s nested components
			// are dropped entirely), so the graph view shows the block itself,
			// printed back from its parse (comments are not kept). Both
			// renderers skip this key.
			props[rawKey] = gp.print(block)
			gp.opaqueNodes = append(gp.opaqueNodes, opaqueNode{id: id, name: name, scope: sc})
		}
		gp.addNode(doc, sc, GraphNode{
			ID:        id,
			Component: component,
			Label:     label,
			Props:     props,
			// Captured here or lost for good: Props is a map, so parsing an
			// existing config into a graph and rendering it back would otherwise
			// silently re-sequence the author's blocks into schema order. That
			// round trip runs on every "recreate as visual pipeline".
			BlockOrder: topLevelBlockOrder(block.Body),
			Disabled:   false,
		})
		sc.nodeByRef[name+"."+label] = id
		gp.blocks[id] = block
	}
}

// nodeID is a node's id: n_<component>_<label> at the top level (as it always
// was), with the module path after the n_ inside a module.
func (gp *graphParser) nodeID(doc *GraphDocument, sc *parseScope, component, label string) string {
	id := fmt.Sprintf("n_%s_%s", sanitizeLabel(component), sanitizeLabel(label))
	if sc.path != "" {
		id = "n_" + sc.path + "__" + strings.TrimPrefix(id, "n_")
	}
	if !gp.opts.ExpandModules {
		// The builder's parse, unchanged: one "_2" on a clash.
		for _, existing := range doc.Nodes {
			if existing.ID == id {
				id += "_2"
				break
			}
		}
		return id
	}
	// Module paths sanitize and join with "__", so different paths can meet
	// (declare "a" { declare "b" } and declare "a__b"): count up until free.
	base := id
	for n := 2; gp.nodeIDTaken(doc, id); n++ {
		id = fmt.Sprintf("%s_%d", base, n)
	}
	return id
}

func (gp *graphParser) nodeIDTaken(doc *GraphDocument, id string) bool {
	for _, existing := range doc.Nodes {
		if existing.ID == id {
			return true
		}
	}
	return false
}

// addNode appends a node at the next slot of the server's grid layout (which
// the graph view replaces with its own when it can) and records its scope.
func (gp *graphParser) addNode(doc *GraphDocument, sc *parseScope, n GraphNode) {
	n.Position = Position{X: gp.x, Y: gp.y}
	if group := sc.group(); group != "" {
		if gp.parents == nil {
			gp.parents = map[string]string{}
		}
		gp.parents[n.ID] = group
	}
	doc.Nodes = append(doc.Nodes, n)
	gp.nodeScope[n.ID] = sc
	gp.x += 280
	if gp.x > 1120 {
		gp.x = 0
		gp.y += 200
	}
}

// addModule maps a `declare` to a group node and its body to the group's
// children (ParseOptions.ExpandModules).
func (gp *graphParser) addModule(doc *GraphDocument, sc *parseScope, block *ast.BlockStmt) {
	mod := &module{name: block.Label, args: map[string]*modulePort{}, exports: map[string]*modulePort{}}
	mod.groupID = gp.nodeID(doc, sc, "declare", block.Label)
	gp.drop("module %q is shown as a group of its components (the builder cannot edit modules)", block.Label)
	gp.addNode(doc, sc, GraphNode{
		ID:        mod.groupID,
		Component: "declare",
		Label:     block.Label,
		// The module as written, for the inspector; neither renderer emits it.
		Props: map[string]interface{}{rawKey: gp.print(block)},
	})
	sc.modules[block.Label] = mod
	path := sanitizeLabel(block.Label)
	if sc.path != "" {
		path = sc.path + "__" + path
	}
	mod.body = &parseScope{
		parent: sc, module: mod, depth: sc.depth + 1, path: path,
		nodeByRef: map[string]string{}, modules: map[string]*module{},
	}
	gp.modules = append(gp.modules, mod)
	gp.addBody(doc, mod.body, block.Body)
}

// addPort records a module's `argument` or `export` block as a group port.
func (gp *graphParser) addPort(mod *module, kind string, block *ast.BlockStmt) {
	if kind == "argument" {
		if _, dup := mod.args[block.Label]; !dup {
			mod.args[block.Label] = &modulePort{}
			mod.argOrder = append(mod.argOrder, block.Label)
		}
		return
	}
	if _, dup := mod.exports[block.Label]; !dup {
		mod.exports[block.Label] = &modulePort{}
		mod.exportOrder = append(mod.exportOrder, block.Label)
		mod.exportStmts = append(mod.exportStmts, block)
	}
}

// Group port names: an argument and an export may share a name, and they are
// different ports (one carries data in, the other out), so each is named by
// its kind.
func argPort(name string) string    { return "argument." + name }
func exportPort(name string) string { return "export." + name }

// wireAll turns references into edges, removing the props they came from.
// Bodies are walked depth-first so a reference inside a block (an OTel
// `output` block, say) is addressed by its dotted path. Every module's port
// directions are settled first (resolvePorts): whether an instance's attribute
// is a port depends on how its module uses that argument, and whether a
// reference to an instance's export is one on what the export names — across
// modules, in any declaration order. Without modules this is one pass over the
// nodes in source order, as it always was.
func (gp *graphParser) wireAll(doc *GraphDocument) {
	for _, o := range gp.opaqueNodes {
		if m := o.scope.lookupModule(o.name); m != nil {
			gp.instanceOf[o.id] = m
		}
	}
	gp.resolvePorts(doc)
	for i := range doc.Nodes {
		// A group node has no block of its own: its body is its children.
		if block := gp.blocks[doc.Nodes[i].ID]; block != nil {
			gp.edges(doc, i, block.Body, nil, gp.nodeScope[doc.Nodes[i].ID], doc.Nodes[i].Props)
		}
	}
	for _, m := range gp.modules {
		gp.wireExports(doc, m)
	}
	for i := range doc.Nodes {
		if m := gp.instanceOf[doc.Nodes[i].ID]; m != nil {
			gp.wireInstance(doc, i, m)
		}
	}
}

// resolvePorts settles which module arguments and exports are wired, and
// which way data crosses each, before any edge is drawn. A port's direction
// can depend on another module's (an instance of b inside a, an export naming
// an instance's export), so it iterates until nothing changes; each pass only
// ever marks a port wired, so it terminates.
func (gp *graphParser) resolvePorts(doc *GraphDocument) {
	if len(gp.modules) == 0 {
		return
	}
	for changed := true; changed; {
		changed = false
		for i := range doc.Nodes {
			sc, block := gp.nodeScope[doc.Nodes[i].ID], gp.blocks[doc.Nodes[i].ID]
			if block == nil || sc.module == nil {
				continue
			}
			walkWireAttrs(block.Body, nil, func(prop string, value ast.Expr) {
				refs, isWire := wireRefs(value)
				if !isWire {
					return
				}
				ends, ok := gp.wireEnds(doc, i, prop, refs, sc)
				if !ok {
					return
				}
				for _, e := range ends {
					if e.arg != nil && !e.arg.wired {
						e.arg.wired, e.arg.outward = true, gp.inputProduces(doc, i, prop)
						changed = true
					}
				}
			})
		}
		for _, m := range gp.modules {
			for _, block := range m.exportStmts {
				port := m.exports[block.Label]
				if port.wired {
					continue
				}
				if value := exportValue(block); value != nil {
					if _, outward, ok := gp.exportEnds(doc, m, value); ok {
						port.wired, port.outward = true, outward
						changed = true
					}
				}
			}
		}
	}
}

// walkWireAttrs calls fn for every attribute a wire can come from, at its
// dotted path: the body's own and those of nested unlabeled blocks that occur
// once (a repeated block is a list in props, which no port path addresses) —
// the same attributes edges() visits.
func walkWireAttrs(body ast.Body, path []string, fn func(prop string, value ast.Expr)) {
	counts := map[string]int{}
	for _, stmt := range body {
		if b, ok := stmt.(*ast.BlockStmt); ok {
			counts[strings.Join(b.Name, ".")]++
		}
	}
	for _, stmt := range body {
		switch s := stmt.(type) {
		case *ast.BlockStmt:
			name := strings.Join(s.Name, ".")
			if s.Label == "" && counts[name] == 1 {
				walkWireAttrs(s.Body, append(append([]string{}, path...), name), fn)
			}
		case *ast.AttributeStmt:
			fn(strings.Join(append(append([]string{}, path...), s.Name.Name), "."), s.Value)
		}
	}
}

// exportValue is an export block's `value` expression.
func exportValue(block *ast.BlockStmt) ast.Expr {
	for _, stmt := range block.Body {
		if attr, ok := stmt.(*ast.AttributeStmt); ok && attr.Name.Name == "value" {
			return attr.Value
		}
	}
	return nil
}

// exportEnd is one thing an export's value names: a component's (or inner
// instance's) port, or — passed straight through — one of the module's own
// arguments.
type exportEnd struct {
	node, export string
	arg          string
}

// exportEnds resolves an export's value when it is pure wiring: every
// reference a port in the module (or a wired argument of it). outward says
// which way data crosses the module boundary through the export, decided by
// the first reference: out for a data export, in for an exported receiver; an
// argument passed through flips its argument's direction (receivers handed in
// and exported again take data in through the export).
func (gp *graphParser) exportEnds(doc *GraphDocument, m *module, value ast.Expr) ([]exportEnd, bool, bool) {
	refs, isWire := wireRefs(value)
	if !isWire || len(refs) == 0 {
		return nil, false, false
	}
	ends := make([]exportEnd, 0, len(refs))
	outward := false
	for i, ref := range refs {
		var out bool
		if arg, name, isArg := argumentRef(m.body, ref); isArg {
			if !arg.wired {
				return nil, false, false
			}
			ends = append(ends, exportEnd{arg: name})
			out = !arg.outward
		} else {
			otherID, export, resolved := resolveRef(ref, m.body.nodeByRef)
			if !resolved || !gp.isPort(doc, otherID, export) {
				return nil, false, false
			}
			ends = append(ends, exportEnd{node: otherID, export: export})
			out = !gp.isReceiverExport(doc, otherID, export)
		}
		if i == 0 {
			outward = out
		}
	}
	return ends, outward, true
}

// wireExports turns each export whose value is pure wiring into edges between
// the group's export port and what it names (another of the group's ports, for
// an argument passed through); any other reference in an export's value is a
// binding on the group.
func (gp *graphParser) wireExports(doc *GraphDocument, m *module) {
	for _, block := range m.exportStmts {
		value := exportValue(block)
		if value == nil {
			continue
		}
		port := m.exports[block.Label]
		ends, _, ok := gp.exportEnds(doc, m, value)
		if !ok || !port.wired {
			for _, ref := range exprRefs(value) {
				if _, name, isArg := argumentRef(m.body, ref); isArg {
					gp.binding(m.groupID, exportPort(block.Label), m.groupID, argPort(name), value)
				} else if otherID, export, resolved := resolveRef(ref, m.body.nodeByRef); resolved {
					gp.binding(m.groupID, exportPort(block.Label), otherID, export, value)
				}
			}
			continue
		}
		out := PortRef{Node: m.groupID, Port: exportPort(block.Label)}
		for _, e := range ends {
			inner := PortRef{Node: e.node, Port: e.export}
			if e.arg != "" {
				inner = PortRef{Node: m.groupID, Port: argPort(e.arg)}
			}
			if port.outward {
				gp.addEdge(doc, inner, out)
			} else {
				gp.addEdge(doc, out, inner)
			}
		}
	}
}

// wireInstance links a module instance to its module's group: one edge per
// wired argument the instance sets and per wired export, in the direction
// data crosses the module boundary.
func (gp *graphParser) wireInstance(doc *GraphDocument, idx int, m *module) {
	inst := doc.Nodes[idx].ID
	set := map[string]bool{}
	for _, stmt := range gp.blocks[inst].Body {
		if attr, ok := stmt.(*ast.AttributeStmt); ok {
			set[attr.Name.Name] = true
		}
	}
	link := func(groupPort, name string, outward bool) {
		group, instance := PortRef{Node: m.groupID, Port: groupPort}, PortRef{Node: inst, Port: name}
		if outward {
			gp.addEdge(doc, group, instance)
		} else {
			gp.addEdge(doc, instance, group)
		}
	}
	for _, name := range m.argOrder {
		if p := m.args[name]; p.wired && set[name] {
			link(argPort(name), name, p.outward)
		}
	}
	for _, name := range m.exportOrder {
		if p := m.exports[name]; p.wired {
			link(exportPort(name), name, p.outward)
		}
	}
}

// addEdge appends an edge in dataflow direction, from → to. Its id names both
// ends' ports, made unique if two references draw the same edge.
func (gp *graphParser) addEdge(doc *GraphDocument, from, to PortRef) {
	base := fmt.Sprintf("e_%s_%s_%s_%s", from.Node, from.Port, to.Node, to.Port)
	id := base
	for n := 2; gp.edgeIDTaken(doc, id); n++ {
		id = fmt.Sprintf("%s_%d", base, n)
	}
	doc.Edges = append(doc.Edges, GraphEdge{ID: id, From: from, To: to})
}

func (gp *graphParser) edgeIDTaken(doc *GraphDocument, id string) bool {
	for _, e := range doc.Edges {
		if e.ID == id {
			return true
		}
	}
	return false
}

func (gp *graphParser) drop(format string, args ...any) {
	gp.opaque = true
	msg := fmt.Sprintf(format, args...)
	for _, existing := range gp.dropped {
		if existing == msg {
			return
		}
	}
	gp.dropped = append(gp.dropped, msg)
}

func (gp *graphParser) warning() string {
	if len(gp.dropped) == 0 {
		return ""
	}
	return "this pipeline could not be fully mapped to a graph: " + strings.Join(gp.dropped, "; ")
}

func (gp *graphParser) isComponent(name string) bool {
	if gp.hasSchema {
		_, ok := gp.schema.Components[name]
		return ok
	}
	return !configBlocks[name]
}

// topLevelBlockOrder lists a body's block names in first-appearance order, once
// each. Repeats of the same name keep their relative order inside the props
// list already, so only the sequence *between* differently-named blocks needs
// recording — that is the part a map cannot hold.
func topLevelBlockOrder(body ast.Body) []string {
	var order []string
	seen := map[string]bool{}
	for _, stmt := range body {
		block, ok := stmt.(*ast.BlockStmt)
		if !ok {
			continue
		}
		name := strings.Join(block.Name, ".")
		if seen[name] {
			continue
		}
		seen[name] = true
		order = append(order, name)
	}
	return order
}

// props converts a block body into a props map: attributes by value, nested
// blocks as nested objects (or arrays when the block repeats).
func (gp *graphParser) props(body ast.Body, owner string) map[string]interface{} {
	out := map[string]interface{}{}
	counts := map[string]int{}
	for _, stmt := range body {
		if block, ok := stmt.(*ast.BlockStmt); ok {
			counts[strings.Join(block.Name, ".")]++
		}
	}
	for _, stmt := range body {
		switch s := stmt.(type) {
		case *ast.AttributeStmt:
			out[s.Name.Name] = gp.value(s.Value)
		case *ast.BlockStmt:
			name := strings.Join(s.Name, ".")
			if s.Label != "" {
				gp.drop("block %q in %s has a label, which the graph model cannot represent", name, owner)
				continue
			}
			nested := gp.props(s.Body, owner+"."+name)
			if counts[name] > 1 {
				list, isList := out[name].([]interface{})
				if !isList {
					list = nil
				}
				out[name] = append(list, nested)
				continue
			}
			out[name] = nested
		}
	}
	return out
}

// value converts an expression into a props value. Literals become Go values;
// everything else is preserved verbatim as {"$expr": "..."} so a round trip
// through the renderer reproduces it.
func (gp *graphParser) value(expr ast.Expr) interface{} {
	switch e := expr.(type) {
	case *ast.LiteralExpr:
		switch e.Kind {
		case token.STRING:
			if s, err := strconv.Unquote(e.Value); err == nil {
				return s
			}
			return e.Value
		case token.NUMBER, token.FLOAT:
			if f, err := strconv.ParseFloat(e.Value, 64); err == nil {
				return f
			}
		case token.BOOL:
			return e.Value == "true"
		case token.NULL:
			return nil
		case token.ILLEGAL, token.LITERAL, token.EOF, token.COMMENT, token.IDENT,
			token.OR, token.AND, token.NOT, token.ASSIGN, token.EQ, token.NEQ, token.LT, token.LTE, token.GT, token.GTE,
			token.ADD, token.SUB, token.MUL, token.DIV, token.MOD, token.POW,
			token.LCURLY, token.RCURLY, token.LPAREN, token.RPAREN, token.LBRACK, token.RBRACK, token.COMMA, token.DOT,
			token.TERMINATOR:
			// Not a literal shape we can model; fall through to the
			// expression form below.
		}
	case *ast.ArrayExpr:
		out := make([]interface{}, 0, len(e.Elements))
		for _, el := range e.Elements {
			out = append(out, gp.value(el))
		}
		return out
	case *ast.ObjectExpr:
		out := map[string]interface{}{}
		for _, field := range e.Fields {
			out[field.Name.Name] = gp.value(field.Value)
		}
		return out
	}
	return map[string]interface{}{exprKey: gp.print(expr)}
}

// print renders an expression back to source text.
func (gp *graphParser) print(node ast.Node) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, node); err != nil {
		return ""
	}
	return strings.TrimSpace(buf.String())
}

// edges walks one node's body and converts every reference to another node
// into an edge (deleting the prop it came from when the prop was nothing but
// wiring) or a binding. path is the dotted location of the current body inside
// the component; sc is the body the node sits in, which references resolve in.
func (gp *graphParser) edges(doc *GraphDocument, idx int, body ast.Body, path []string, sc *parseScope, props map[string]interface{}) {
	for _, stmt := range body {
		switch s := stmt.(type) {
		case *ast.BlockStmt:
			name := strings.Join(s.Name, ".")
			if s.Label != "" {
				continue
			}
			child, ok := props[name].(map[string]interface{})
			if !ok {
				// A repeated block is an array; references inside one cannot
				// be addressed by a port path, so they stay as props.
				continue
			}
			gp.edges(doc, idx, s.Body, append(append([]string{}, path...), name), sc, child)
		case *ast.AttributeStmt:
			prop := strings.Join(append(append([]string{}, path...), s.Name.Name), ".")
			if refs, isWire := wireRefs(s.Value); isWire && gp.wire(doc, idx, prop, refs, sc) {
				// The renderer writes the attribute back from the edges.
				delete(props, s.Name.Name)
				continue
			}
			gp.references(doc, idx, prop, s, sc)
		}
	}
}

// wire turns an attribute that is nothing but wiring — a reference, a list of
// them, or the array.concat the renderer emits for a targets fan-in — into
// edges, and reports whether it did. It is all or nothing: only when every
// reference resolves to a port of a node in the graph. Anything less (a list
// mixing a receiver with a non-port export, a reference to an `argument`
// outside an expanded module) stays an expression, so the prop is never both
// a wire and a value.
func (gp *graphParser) wire(doc *GraphDocument, idx int, prop string, refs []string, sc *parseScope) bool {
	ends, ok := gp.wireEnds(doc, idx, prop, refs, sc)
	if !ok {
		return false
	}
	for _, e := range ends {
		if e.arg == nil {
			gp.edge(doc, idx, prop, e.node, e.export)
			continue
		}
		// An argument's direction is the consumer's: a forward_to (or an
		// output block's list) hands data out through it; anything else takes
		// data in. resolvePorts has settled it; the first use decides.
		if !e.arg.wired {
			e.arg.wired, e.arg.outward = true, gp.inputProduces(doc, idx, prop)
		}
		consumer, port := PortRef{Node: doc.Nodes[idx].ID, Port: prop}, PortRef{Node: e.node, Port: e.export}
		if e.arg.outward {
			gp.addEdge(doc, consumer, port)
		} else {
			gp.addEdge(doc, port, consumer)
		}
	}
	return true
}

// wireEnd is one resolved reference of a wiring attribute: a node's port, or
// (arg set) the enclosing module's argument port on its group.
type wireEnd struct {
	node, export string
	arg          *modulePort
}

// wireEnds resolves every reference of a wiring attribute, or reports that
// one does not resolve to a port (all or nothing, see wire).
func (gp *graphParser) wireEnds(doc *GraphDocument, idx int, prop string, refs []string, sc *parseScope) ([]wireEnd, bool) {
	if len(refs) == 0 {
		return nil, false
	}
	ends := make([]wireEnd, 0, len(refs))
	for _, ref := range refs {
		if arg, name, isArg := argumentRef(sc, ref); isArg {
			if !gp.isInputPort(doc, idx, prop) {
				return nil, false
			}
			ends = append(ends, wireEnd{sc.module.groupID, argPort(name), arg})
			continue
		}
		otherID, export, ok := resolveRef(ref, sc.nodeByRef)
		if !ok || !gp.isPort(doc, otherID, export) {
			return nil, false
		}
		ends = append(ends, wireEnd{otherID, export, nil})
	}
	return ends, true
}

// argumentRef resolves argument.<name>.value inside an expanded module to the
// module's argument port.
func argumentRef(sc *parseScope, ref string) (*modulePort, string, bool) {
	if sc.module == nil {
		return nil, "", false
	}
	parts := strings.Split(ref, ".")
	if len(parts) != 3 || parts[0] != "argument" || parts[2] != "value" {
		return nil, "", false
	}
	p, ok := sc.module.args[parts[1]]
	return p, parts[1], ok
}

// isInputPort reports whether prop is a wireable argument of the node: an
// input port in its schema, or a wired argument of the module it instantiates.
// Without a schema nothing says so, and the reference stays a binding.
func (gp *graphParser) isInputPort(doc *GraphDocument, idx int, prop string) bool {
	node := doc.Nodes[idx]
	if m := gp.instanceOf[node.ID]; m != nil {
		p, ok := m.args[prop]
		return ok && p.wired
	}
	return gp.hasSchema && findInputPath(gp.schema.Components[node.Component], prop) != nil
}

// inputProduces reports whether data leaves the node through argument prop.
func (gp *graphParser) inputProduces(doc *GraphDocument, idx int, prop string) bool {
	node := doc.Nodes[idx]
	if m := gp.instanceOf[node.ID]; m != nil {
		p, ok := m.args[prop]
		return ok && p.outward
	}
	in := findInputPath(gp.schema.Components[node.Component], prop)
	return in != nil && inputRole(*in) == roleProduces
}

// findInputPath finds the input port at a dotted prop path (output.metrics).
func findInputPath(c ComponentSchema, prop string) *PortSchema {
	for i := range c.Inputs {
		if strings.Join(portPath(c.Inputs[i]), ".") == prop {
			return &c.Inputs[i]
		}
	}
	return nil
}

// references handles an attribute that is not pure wiring but contains
// references — convert.nonsensitive(secret.data["k"]), a concat of a wire and
// a literal list, a list mixing a receiver with a file's content. The prop
// keeps the expression verbatim ({"$expr": ...}, set by props()), so rendering
// it back is exact, and it is neither a wire nor a reason to call the graph
// opaque: an edge here would make the renderer write the attribute twice (or
// drop the expression for the wire). Each reference to another node is
// reported as a binding instead, which the read-only view lists and nothing
// renders. Inside an expanded module a reference to one of its arguments is a
// binding to the module's group.
func (gp *graphParser) references(doc *GraphDocument, idx int, prop string, s *ast.AttributeStmt, sc *parseScope) {
	node := doc.Nodes[idx]
	for _, ref := range exprRefs(s.Value) {
		if _, name, isArg := argumentRef(sc, ref); isArg {
			gp.binding(node.ID, prop, sc.module.groupID, argPort(name), s.Value)
			continue
		}
		if otherID, export, ok := resolveRef(ref, sc.nodeByRef); ok {
			gp.binding(node.ID, prop, otherID, export, s.Value)
		}
	}
}

// binding records a reference kept inside an expression (see
// ParseResult.Bindings). Expr is the whole attribute value: that is what the
// prop holds.
func (gp *graphParser) binding(nodeID, prop, otherID, export string, value ast.Expr) {
	gp.bindings = append(gp.bindings, GraphBinding{
		Node: nodeID,
		Prop: prop,
		Ref:  BindingRef{Node: otherID, Export: export, Expr: gp.print(value)},
	})
}

// resolveRef splits a "component.label.export" reference into the node it
// names and the export, trying progressively shorter prefixes so a dotted
// export (output.metrics) and a dotted component name both resolve.
func resolveRef(ref string, nodeByRef map[string]string) (nodeID, export string, ok bool) {
	parts := strings.Split(ref, ".")
	if len(parts) < 3 {
		return "", "", false
	}
	for split := len(parts) - 1; split >= 2; split-- {
		if id, exists := nodeByRef[strings.Join(parts[:split], ".")]; exists {
			return id, strings.Join(parts[split:], "."), true
		}
	}
	return "", "", false
}

// isPort reports whether export is a wireable port of the node. Without a
// schema every reference is treated as one (the legacy behaviour); with one, a
// mapped component's port list decides, a module instance's ports are its
// module's wired exports, and any other opaque node — whose exports no schema
// describes — has none.
func (gp *graphParser) isPort(doc *GraphDocument, nodeID, export string) bool {
	if !gp.hasSchema {
		return true
	}
	if m := gp.instanceOf[nodeID]; m != nil {
		p, ok := m.exports[export]
		return ok && p.wired
	}
	for _, n := range doc.Nodes {
		if n.ID != nodeID {
			continue
		}
		c, known := gp.schema.Components[n.Component]
		return known && findOutput(c, export) != nil
	}
	return false
}

// edge adds the edge for one resolved reference, in dataflow direction.
func (gp *graphParser) edge(doc *GraphDocument, idx int, prop, otherID, export string) {
	node := doc.Nodes[idx]
	edge := GraphEdge{
		ID:   fmt.Sprintf("e_%s_%s_%s", otherID, node.ID, prop),
		From: PortRef{Node: otherID, Port: export},
		To:   PortRef{Node: node.ID, Port: prop},
	}
	if gp.isReceiverExport(doc, otherID, export) {
		// A receiver export means data flows this node → the referenced
		// one, so the canvas edge points the other way (D1(b)).
		edge.ID = fmt.Sprintf("e_%s_%s_%s", node.ID, otherID, prop)
		edge.From = PortRef{Node: node.ID, Port: prop}
		edge.To = PortRef{Node: otherID, Port: export}
	}
	doc.Edges = append(doc.Edges, edge)
}

func (gp *graphParser) isReceiverExport(doc *GraphDocument, nodeID, export string) bool {
	if !gp.hasSchema {
		return false
	}
	if m := gp.instanceOf[nodeID]; m != nil {
		p, ok := m.exports[export]
		return ok && p.wired && !p.outward
	}
	for _, n := range doc.Nodes {
		if n.ID != nodeID {
			continue
		}
		if out := findOutput(gp.schema.Components[n.Component], export); out != nil {
			return outputRole(*out) == roleAccepts
		}
	}
	return false
}

// wireRefs reports whether an expression is pure wiring — a reference, a list
// of references (nested lists included), or array.concat over those, which is
// what the renderer writes for a targets fan-in — and returns its references.
func wireRefs(expr ast.Expr) ([]string, bool) {
	switch e := expr.(type) {
	case *ast.AccessExpr:
		return []string{accessPath(e)}, true
	case *ast.ArrayExpr:
		return wireList(e.Elements)
	case *ast.CallExpr:
		if fn, isAccess := e.Value.(*ast.AccessExpr); isAccess && accessPath(fn) == "array.concat" {
			return wireList(e.Args)
		}
	}
	return nil, false
}

func wireList(elems []ast.Expr) ([]string, bool) {
	refs := []string{}
	for _, el := range elems {
		inner, ok := wireRefs(el)
		if !ok {
			return nil, false
		}
		refs = append(refs, inner...)
	}
	return refs, true
}

// exprRefs returns every dotted reference anywhere inside an expression: in
// call arguments, indexed values and indices, operands, object fields and
// list elements. A called function's own name (convert.nonsensitive) is not a
// reference and is skipped.
func exprRefs(expr ast.Expr) []string {
	var refs []string
	switch e := expr.(type) {
	case *ast.AccessExpr:
		root := ast.Expr(e)
		for {
			inner, isAccess := root.(*ast.AccessExpr)
			if !isAccess {
				break
			}
			root = inner.Value
		}
		if _, isIdent := root.(*ast.IdentifierExpr); isIdent {
			refs = append(refs, accessPath(e))
		} else {
			// foo(x).y: the field access is on a computed value, so only
			// the computation can hold a reference.
			refs = append(refs, exprRefs(root)...)
		}
	case *ast.ArrayExpr:
		for _, el := range e.Elements {
			refs = append(refs, exprRefs(el)...)
		}
	case *ast.ObjectExpr:
		for _, field := range e.Fields {
			refs = append(refs, exprRefs(field.Value)...)
		}
	case *ast.CallExpr:
		for _, arg := range e.Args {
			refs = append(refs, exprRefs(arg)...)
		}
	case *ast.IndexExpr:
		refs = append(refs, exprRefs(e.Value)...)
		refs = append(refs, exprRefs(e.Index)...)
	case *ast.BinaryExpr:
		refs = append(refs, exprRefs(e.Left)...)
		refs = append(refs, exprRefs(e.Right)...)
	case *ast.UnaryExpr:
		refs = append(refs, exprRefs(e.Value)...)
	case *ast.ParenExpr:
		refs = append(refs, exprRefs(e.Inner)...)
	case *ast.IdentifierExpr, *ast.LiteralExpr:
		// a bare identifier or a literal is not a cross-component reference
	}
	return refs
}

// accessPath reconstructs the dotted path from an AccessExpr chain.
func accessPath(e *ast.AccessExpr) string {
	parts := []string{e.Name.Name}
	cur := e.Value
	for {
		switch inner := cur.(type) {
		case *ast.AccessExpr:
			parts = append([]string{inner.Name.Name}, parts...)
			cur = inner.Value
		case *ast.IdentifierExpr:
			parts = append([]string{inner.Ident.Name}, parts...)
			return strings.Join(parts, ".")
		default:
			return strings.Join(parts, ".")
		}
	}
}
