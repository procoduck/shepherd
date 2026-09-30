package visual

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Change kinds for a GraphDiff entry. Strings rather than an enum, matching
// this package's other enum-like fields (UpgradeItem.Class) and the proto
// contract's enum-like-field rule.
const (
	ChangeAdded   = "added"
	ChangeRemoved = "removed"
	ChangeChanged = "changed"
)

// FieldChange is one modified attribute on a changed node — a top-level field
// ("label", "component", "disabled", "notes", "block_order") or a component
// property keyed "prop:<name>". OldValue/NewValue are human-readable
// stringifications; an added attribute has an empty OldValue, a removed one an
// empty NewValue.
type FieldChange struct {
	Field    string `json:"field"`
	OldValue string `json:"old_value"`
	NewValue string `json:"new_value"`
}

// NodeChange is one node added, removed, or changed between two graphs. Kind is
// ChangeAdded/ChangeRemoved/ChangeChanged. FieldChanges is populated only for
// ChangeChanged. ID is the target-graph node id for added/changed, the
// base-graph id for removed — informational only, since the two graphs'
// ids need not agree (#203).
type NodeChange struct {
	Kind         string        `json:"kind"`
	ID           string        `json:"id"`
	Component    string        `json:"component"`
	Label        string        `json:"label"`
	FieldChanges []FieldChange `json:"field_changes,omitempty"`
}

// EdgeChange is one wire added, removed, or changed (order). From/To are the
// target-graph endpoints for added/changed, the base-graph endpoints for
// removed. Each endpoint's Node is the block's display name (`component
// "label"`, see nodeDisplayName) rather than its graph-internal id, which means
// nothing to a reader and differs between a saved graph and a re-parse (#203);
// an endpoint naming a node the graph doesn't hold keeps the raw id.
type EdgeChange struct {
	Kind string  `json:"kind"`
	ID   string  `json:"id"`
	From PortRef `json:"from"`
	To   PortRef `json:"to"`
}

// BindingChange is one property binding added, removed, or changed. OldRef is
// set for removed/changed, NewRef for added/changed. Node and each ref's Node
// are display names, like EdgeChange's endpoints.
type BindingChange struct {
	Kind   string     `json:"kind"`
	Node   string     `json:"node"`
	Prop   string     `json:"prop"`
	OldRef BindingRef `json:"old_ref"`
	NewRef BindingRef `json:"new_ref"`
}

// GraphDiff is the structural difference between two graph documents.
type GraphDiff struct {
	NodeChanges    []NodeChange    `json:"node_changes"`
	EdgeChanges    []EdgeChange    `json:"edge_changes"`
	BindingChanges []BindingChange `json:"binding_changes"`
}

// DiffGraphs computes the structural difference from `from` to `to`: which
// nodes, edges and bindings were added, removed, or changed. It is a pure
// function of the two documents — position and viewport (pure layout) are
// deliberately ignored, since moving a node on the canvas is not a change to
// what the pipeline does.
//
// Node ids are not a stable identity: a revision's saved graph and a re-parse
// of the same Alloy assign different ids to the same blocks (#203). Nodes are
// therefore matched by what Alloy itself identifies a block by — component +
// label — and only where that is ambiguous (the pair isn't unique on both
// sides) or unmatched (a relabelled block) by id. Wires and bindings are then
// compared through that node matching, so an unchanged graph under renamed ids
// diffs as empty.
//
// Output is deterministically ordered (nodes by component, label, kind, id;
// edges by endpoints; bindings by node then prop; field changes by field) so a
// diff of the same two graphs is byte-stable.
func DiffGraphs(from, to GraphDocument) GraphDiff {
	m := matchNodes(from.Nodes, to.Nodes)
	diff := GraphDiff{
		NodeChanges:    diffNodes(from.Nodes, to.Nodes, m),
		EdgeChanges:    diffEdges(from.Edges, to.Edges, m),
		BindingChanges: diffBindings(from.Bindings, to.Bindings, m),
	}
	return diff
}

// nodeMatch pairs base-graph nodes with target-graph nodes and gives every node
// id a side-independent canonical key: a matched pair shares one key, so edges
// and bindings compare equal across the two graphs whatever their ids.
type nodeMatch struct {
	pairs    [][2]int // indices into (from, to)
	fromKey  map[string]string
	toKey    map[string]string
	fromName map[string]string
	toName   map[string]string
}

// canon returns the canonical key of a node id on one side. An id the graph
// doesn't hold (a dangling reference) keys by the raw id, so the same dangling
// id on both sides still compares equal.
func canon(keys map[string]string, id string) string {
	if k, ok := keys[id]; ok {
		return k
	}
	return "id:" + id
}

