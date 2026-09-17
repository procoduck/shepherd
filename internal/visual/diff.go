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
// ChangeChanged.
type NodeChange struct {
	Kind         string        `json:"kind"`
	ID           string        `json:"id"`
	Component    string        `json:"component"`
	Label        string        `json:"label"`
	FieldChanges []FieldChange `json:"field_changes,omitempty"`
}

// EdgeChange is one wire added, removed, or changed (endpoints or order). From/To
// are the target-graph endpoints for added/changed, the base-graph endpoints for
// removed.
type EdgeChange struct {
	Kind string  `json:"kind"`
	ID   string  `json:"id"`
	From PortRef `json:"from"`
	To   PortRef `json:"to"`
}

// BindingChange is one property binding added, removed, or changed. OldRef is
// set for removed/changed, NewRef for added/changed.
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
// what the pipeline does. Output is deterministically ordered (nodes/edges by
// id, bindings by node then prop, field changes by field) so a diff of the same
// two graphs is byte-stable.
func DiffGraphs(from, to GraphDocument) GraphDiff {
	diff := GraphDiff{
		NodeChanges:    diffNodes(from.Nodes, to.Nodes),
		EdgeChanges:    diffEdges(from.Edges, to.Edges),
		BindingChanges: diffBindings(from.Bindings, to.Bindings),
	}
	return diff
}

func diffNodes(from, to []GraphNode) []NodeChange {
	fromByID := make(map[string]GraphNode, len(from))
	for _, n := range from {
		fromByID[n.ID] = n
	}
	toByID := make(map[string]GraphNode, len(to))
	for _, n := range to {
		toByID[n.ID] = n
	}

	var changes []NodeChange
	for _, n := range to {
		old, existed := fromByID[n.ID]
		if !existed {
			changes = append(changes, NodeChange{
				Kind: ChangeAdded, ID: n.ID, Component: n.Component, Label: n.Label,
			})
			continue
		}
		if fcs := nodeFieldChanges(old, n); len(fcs) > 0 {
			changes = append(changes, NodeChange{
				Kind: ChangeChanged, ID: n.ID, Component: n.Component, Label: n.Label,
				FieldChanges: fcs,
			})
		}
	}
	for _, n := range from {
		if _, stillThere := toByID[n.ID]; !stillThere {
			changes = append(changes, NodeChange{
				Kind: ChangeRemoved, ID: n.ID, Component: n.Component, Label: n.Label,
			})
		}
	}

	sort.SliceStable(changes, func(i, j int) bool { return changes[i].ID < changes[j].ID })
	return changes
}

// nodeFieldChanges lists the attributes that differ between two nodes that share
// an id. Position is intentionally excluded (pure layout). Props are compared
// key by key so a diff names the exact attribute that changed.
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
	if oldBO, curBO := blockOrderStr(old.BlockOrder), blockOrderStr(cur.BlockOrder); oldBO != curBO {
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
		case ook && nok && os != ns:
			fcs = append(fcs, FieldChange{Field: "prop:" + k, OldValue: os, NewValue: ns})
		case ook && !nok:
			fcs = append(fcs, FieldChange{Field: "prop:" + k, OldValue: os, NewValue: ""})
		case !ook && nok:
			fcs = append(fcs, FieldChange{Field: "prop:" + k, OldValue: "", NewValue: ns})
		}
	}
	return fcs
}

func diffEdges(from, to []GraphEdge) []EdgeChange {
	fromByID := make(map[string]GraphEdge, len(from))
	for _, e := range from {
		fromByID[e.ID] = e
	}
	toByID := make(map[string]GraphEdge, len(to))
	for _, e := range to {
		toByID[e.ID] = e
	}

	var changes []EdgeChange
	for _, e := range to {
		old, existed := fromByID[e.ID]
		if !existed {
			changes = append(changes, EdgeChange{Kind: ChangeAdded, ID: e.ID, From: e.From, To: e.To})
			continue
		}
		if !edgesEqual(old, e) {
			changes = append(changes, EdgeChange{Kind: ChangeChanged, ID: e.ID, From: e.From, To: e.To})
		}
	}
	for _, e := range from {
		if _, stillThere := toByID[e.ID]; !stillThere {
			changes = append(changes, EdgeChange{Kind: ChangeRemoved, ID: e.ID, From: e.From, To: e.To})
		}
	}

	sort.SliceStable(changes, func(i, j int) bool { return changes[i].ID < changes[j].ID })
	return changes
}

func edgesEqual(a, b GraphEdge) bool {
	return a.From == b.From && a.To == b.To && intPtrStr(a.Order) == intPtrStr(b.Order)
}

func diffBindings(from, to []GraphBinding) []BindingChange {
	type key struct{ node, prop string }
	fromByKey := make(map[key]GraphBinding, len(from))
	for _, b := range from {
		fromByKey[key{b.Node, b.Prop}] = b
	}
	toByKey := make(map[key]GraphBinding, len(to))
	for _, b := range to {
		toByKey[key{b.Node, b.Prop}] = b
	}

	var changes []BindingChange
	for _, b := range to {
		old, existed := fromByKey[key{b.Node, b.Prop}]
		if !existed {
			changes = append(changes, BindingChange{Kind: ChangeAdded, Node: b.Node, Prop: b.Prop, NewRef: b.Ref})
			continue
		}
		if old.Ref != b.Ref {
			changes = append(changes, BindingChange{Kind: ChangeChanged, Node: b.Node, Prop: b.Prop, OldRef: old.Ref, NewRef: b.Ref})
		}
	}
	for _, b := range from {
		if _, stillThere := toByKey[key{b.Node, b.Prop}]; !stillThere {
			changes = append(changes, BindingChange{Kind: ChangeRemoved, Node: b.Node, Prop: b.Prop, OldRef: b.Ref})
		}
	}

	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].Node != changes[j].Node {
			return changes[i].Node < changes[j].Node
		}
		return changes[i].Prop < changes[j].Prop
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
