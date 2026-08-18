// Package wizard provides a pluggable wizard registry.
// Each wizard kind knows how to render its step schema and commit its output
// to a pipeline.
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
	Steps []Step `json:"steps"`
}

// CommitResult is the output of committing a wizard to a pipeline.
type CommitResult struct {
	// Contents is the generated Alloy syntax pipeline body.
	Contents string
	// Matchers is the suggested matcher set.
	Matchers []string
}

// Wizard is the interface each wizard kind must implement.
type Wizard interface {
	Kind() string
	Schema() Schema
	// Commit generates the pipeline contents from the wizard state.
	// state is the user-provided JSON blob (map of fieldName → value).
	Commit(state map[string]any) (CommitResult, error)
}

// Registry maps wizard kinds to their implementations.
type Registry struct {
	wizards map[string]Wizard
}

var defaultRegistry = &Registry{wizards: make(map[string]Wizard)}

// Register adds a wizard to the default registry.
func Register(w Wizard) {
	defaultRegistry.wizards[w.Kind()] = w
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

// UnmarshalState unmarshals JSON bytes into a wizard state map.
func UnmarshalState(raw json.RawMessage) (map[string]any, error) {
	if raw == nil {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("unmarshaling wizard state: %w", err)
	}
	return m, nil
}
