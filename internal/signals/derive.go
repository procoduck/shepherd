package signals

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/grafana/alloy/syntax/ast"
	"github.com/grafana/alloy/syntax/parser"

	"shepherd/internal/schema"
)

// nonComponentBlocks names top-level Alloy syntax constructs that are not
// pipeline components and therefore never appear in the schema artifact's
// components map. Skipping them in Derive is not the "unknown component"
// case this package must report honestly — it is recognizing syntax that was
// never going to be a component in the first place, the same way a parser
// distinguishes a keyword from an unresolved identifier.
//
// This mirrors internal/visual/parse.go's configBlocks list, kept as a
// separate copy on purpose: this package's W1 territory
// (docs/gateway-tier-plan.md §8) is internal/signals/** only, and the list is
// Alloy syntax vocabulary — not a UI concern internal/visual owns — so
// duplicating a dozen well-known, rarely-changing keywords is cheaper and
// safer than adding a cross-package dependency for it.
var nonComponentBlocks = map[string]bool{
	"logging": true, "tracing": true, "http": true,
	"argument": true, "export": true, "remotecfg": true,
	"livedebugging": true, "import.file": true, "import.git": true,
	"import.http": true, "import.string": true,
}

// containerBlocks are not components themselves but their bodies CAN hold
// components, so Derive must recurse into them rather than skip them.
//
// Skipping these was a real defect, not a theoretical one: a pipeline whose
// components all live inside a `foreach` derived an EMPTY signal set that
// still reported Proven() == true, so role enforcement passed trivially for
// every role — the control silently covering nothing, which is exactly the
// failure class docs/project-status.md §6 names. `declare` is included for the
// same reason: a user-authored declare body holds real components, and the
// merge engine wraps pipelines in declare blocks itself.
var containerBlocks = map[string]bool{
	"foreach": true, "declare": true,
}

// Contribution records one top-level component block's part in a pipeline's
// derived signal set. Callers (the merge engine, a UI explaining a refusal to
// a user) need to say WHICH component is responsible for WHICH signal, not
// just refuse a whole pipeline with no explanation — this is what makes that
// possible.
type Contribution struct {
	// Component is the schema component name, e.g. "prometheus.scrape".
	Component string
	// Label is the block's Alloy label, e.g. "app" for `prometheus.scrape
	// "app" { ... }`.
	Label string
	// Signals is what this component alone contributes. Empty for a
	// plumbing-only component (e.g. discovery.kubernetes, which only speaks
	// "targets").
	Signals Set
	// WireTypes lists every distinct wire type found across this
	// component's schema-declared inputs and outputs ports, sorted. Kept
	// for diagnostics even when it includes "targets" (which contributes no
	// Signal) — a caller explaining "why does this count as a metrics
	// pipeline" wants to show the raw wire, not just the derived signal.
	WireTypes []string
}

// UnknownComponent is a top-level block whose name Derive could not find in
// the schema. It could be a typo, a component introduced by a newer Alloy
// release than the pinned schema, or a component removed by an older one —
// Derive does not try to guess which. See Signals.HasUnknown.
type UnknownComponent struct {
	Component string
	Label     string
}

// Signals is Derive's structured result: the pipeline's overall signal set,
// plus a per-component breakdown of how that set was reached, plus whatever
// Derive could NOT prove.
type Signals struct {
	// Combined is the union of every known component's Signals. This is
	// empty both for a pipeline that provably ships nothing (e.g. only
	// targets-typed discovery components) and — deliberately
	// indistinguishable at this field alone — for one Derive could not fully
	// classify. Check HasUnknown and Unclassified before treating an empty
	// Combined as a proven-empty pipeline; Proven reports the conjunction.
	Combined Set
	// Contributions lists one entry per top-level component block that
	// Derive found in the schema, in source order. A block skipped as a
	// nonComponentBlocks entry has no Contribution; a block Derive could not
	// find in the schema is in Unknown instead.
	Contributions []Contribution
	// Unknown lists every top-level block Derive treated as a possible
	// component (i.e. not a recognized non-component construct) but could
	// not find in the schema. Never silently dropped — see Derive's doc.
	Unknown []UnknownComponent
	// Unclassified lists any wire type Derive encountered on a known
	// component's ports that classifyWireType does not recognize, sorted
	// and de-duplicated. Given TestWireTypesExhaustive this should never be
	// non-empty against the pinned artifact; it exists as a defensive,
	// visible signal rather than a silent miscount if that invariant is
	// ever violated (e.g. a caller hands Derive a *schema.Registry pinned to
	// a version this package's wireTypeSignals table predates).
	Unclassified []string
}

// Has reports whether sig is in the pipeline's combined signal set.
func (s Signals) Has(sig Signal) bool {
	return s.Combined.Has(sig)
}

// HasUnknown reports whether Derive found at least one top-level block it
// could not resolve against the schema. Callers that need a PROVEN signal
// set — a role-enforcement decision, for instance — must treat true here as
// "signals unknown" and respond conservatively (refuse, or treat as
// carrying every signal), never as "no additional signal". See Derive's doc
// comment for the fail-safe rationale.
func (s Signals) HasUnknown() bool {
	return len(s.Unknown) > 0
}

// Proven reports whether Combined is a complete, provable account of the
// pipeline's signals: no unresolved components and no unclassified wire
// types. False means Combined is a floor, not the true set.
func (s Signals) Proven() bool {
	return len(s.Unknown) == 0 && len(s.Unclassified) == 0
}