// display returns a node id's display name on one side, the raw id for a node
// the graph doesn't hold.
func display(names map[string]string, id string) string {
	if n, ok := names[id]; ok {
		return n
	}
	return id
}

// nodeDisplayName is how a reader names a block: its component and quoted
// label, the way the block header reads in Alloy (`prometheus.scrape "web"`).
func nodeDisplayName(n GraphNode) string {
	if n.Label == "" {
		return n.Component
	}
	return n.Component + ` "` + n.Label + `"`
}

func matchNodes(from, to []GraphNode) nodeMatch {
	identity := func(n GraphNode) string { return n.Component + "\x00" + n.Label }
	fromCount := make(map[string]int, len(from))
	for _, n := range from {
		fromCount[identity(n)]++
	}
	toByIdentity := make(map[string][]int, len(to))
	for j, n := range to {
		toByIdentity[identity(n)] = append(toByIdentity[identity(n)], j)
	}

	m := nodeMatch{
		fromKey: make(map[string]string, len(from)), toKey: make(map[string]string, len(to)),
		fromName: make(map[string]string, len(from)), toName: make(map[string]string, len(to)),
	}
	fromMatched := make([]bool, len(from))
	toMatched := make([]bool, len(to))

	// Pass 1: component + label, where it names exactly one block on each side.
	for i, n := range from {
		k := identity(n)
		if fromCount[k] != 1 || len(toByIdentity[k]) != 1 {
			continue
		}
		j := toByIdentity[k][0]
		m.pairs = append(m.pairs, [2]int{i, j})
		fromMatched[i], toMatched[j] = true, true
	}

	// Pass 2: the id, for what identity couldn't pair — an ambiguous component +
	// label, or a block relabelled in place.
	toIdx := make(map[string]int, len(to))
	for j, n := range to {
		if !toMatched[j] {
			if _, dup := toIdx[n.ID]; !dup {
				toIdx[n.ID] = j
			}
		}
	}
	for i, n := range from {
		if fromMatched[i] {
			continue
		}
		if j, ok := toIdx[n.ID]; ok && !toMatched[j] {
			m.pairs = append(m.pairs, [2]int{i, j})
			fromMatched[i], toMatched[j] = true, true
		}
	}

	for p, pr := range m.pairs {
		k := "pair:" + strconv.Itoa(p)
		m.fromKey[from[pr[0]].ID] = k
		m.toKey[to[pr[1]].ID] = k
	}
	for i, n := range from {
		if !fromMatched[i] {
			m.fromKey[n.ID] = "from:" + n.ID
		}
		m.fromName[n.ID] = nodeDisplayName(n)
	}
	for j, n := range to {
		if !toMatched[j] {
			m.toKey[n.ID] = "to:" + n.ID
		}
		m.toName[n.ID] = nodeDisplayName(n)
	}
	return m
}

