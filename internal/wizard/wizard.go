// Package wizard provides a pluggable wizard registry.
// Each wizard kind knows how to render its step schema and commit its output
// to a pipeline.
//
// Every wizard declares, via Role, which collector role its committed
// pipeline is meant to be served to (docs/gateway-tier-plan.md W8's gate,
// G6). Register wraps every wizard so that guarantee is structural rather
// than a discipline each wizard author has to remember: Commit's generated
// contents are derived and checked against the declared role's
// internal/signals policy before a caller ever sees a successful result —
// see role.go. A wizard whose output contradicts its own declared role is
// refused, not merely unlikely to occur.
package wizard

import (
	"encoding/json"
	"fmt"
)

// StepField describes a single form field in a wizard step.
type StepField struct {
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Type        string   `json:"type"` // "text", "select", "toggle", "number"
	Required    bool     `json:"required,omitempty"`
	Options     []string `json:"options,omitempty"`
	Default     any      `json:"default,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Description string   `json:"description,omitempty"`
}

// Step is a single page in the wizard flow.
type Step struct {
	ID     string      `json:"id"`
	Title  string      `json:"title"`
	Fields []StepField `json:"fields"`
}

// Schema is the full wizard definition returned to the frontend.
type Schema struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// Description is one sentence on what this wizard produces, shown on the
	// catalog card. It lives here rather than in the UI because a wizard that
	// cannot describe itself needs a matching entry in a hand-kept map on the
	// other side of the wire — which is exactly how five registered wizards
	// ended up invisible: the catalog page filtered out every kind it had no
	// local copy for.
	Description string `json:"description"`
	Steps       []Step `json:"steps"`
}

// CommitResult is the output of committing a wizard to a pipeline.
type CommitResult struct {
	// Contents is the generated Alloy syntax pipeline body.
	Contents string
	// Matchers is the suggested matcher set.
	Matchers []string
	// Role is the collector role this pipeline was checked against — the
	// same value Role(state) returned for this commit. Set by Register's
	// wrapper after a successful role check, never by a wizard's own
	// Commit; a caller (a UI, an audit trail) can show it without
	// recomputing Role itself.
	Role string
}

// Wizard is the interface each wizard kind must implement.
type Wizard interface {
	Kind() string
	Schema() Schema
	// Role returns the collector role this wizard's output is meant to be
	// served to, for the given committed state. Most wizards return a fixed
	// role regardless of state (a wizard that only ever generates a
	// metrics-shaped pipeline has no reason to ask); a wizard whose schema
	// lets the operator pick a role (e.g. a step field) must derive it from
	// state the same way Commit does, so the two never disagree. Register's
	// wrapper calls this to check Commit's actual output against it — see
	// role.go — so Role must return the role the generated content is
	// ACTUALLY meant for, not an aspirational or default one.
	Role(state map[string]any) string
	// Commit generates the pipeline contents from the wizard state.
	// state is the user-provided JSON blob (map of fieldName → value).
	Commit(state map[string]any) (CommitResult, error)
}

// Registry maps wizard kinds to their implementations.
type Registry struct {
	wizards map[string]Wizard
}

var defaultRegistry = &Registry{wizards: make(map[string]Wizard)}

// Register adds a wizard to the default registry, wrapped in roleEnforced
// (role.go) so every Commit call — from any caller, present or future — is
// checked against the wizard's declared role before it can succeed. This is
// the only path onto defaultRegistry, so there is no way to register a
// wizard that bypasses the check.
func Register(w Wizard) {
	defaultRegistry.wizards[w.Kind()] = roleEnforced{Wizard: w}
}

// Default returns the default registry.
func Default() *Registry { return defaultRegistry }

// Get returns a wizard by kind or an error if not found.
func (r *Registry) Get(kind string) (Wizard, error) {
	w, ok := r.wizards[kind]
	if !ok {
		return nil, fmt.Errorf("unknown wizard kind %q", kind)
	}
	return w, nil
}

// ListKinds returns all registered wizard kinds.
func (r *Registry) ListKinds() []string {
	kinds := make([]string, 0, len(r.wizards))
	for k := range r.wizards {
		kinds = append(kinds, k)
	}
	return kinds
}

// MarshalState marshals the wizard state map to JSON bytes.
func MarshalState(state map[string]any) (json.RawMessage, error) {
	b, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("marshaling wizard state: %w", err)
	}
	return b, nil
}
