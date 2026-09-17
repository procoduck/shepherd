package visual

// StabilityExperimental is the stability value that gates a component behind the
// org opt-in (#114). Any other value (including empty) is treated as permitted.
const StabilityExperimental = "experimental"

// ExperimentalNode identifies one node in a graph whose component is
// experimental, for reporting which nodes a gate would reject.
type ExperimentalNode struct {
	NodeID    string
	Component string
	Label     string
}

// ExperimentalNodes returns the enabled nodes in doc whose component is marked
// experimental in the schema, in document order. Disabled nodes are skipped —
// they don't render, so they can't drag an experimental dependency into the
// output. A component absent from the schema is treated as non-experimental
// here: an unknown component is the renderer's problem to diagnose, not this
// gate's. Pure; the caller decides whether the org permits these.
func ExperimentalNodes(doc GraphDocument, payload SchemaPayload) []ExperimentalNode {
	var out []ExperimentalNode
	for _, n := range doc.Nodes {
		if n.Disabled {
			continue
		}
		if c, ok := payload.Components[n.Component]; ok && c.Stability == StabilityExperimental {
			out = append(out, ExperimentalNode{NodeID: n.ID, Component: n.Component, Label: n.Label})
		}
	}
	return out
}
