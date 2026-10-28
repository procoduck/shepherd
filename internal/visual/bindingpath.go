package visual

import (
	"errors"
	"fmt"
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

// ParseBindingPath splits a binding path into its segments, without the schema.
func ParseBindingPath(prop string) ([]BindingPathToken, error) {
	if prop == "" {
		return nil, errors.New("empty path")
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
	toks, err := ParseBindingPath(prop)
	if err != nil {
		return nil, AttributeSchema{}, err
	}
	attrs, blocks := c.Attributes, c.Blocks
	var steps []BindingBlockStep
	i := 0
	for i < len(toks)-1 {
		matched := false
		// Longest block name first: `stage.tenant` before `stage`.
		for j := len(toks) - 2; j >= i && !matched; j-- {
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
			for _, b := range blocks {
				if b.Name != name {
					continue
				}
				if !b.Repeatable && toks[j].Index > 0 {
					return nil, AttributeSchema{}, fmt.Errorf("block %q appears once; index %d does not exist", name, toks[j].Index)
				}
				steps = append(steps, BindingBlockStep{Name: name, Index: toks[j].Index, Repeatable: b.Repeatable})
				attrs, blocks = b.Attributes, b.Blocks
				i = j + 1
				matched = true
				break
			}
		}
		if !matched {
			return nil, AttributeSchema{}, fmt.Errorf("no block at %q", strings.Join(tokenNames(toks[i:len(toks)-1]), "."))
		}
	}
	leaf := toks[len(toks)-1].Name
	for _, a := range attrs {
		if a.Name == leaf {
			return steps, a, nil
		}
	}
	for _, b := range blocks {
		if b.Name == leaf {
			return nil, AttributeSchema{}, fmt.Errorf("%q is a block, not an attribute", leaf)
		}
	}
	return nil, AttributeSchema{}, fmt.Errorf("no attribute %q there", leaf)
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

// setAtBindingPath returns a copy of props with value set at the resolved
// path, copying every container on the way and creating a missing block only
// for instance 0. prior reports whether a value was already there.
func setAtBindingPath(props map[string]interface{}, steps []BindingBlockStep, attr string, value interface{}) (out map[string]interface{}, prior bool, err error) {
	out = make(map[string]interface{}, len(props)+1)
	for k, v := range props {
		out[k] = v
	}
	if len(steps) == 0 {
		_, prior = props[attr]
		out[attr] = value
		return out, prior, nil
	}
	step := steps[0]
	current, present := props[step.Name]
	instances, ok := blockInstances(current)
	if !ok {
		return nil, false, fmt.Errorf("%q: %w", step.Name, errBindingNotBlock)
	}
	var inst map[string]interface{}
	switch {
	case step.Index < len(instances):
		inst = instances[step.Index]
	case step.Index == 0 && len(instances) == 0:
		inst = map[string]interface{}{}
	default:
		return nil, false, fmt.Errorf("%s[%d]: %w (the node has %d)", step.Name, step.Index, errBindingIndex, len(instances))
	}
	child, prior, err := setAtBindingPath(inst, steps[1:], attr, value)
	if err != nil {
		return nil, false, err
	}
	if list, isList := current.([]interface{}); isList && present {
		next := append([]interface{}{}, list...)
		next[step.Index] = child
		out[step.Name] = next
	} else {
		out[step.Name] = child
	}
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
	next, _, err := setAtBindingPath(props, steps, attr.Name, value)
	return next, err
}

// applyNestedBindings overlays a node's nested bindings onto a copy of its
// props as {"$expr": ...} values and returns the props to render and the
// bindings that stay top-level. A binding that does not resolve is reported
// and emits nothing.
func (rd *renderer) applyNestedBindings(id string, c ComponentSchema, known bool, props map[string]interface{}, binds []GraphBinding) (map[string]interface{}, []GraphBinding) {
	var top []GraphBinding
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
		next, prior, err := setAtBindingPath(props, steps, attr.Name, map[string]interface{}{exprKey: expr})
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