func diffNodes(from, to []GraphNode, m nodeMatch) []NodeChange {
	fromMatched := make([]bool, len(from))
	toMatched := make([]bool, len(to))

	var changes []NodeChange
	for _, pr := range m.pairs {
		old, n := from[pr[0]], to[pr[1]]
		fromMatched[pr[0]], toMatched[pr[1]] = true, true
		if fcs := nodeFieldChanges(old, n); len(fcs) > 0 {
			changes = append(changes, NodeChange{
				Kind: ChangeChanged, ID: n.ID, Component: n.Component, Label: n.Label,
				FieldChanges: fcs,
			})
		}
	}
	for j, n := range to {
		if !toMatched[j] {
			changes = append(changes, NodeChange{
				Kind: ChangeAdded, ID: n.ID, Component: n.Component, Label: n.Label,
			})
		}
	}
	for i, n := range from {
		if !fromMatched[i] {
			changes = append(changes, NodeChange{
				Kind: ChangeRemoved, ID: n.ID, Component: n.Component, Label: n.Label,
			})
		}
	}

	sort.SliceStable(changes, func(i, j int) bool {
		a, b := changes[i], changes[j]
		if a.Component != b.Component {
			return a.Component < b.Component
		}
		if a.Label != b.Label {
			return a.Label < b.Label
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.ID < b.ID
	})
	return changes
}

// nodeFieldChanges lists the attributes that differ between two matched nodes.
// Position is intentionally excluded (pure layout). Props are compared key by
// key so a diff names the exact attribute that changed.
func nodeFieldChanges(old, cur GraphNode) []FieldChange {
	var fcs []FieldChange
	if old.Component != cur.Component {
		fcs = append(fcs, FieldChange{Field: "component", OldValue: old.Component, NewValue: cur.Component})
	}
	if old.Label != cur.Label {
		fcs = append(fcs, FieldChange{Field: "label", OldValue: old.Label, NewValue: cur.Label})
	}
	if old.Disabled != cur.Disabled {
		fcs = append(fcs, FieldChange{Field: "disabled", OldValue: boolStr(old.Disabled), NewValue: boolStr(cur.Disabled)})
	}
	if old.Notes != cur.Notes {
		fcs = append(fcs, FieldChange{Field: "notes", OldValue: old.Notes, NewValue: cur.Notes})
	}
	// An empty block_order means "schema order" (orderBlocks), not a stated
	// order, and a re-parse records one where the builder may not — so only two
	// stated orders are compared.
	if oldBO, curBO := blockOrderStr(old.BlockOrder), blockOrderStr(cur.BlockOrder); oldBO != "" && curBO != "" && oldBO != curBO {
		fcs = append(fcs, FieldChange{Field: "block_order", OldValue: oldBO, NewValue: curBO})
	}
	fcs = append(fcs, propChanges(old.Props, cur.Props)...)

	sort.SliceStable(fcs, func(i, j int) bool { return fcs[i].Field < fcs[j].Field })
	return fcs
}

// propChanges diffs two prop maps key by key. Each changed/added/removed key
// becomes a FieldChange keyed "prop:<name>", its values canonically stringified
// so a reordered nested object doesn't read as a change.
func propChanges(old, cur map[string]any) []FieldChange {
	keys := make(map[string]struct{}, len(old)+len(cur))
	for k := range old {
		keys[k] = struct{}{}
	}
	for k := range cur {
		keys[k] = struct{}{}
	}
	var fcs []FieldChange
	for k := range keys {
		ov, ook := old[k]
		nv, nok := cur[k]
		os, ns := scalarStr(ov), scalarStr(nv)
		switch {
		case ook && nok && scalarStr(blockShape(ov)) != scalarStr(blockShape(nv)):
			fcs = append(fcs, FieldChange{Field: "prop:" + k, OldValue: os, NewValue: ns})
		case ook && !nok:
			fcs = append(fcs, FieldChange{Field: "prop:" + k, OldValue: os, NewValue: ""})
		case !ook && nok:
			fcs = append(fcs, FieldChange{Field: "prop:" + k, OldValue: "", NewValue: ns})
		}
	}
	return fcs
}

// diffEdges matches wires by their canonical endpoints (both nodes through the
// node matching, plus both ports) — a wire has no identity beyond what it
// connects. Two wires with identical endpoints on one side pair in document
// order. A wire left over on both sides with the same id is a re-pointed wire
// (changed); the rest are added/removed.
func diffEdges(from, to []GraphEdge, m nodeMatch) []EdgeChange {
	endpoints := func(keys map[string]string, e GraphEdge) string {
		return canon(keys, e.From.Node) + "\x00" + e.From.Port + "\x00" + canon(keys, e.To.Node) + "\x00" + e.To.Port
	}
	named := func(names map[string]string, e GraphEdge) (PortRef, PortRef) {
		return PortRef{Node: display(names, e.From.Node), Port: e.From.Port},
			PortRef{Node: display(names, e.To.Node), Port: e.To.Port}
	}

	fromByEndpoints := make(map[string][]int, len(from))
	for i, e := range from {
		k := endpoints(m.fromKey, e)
		fromByEndpoints[k] = append(fromByEndpoints[k], i)
	}
	fromMatched := make([]bool, len(from))
	toMatched := make([]bool, len(to))

	var changes []EdgeChange
	for j, e := range to {
		k := endpoints(m.toKey, e)
		cands := fromByEndpoints[k]
		if len(cands) == 0 {
			continue
		}
		i := cands[0]
		fromByEndpoints[k] = cands[1:]
		fromMatched[i], toMatched[j] = true, true
		if intPtrStr(from[i].Order) != intPtrStr(e.Order) {
			f, t := named(m.toName, e)
			changes = append(changes, EdgeChange{Kind: ChangeChanged, ID: e.ID, From: f, To: t})
		}
	}

	// Leftovers sharing an id: the same wire re-pointed.
	fromLeftByID := make(map[string]int)
	for i, e := range from {
		if !fromMatched[i] {
			if _, dup := fromLeftByID[e.ID]; !dup {
				fromLeftByID[e.ID] = i
			}
		}
	}
	for j, e := range to {
		if toMatched[j] {
			continue
		}
		if i, ok := fromLeftByID[e.ID]; ok && !fromMatched[i] {
			fromMatched[i], toMatched[j] = true, true
			f, t := named(m.toName, e)
			changes = append(changes, EdgeChange{Kind: ChangeChanged, ID: e.ID, From: f, To: t})
			continue
		}
		toMatched[j] = true
		f, t := named(m.toName, e)
		changes = append(changes, EdgeChange{Kind: ChangeAdded, ID: e.ID, From: f, To: t})
	}
	for i, e := range from {
		if !fromMatched[i] {
			f, t := named(m.fromName, e)
			changes = append(changes, EdgeChange{Kind: ChangeRemoved, ID: e.ID, From: f, To: t})
		}
	}

	sort.SliceStable(changes, func(i, j int) bool {
		a, b := changes[i], changes[j]
		ak := [...]string{a.From.Node, a.From.Port, a.To.Node, a.To.Port, a.Kind, a.ID}
		bk := [...]string{b.From.Node, b.From.Port, b.To.Node, b.To.Port, b.Kind, b.ID}
		for x := range ak {
			if ak[x] != bk[x] {
				return ak[x] < bk[x]
			}
		}
		return false
	})
	return changes
}

// diffBindings matches bindings by (canonical node, prop) and compares refs by
// canonical node, export and expression, so a binding between matched nodes is
// unchanged whatever the ids.
func diffBindings(from, to []GraphBinding, m nodeMatch) []BindingChange {
	type key struct{ node, prop string }
	refKey := func(keys map[string]string, r BindingRef) string {
		node := ""
		if r.Node != "" {
			node = canon(keys, r.Node)
		}
		return node + "\x00" + r.Export + "\x00" + r.Expr
	}
	namedRef := func(names map[string]string, r BindingRef) BindingRef {
		if r.Node != "" {
			r.Node = display(names, r.Node)
		}
		return r
	}

	fromByKey := make(map[key]GraphBinding, len(from))
	for _, b := range from {
		fromByKey[key{canon(m.fromKey, b.Node), b.Prop}] = b
	}
	toByKey := make(map[key]GraphBinding, len(to))
	for _, b := range to {
		toByKey[key{canon(m.toKey, b.Node), b.Prop}] = b
	}

	var changes []BindingChange
	for _, b := range to {
		node := display(m.toName, b.Node)
		old, existed := fromByKey[key{canon(m.toKey, b.Node), b.Prop}]
		if !existed {
			changes = append(changes, BindingChange{Kind: ChangeAdded, Node: node, Prop: b.Prop, NewRef: namedRef(m.toName, b.Ref)})
			continue
		}
		if refKey(m.fromKey, old.Ref) != refKey(m.toKey, b.Ref) {
			changes = append(changes, BindingChange{
				Kind: ChangeChanged, Node: node, Prop: b.Prop,
				OldRef: namedRef(m.fromName, old.Ref), NewRef: namedRef(m.toName, b.Ref),
			})
		}
	}
	for _, b := range from {
		if _, stillThere := toByKey[key{canon(m.fromKey, b.Node), b.Prop}]; !stillThere {
			changes = append(changes, BindingChange{
				Kind: ChangeRemoved, Node: display(m.fromName, b.Node), Prop: b.Prop,
				OldRef: namedRef(m.fromName, b.Ref),
			})
		}
	}

	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].Node != changes[j].Node {
			return changes[i].Node < changes[j].Node
		}
		if changes[i].Prop != changes[j].Prop {
			return changes[i].Prop < changes[j].Prop
		}
		return changes[i].Kind < changes[j].Kind
	})
	return changes
}

