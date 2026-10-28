package visual

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Binding paths (#426). GraphBinding.Prop names where a binding's expression
// goes. A prop with neither `.` nor `[` is a top-level attribute and keeps the
// meaning it always had: Render writes `prop = expr` after the node's body,
// unchanged byte for byte. Anything else is a path into the component's nested
// blocks:
//
//	path    = segment *( "." segment )
//	segment = identifier [ "[" index "]" ]
//
// The last segment is the attribute and carries no index. The ones before it
// name blocks; a block name may itself contain dots (`stage.tenant`), so which
// segments group into one block name is settled by the component's schema,
// longest name first, and only the last segment of a block name may carry the
// index. The index picks the block instance (0-based); omitted, it is instance
// 0. The canonical form FormatBindingPath writes gives every repeatable block
// an index and no other block one: `endpoint[0].basic_auth.password`,
// `client.tls.ca_pem`, `stage.tenant[0].value`.
//
// A nested binding renders exactly as {"$expr": expr} written at the same
// place in props would, so the read-only parse (which keeps expressions in
// props) reproduces the text, and reports the binding back at its canonical
// path in ParseResult.Bindings.

// BindingPathToken is one lexical segment of a binding path.
type BindingPathToken struct {
	Name    string
	Index   int
	Indexed bool
}

// BindingBlockStep is one block a resolved binding path descends into.
type BindingBlockStep struct {
	Name       string
	Index      int
	Repeatable bool
}

// IsNestedBindingProp reports whether prop is a path into a nested block
// rather than a top-level attribute name.
func IsNestedBindingProp(prop string) bool {
	return strings.ContainsAny(prop, ".[")
}

// maxBindingIndex bounds a path index, so a hostile document cannot make the
// renderer reason about absurd instance numbers.
const maxBindingIndex = 9999

// MaxBindingPathSegments and MaxBindingPathBytes bound a nested binding path
// (#438 review). The path is client input on every Render path, and refusing
// it before splitting or resolving keeps that work constant per binding. The
// deepest attribute any shipped schema holds is 8 segments and 114 bytes
// (otelcol.processor.resourcedetection's
// gcp.resource_attributes.gcp.gce.instance.group_manager.name.enabled, with a
// four-digit index on each repeatable block); bindingpath_test.go fails if a
// schema ever needs more than half of either.
const (
	MaxBindingPathSegments = 32
	MaxBindingPathBytes    = 256
)

// ParseBindingPath splits a binding path into its segments, without the schema.
func ParseBindingPath(prop string) ([]BindingPathToken, error) {
	if prop == "" {
		return nil, errors.New("empty path")
	}
	if len(prop) > MaxBindingPathBytes {
		return nil, fmt.Errorf("the path is %d bytes; the limit is %d bytes", len(prop), MaxBindingPathBytes)
	}
	if n := strings.Count(prop, ".") + 1; n > MaxBindingPathSegments {
		return nil, fmt.Errorf("the path has %d segments; the limit is %d segments", n, MaxBindingPathSegments)
	}
	parts := strings.Split(prop, ".")
	out := make([]BindingPathToken, 0, len(parts))
	for i, part := range parts {
		tok := BindingPathToken{Name: part}
		if open := strings.IndexByte(part, '['); open >= 0 {
			if !strings.HasSuffix(part, "]") {
				return nil, fmt.Errorf("segment %q: unterminated index", part)
			}
			digits := part[open+1 : len(part)-1]
			if !validIndex(digits) {
				return nil, fmt.Errorf("segment %q: the index must be a non-negative integer", part)
			}
			n, err := strconv.Atoi(digits)
			if err != nil || n > maxBindingIndex {
				return nil, fmt.Errorf("segment %q: index out of range", part)
			}
			tok = BindingPathToken{Name: part[:open], Index: n, Indexed: true}
		}
		if !validIdent(tok.Name) {
			return nil, fmt.Errorf("segment %q is not an identifier", part)
		}
		if tok.Indexed && i == len(parts)-1 {
			return nil, errors.New("the attribute (last segment) cannot carry an index")
		}
		out = append(out, tok)
	}
	return out, nil
}

func validIndex(s string) bool {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func validIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		letter := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !letter && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// ResolveBindingPath resolves prop against a component's schema into the blocks
// it descends through and the attribute it names.
func ResolveBindingPath(c ComponentSchema, prop string) ([]BindingBlockStep, AttributeSchema, error) {
	steps, attr, _, err := ResolveBindingPathCounted(c, prop)
	return steps, attr, err
}

// ResolveBindingPathCounted is ResolveBindingPath reporting how many
// block-name candidates it tried — the work, which is linear in the path:
// at each level only as many segments are joined as the longest block name
// there has parts (one, for most components; six at most in any shipped
// schema).
func ResolveBindingPathCounted(c ComponentSchema, prop string) ([]BindingBlockStep, AttributeSchema, int, error) {
	tried := 0
	toks, err := ParseBindingPath(prop)
	if err != nil {
		return nil, AttributeSchema{}, tried, err
	}
	attrs, blocks := c.Attributes, c.Blocks
	var steps []BindingBlockStep
	i := 0
	for i < len(toks)-1 {
		byName := make(map[string]BlockSchema, len(blocks))
		maxParts := 0
		for _, b := range blocks {
			byName[b.Name] = b
			maxParts = max(maxParts, strings.Count(b.Name, ".")+1)
		}
		matched := false
		// Longest block name first (`stage.tenant` before `stage`), but never
		// longer than the longest name this level declares.
		for j := min(len(toks)-2, i+maxParts-1); j >= i && !matched; j-- {
			tried++
			names := make([]string, 0, j-i+1)
			indexedEarly := false
			for k := i; k <= j; k++ {
				names = append(names, toks[k].Name)
				if k < j && toks[k].Indexed {
					indexedEarly = true
				}
			}
			if indexedEarly {
				continue
			}
			name := strings.Join(names, ".")
			b, ok := byName[name]
			if !ok {
				continue
			}
			if !b.Repeatable && toks[j].Index > 0 {
				return nil, AttributeSchema{}, tried, fmt.Errorf("block %q appears once; index %d does not exist", name, toks[j].Index)
			}
			steps = append(steps, BindingBlockStep{Name: name, Index: toks[j].Index, Repeatable: b.Repeatable})
			attrs, blocks = b.Attributes, b.Blocks
			i = j + 1
			matched = true
		}
		if !matched {
			return nil, AttributeSchema{}, tried, fmt.Errorf("no block at %q", strings.Join(tokenNames(toks[i:len(toks)-1]), "."))
		}
	}
	leaf := toks[len(toks)-1].Name
	for _, a := range attrs {
		if a.Name == leaf {
			return steps, a, tried, nil
		}
	}
	for _, b := range blocks {
		if b.Name == leaf {
			return nil, AttributeSchema{}, tried, fmt.Errorf("%q is a block, not an attribute", leaf)
		}
	}
	return nil, AttributeSchema{}, tried, fmt.Errorf("no attribute %q there", leaf)
}

func tokenNames(toks []BindingPathToken) []string {
	out := make([]string, len(toks))
	for i, t := range toks {
		out[i] = t.Name
	}
	return out
}

// FormatBindingPath writes a resolved path in its canonical form.
func FormatBindingPath(steps []BindingBlockStep, attr string) string {
	var b strings.Builder
	for _, s := range steps {
		b.WriteString(s.Name)
		if s.Repeatable {
			fmt.Fprintf(&b, "[%d]", s.Index)
		}
		b.WriteByte('.')
	}
	b.WriteString(attr)
	return b.String()
}

// errBindingNotBlock and errBindingIndex are setAtBindingPath's refusals.
var (
	errBindingNotBlock = errors.New("the props there are not a block")
	errBindingIndex    = errors.New("no such block instance")
)

// copyOnWrite tracks the containers one node's binding overlay has already
// copied, so each is copied at most once however many bindings pass through
// it (#438 review: copying the node's props per binding was bindings x props).
// The document's own props are never written.
type copyOnWrite struct{ owned map[uintptr]bool }

func newCopyOnWrite() *copyOnWrite { return &copyOnWrite{owned: map[uintptr]bool{}} }

func (c *copyOnWrite) ownMap(m map[string]interface{}) map[string]interface{} {
	if m != nil && c.owned[reflect.ValueOf(m).Pointer()] {
		return m
	}
	out := make(map[string]interface{}, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	c.owned[reflect.ValueOf(out).Pointer()] = true
	return out
}

func (c *copyOnWrite) ownSlice(s []interface{}) []interface{} {
	if len(s) > 0 && c.owned[reflect.ValueOf(s).Pointer()] {
		return s
	}
	out := append([]interface{}{}, s...)
	if len(out) > 0 {
		c.owned[reflect.ValueOf(out).Pointer()] = true
	}
	return out
}

// instanceAt is the index-th block instance a props value holds, and how many
// it holds — the one instance only, in constant time (blockInstances walks
// them all). ok is false when the value is not block-shaped there.
func instanceAt(v interface{}, index int) (inst map[string]interface{}, count int, ok bool) {
	switch x := v.(type) {
	case nil:
		return nil, 0, true
	case map[string]interface{}:
		if _, raw := rawExpr(x); raw {
			return nil, 0, false
		}
		if index == 0 {
			return x, 1, true
		}
		return nil, 1, true
	case []interface{}:
		if index >= len(x) {
			return nil, len(x), true
		}
		m, isMap := x[index].(map[string]interface{})
		return m, len(x), isMap
	}
	return nil, 0, false
}

// checkBindingPath reports, without writing anything, whether the resolved
// path can be placed in props: every block on it is block-shaped and holds
// the instance, or is missing and the index is 0.
func checkBindingPath(props map[string]interface{}, steps []BindingBlockStep) error {
	scope := props
	for _, step := range steps {
		var v interface{}
		if scope != nil {
			v = scope[step.Name]
		}
		inst, count, ok := instanceAt(v, step.Index)
		if !ok {
			return fmt.Errorf("%q: %w", step.Name, errBindingNotBlock)
		}
		if inst == nil && (count != 0 || step.Index != 0) {
			return fmt.Errorf("%s[%d]: %w (the node has %d)", step.Name, step.Index, errBindingIndex, count)
		}
		scope = inst
	}
	return nil
}

// setAtBindingPath places value at the resolved path and returns the props to
// use: containers on the path are copied once through cow, a missing block is
// created for instance 0, and prior reports whether a value was already there.
// Nothing is written when the path cannot be placed.
func setAtBindingPath(cow *copyOnWrite, props map[string]interface{}, steps []BindingBlockStep, attr string, value interface{}) (out map[string]interface{}, prior bool, err error) {
	if err := checkBindingPath(props, steps); err != nil {
		return nil, false, err
	}
	out = cow.ownMap(props)
	scope := out
	for _, step := range steps {
		var child map[string]interface{}
		switch v := scope[step.Name].(type) {
		case []interface{}:
			list := cow.ownSlice(v)
			inst, isMap := list[step.Index].(map[string]interface{})
			if !isMap { // checkBindingPath has refused this already
				return nil, false, fmt.Errorf("%q: %w", step.Name, errBindingNotBlock)
			}
			child = cow.ownMap(inst)
			list[step.Index] = child
			scope[step.Name] = list
		case map[string]interface{}:
			child = cow.ownMap(v)
			scope[step.Name] = child
		default: // missing: checkBindingPath allowed it only for instance 0
			child = cow.ownMap(nil)
			scope[step.Name] = child
		}
		scope = child
	}
	_, prior = scope[attr]
	scope[attr] = value
	return out, prior, nil
}

// ApplyBindingPath returns a copy of props with value written where the
// binding path prop points (see ResolveBindingPath), creating the block when
// the props have none and the index is 0. props itself is not modified.
func ApplyBindingPath(c ComponentSchema, props map[string]interface{}, prop string, value interface{}) (map[string]interface{}, error) {
	steps, attr, err := ResolveBindingPath(c, prop)
	if err != nil {
		return nil, err
	}
	next, _, err := setAtBindingPath(newCopyOnWrite(), props, steps, attr.Name, value)
	return next, err
}

// applyNestedBindings overlays a node's nested bindings onto a copy of its
// props as {"$expr": ...} values and returns the props to render and the
// bindings that stay top-level. A binding that does not resolve is reported
// and emits nothing.
func (rd *renderer) applyNestedBindings(id string, c ComponentSchema, known bool, props map[string]interface{}, binds []GraphBinding) (map[string]interface{}, []GraphBinding) {
	var top []GraphBinding
	cow := newCopyOnWrite()
	for _, bind := range binds {
		if !IsNestedBindingProp(bind.Prop) {
			top = append(top, bind)
			continue
		}
		expr := strings.TrimSpace(bind.Ref.Expr)
		if expr == "" {
			rd.diag("empty_binding_expr", sevError, id, fmt.Sprintf("binding for %q has an empty expression", clip(bind.Prop)))
			continue
		}
		if !known {
			rd.diag("binding_path_invalid", sevError, id,
				fmt.Sprintf("binding path %q: the component is not in the schema, so its blocks are unknown", clip(bind.Prop)))
			continue
		}
		steps, attr, err := ResolveBindingPath(c, bind.Prop)
		if err != nil {
			rd.diag("binding_path_invalid", sevError, id, fmt.Sprintf("binding path %q: %s", clip(bind.Prop), clip(err.Error())))
			continue
		}
		next, prior, err := setAtBindingPath(cow, props, steps, attr.Name, map[string]interface{}{exprKey: expr})
		if err != nil {
			rd.diag("binding_path_invalid", sevError, id, fmt.Sprintf("binding path %q: %s", clip(bind.Prop), clip(err.Error())))
			continue
		}
		if prior {
			rd.diag("binding_overrides_prop", sevWarning, id,
				fmt.Sprintf("%q is both bound and set as a value; the binding was used", clip(bind.Prop)))
		}
		props = next
	}
	return props, top
}
