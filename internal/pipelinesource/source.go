// Package pipelinesource names where a pipeline came from: the value
// pipelines.source stores (a text column with a CHECK constraint, last
// redefined in migration 0005).
//
// It is a leaf package so the sqlc output can use the type directly (the
// sqlc.yaml column override on pipelines.source): internal/store imports the
// generated code, so the type cannot live there without an import cycle.
package pipelinesource

import (
	"errors"
	"fmt"
)

// Source is a pipeline's origin. Switch over it exhaustively (the exhaustive
// linter checks every switch on this type): what may edit a pipeline, how it
// is matched to collectors and how its editor opens all depend on it.
type Source string

const (
	// UI is a pipeline written as Alloy text in the editor or through the
	// API. It is the default when a create names no source.
	UI Source = "ui"
	// Wizard is a pipeline a wizard rendered; its wizard_kind and
	// wizard_state let the wizard reopen and re-render it.
	Wizard Source = "wizard"
	// Git is a pipeline synced from a repository link. It is read-only in
	// Shepherd and matches only its link's target collector, never by
	// matchers.
	Git Source = "git"
	// Visual is a pipeline built in the visual builder; its wizard_state is
	// the alloy-graph document the contents were rendered from.
	Visual Source = "visual"
)

// ErrUnknown is returned by Parse for a value that is not a Source.
var ErrUnknown = errors.New("unknown pipeline source")

// All returns every Source, in declaration order.
func All() []Source {
	return []Source{UI, Wizard, Git, Visual}
}

// Parse is the one conversion from a string (an API field) to a Source. It
// fails closed: anything else, the empty string included, is ErrUnknown.
func Parse(s string) (Source, error) {
	switch src := Source(s); src {
	case UI, Wizard, Git, Visual:
		return src, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknown, s)
	}
}

// String returns the stored value.
func (s Source) String() string { return string(s) }