// IsEmpty reports whether a diff found no structural change.
func (d GraphDiff) IsEmpty() bool {
	return len(d.NodeChanges) == 0 && len(d.EdgeChanges) == 0 && len(d.BindingChanges) == 0
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func blockOrderStr(bo []string) string {
	// A comma-joined list is a stable, readable canonical form for comparison —
	// block names never contain commas.
	return strings.Join(bo, ",")
}

func intPtrStr(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

// blockShape canonicalises a prop value for comparison only: a one-element
// list holding an object becomes that object, recursively. The renderer reads
// a block given as an object and as a list of one object identically (D2,
// blockInstances), and a re-parse of Alloy produces the object form where the
// builder saves the list form — without this the same block reads as changed
// (#203).
func blockShape(v any) any {
	switch x := v.(type) {
	case []any:
		if len(x) == 1 {
			if m, ok := x[0].(map[string]any); ok {
				if _, raw := rawExpr(m); !raw {
					return blockShape(m)
				}
			}
		}
		out := make([]any, len(x))
		for i, el := range x {
			out[i] = blockShape(el)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, el := range x {
			out[k] = blockShape(el)
		}
		return out
	}
	return v
}

// scalarStr renders a prop value for display and comparison. A string is
// returned as-is; anything else (number, bool, nested object/array) is
// canonically JSON-encoded so equal values compare equal regardless of Go's
// interface type (e.g. json numbers are float64).
func scalarStr(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