// componentDef is the part of a schema component entry Derive needs: the
// distinct wire types across its inputs[] and outputs[] ports.
type componentDef struct {
	WireTypes []string
}

// Derive parses contents as Alloy syntax, walks its top-level component
// blocks, looks each one up in reg's current schema version, and returns the
// signal set the pipeline carries plus which component contributed which
// signal.
//
// Derive is intentionally shallow: it reads each component's OWN declared
// ports from the schema, not the graph of references between components. It
// does not evaluate expressions, does not follow which component's output
// feeds which component's input, and does not descend into `declare` blocks
// (custom components) — the schema has no ports for those anyway. A
// pipeline's signal set is the union of what every component IN it is
// capable of speaking, which is exactly what a role-enforcement decision
// needs ("could this pipeline put a log line on a metrics-only collector"),
// and is deliberately more conservative than a true dataflow analysis: a
// component with an unused loki.logs port still marks the pipeline as
// carrying Logs, even if nothing in this particular file happens to feed it.
// That is the safe direction to be wrong in for a refusal check.
//
// Unknown components (a top-level block that looks like a component but
// is not in the schema — a typo, or a component from an Alloy release the
// pinned schema does not know) are NEVER silently dropped: they are
// collected in Signals.Unknown for the caller to see and decide on. The
// fail-safe stance this package takes, and the one Enforce follows, is that
// an unknown component means the pipeline's true signal set cannot be
// proven, so callers needing a proof (role enforcement, a refusal decision)
// must treat Signals.HasUnknown()==true conservatively — refuse, or assume
// the worst — rather than proceed as if the unknown component contributed
// nothing.
//
// A syntax error is returned as an error, never as an empty Signals: an
// unparseable config's signal set is unknown, not empty, and the two must
// never be confused by a caller that only checks err == nil.
func Derive(contents string, reg *schema.Registry) (Signals, error) {
	file, err := parser.ParseFile("<pipeline>", []byte(contents))
	if err != nil {
		return Signals{}, fmt.Errorf("signals: parse: %w", err)
	}

	defs, err := loadComponentDefs(reg)
	if err != nil {
		return Signals{}, err
	}

	var out Signals
	combined := Set{}
	unclassified := map[string]bool{}

	// walk processes one statement list, recursing into container blocks so a
	// component nested inside `foreach`/`declare` counts exactly as much as a
	// top-level one.
	var walk func(body []ast.Stmt)
	walk = func(body []ast.Stmt) {
		for _, stmt := range body {
			block, ok := stmt.(*ast.BlockStmt)
			if !ok {
				// An attribute or other non-block statement carries no wire
				// type; nothing to derive from it.
				continue
			}
			name := strings.Join(block.Name, ".")
			if nonComponentBlocks[name] {
				continue
			}
			if containerBlocks[name] {
				walk(block.Body)
				continue
			}

			def, known := defs[name]
			if !known {
				out.Unknown = append(out.Unknown, UnknownComponent{Component: name, Label: block.Label})
				continue
			}

			contribSig := Set{}
			for _, wt := range def.WireTypes {
				if wt == wireTypeTargets {
					continue
				}
				sig, ok := classifyWireType(wt)
				if !ok {
					unclassified[wt] = true
					continue
				}
				contribSig = contribSig.Union(sig)
			}

			combined = combined.Union(contribSig)
			out.Contributions = append(out.Contributions, Contribution{
				Component: name,
				Label:     block.Label,
				Signals:   contribSig,
				WireTypes: def.WireTypes,
			})
		}
	}
	walk(file.Body)

	out.Combined = combined
	if len(unclassified) > 0 {
		out.Unclassified = make([]string, 0, len(unclassified))
		for wt := range unclassified {
			out.Unclassified = append(out.Unclassified, wt)
		}
		sort.Strings(out.Unclassified)
	}
	return out, nil
}

// loadComponentDefs reads reg's current schema version and reduces every
// component entry to the wire types Derive cares about.
func loadComponentDefs(reg *schema.Registry) (map[string]componentDef, error) {
	if reg == nil {
		return nil, errors.New("signals: nil schema registry")
	}
	version := reg.CurrentVersion()
	merged, _, err := reg.Get(version)
	if err != nil {
		return nil, fmt.Errorf("signals: load schema %s: %w", version, err)
	}
	componentsRaw, ok := merged["components"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("signals: schema %s: components is not a map", version)
	}

	defs := make(map[string]componentDef, len(componentsRaw))
	for name, raw := range componentsRaw {
		comp, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		defs[name] = componentDef{WireTypes: wireTypesOf(comp)}
	}
	return defs, nil
}

// wireTypesOf returns the distinct, sorted "type" values across a schema
// component entry's inputs[] and outputs[] arrays. Both arrays share the
// {..., "type": "<wire type>", "role": "accepts"|"produces", ...} shape (the
// outputs[] entries use "export" instead of "prop" for the port name, but
// Derive never needs the port name — only the wire type). A component's
// direction (accepts vs produces) does not change which signal it
// contributes: a component that ACCEPTS loki.logs is exactly as much a
// "logs" component, for role-enforcement purposes, as one that PRODUCES it —
// both must run somewhere that is allowed to see logs.
func wireTypesOf(comp map[string]any) []string {
	seen := map[string]bool{}
	var out []string
	for _, key := range [...]string{"inputs", "outputs"} {
		arr, ok := comp[key].([]any)
		if !ok {
			continue
		}
		for _, item := range arr {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			t, ok := m["type"].(string)
			if !ok {
				continue
			}
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}
