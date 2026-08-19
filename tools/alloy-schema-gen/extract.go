//go:build ignore

// extract.go — Shepherd schema extractor for grafana/alloy.
// Injected as cmd/shepherd-schema-dump/main.go inside the alloy checkout by
// tools/alloy-schema-gen/run.sh (which copies portmodel.go in beside it), then
// run with: go run ./cmd/shepherd-schema-dump
//
// Output: JSON to stdout, matching the schema/alloy-v<X>.json artifact shape.
// A human-readable coverage summary is written to stderr.
package main

import (
	"encoding"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	// Blank import registers every component into the global registry.
	_ "github.com/grafana/alloy/internal/component/all"

	"github.com/grafana/alloy/internal/component"
	"github.com/grafana/alloy/internal/featuregate"
)

// maxDepth bounds block nesting. Recursion is additionally cut by a per-path
// type stack (see walkStruct), so a recursive config type such as
// loki/process's StageConfig -> MatchConfig -> []StageConfig terminates on the
// cycle rather than on the depth budget.
const maxDepth = 8

// alloyModulePrefix is the import-path prefix whose packages can be re-parsed
// from source (the extractor runs with the checkout root as its cwd).
const alloyModulePrefix = "github.com/grafana/alloy/"

// ---- interfaces implemented by alloy config types ----

// defaulter mirrors syntax.Defaulter without importing it.
type defaulter interface{ SetToDefault() }

// alloyCapsule mirrors syntax.Capsule without importing it.
type alloyCapsule interface{ AlloyCapsule() }

var (
	textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	capsuleType       = reflect.TypeOf((*alloyCapsule)(nil)).Elem()
	durationType      = reflect.TypeOf(time.Duration(0))
)

// wireTypeForGoType resolves a struct field's Go type to a wire type, reporting
// whether the field is a list-cardinality port.
func wireTypeForGoType(t reflect.Type) (wt WireType, isList bool, ok bool) {
	t = derefType(t)
	if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		isList = true
		t = derefType(t.Elem())
	}
	if NonPortGoTypes[t.String()] {
		return WireType{}, false, false
	}
	wt, found := GoTypeWireMap[t.String()]
	if !found {
		return WireType{}, false, false
	}
	return wt, isList, true
}

func derefType(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t
}

func derefValue(v reflect.Value) reflect.Value {
	for v.IsValid() && v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}

// ---- schema types ----

// AttrDef is one settable attribute. InputType is set when the attribute is
// also a port (its Go type is a wire type) — the L1 validator uses it to skip
// the "required attribute missing" check for something the canvas wires up.
type AttrDef struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Required  bool     `json:"required"`
	InputType string   `json:"input_type,omitempty"`
	Default   any      `json:"default,omitempty"`
	Values    []string `json:"values,omitempty"`
}

// BlockDef is one nested block. Repeatable and Required are always emitted so
// consumers never have to distinguish "false" from "absent".
type BlockDef struct {
	Name       string     `json:"name"`
	Repeatable bool       `json:"repeatable"`
	Required   bool       `json:"required"`
	Truncated  bool       `json:"truncated,omitempty"`
	Attributes []AttrDef  `json:"attributes"`
	Blocks     []BlockDef `json:"blocks"`
}

// PortDef is one canvas port.
//
// Prop/Export is the stable dotted id a stored graph references; Path is the
// same id pre-split, so the renderer can emit `output { metrics = [...] }`
// without re-parsing the name (Alloy attribute names may not contain a dot).
// Role is the dataflow direction (decision D1) — see RoleFor.
type PortDef struct {
	Prop        string   `json:"prop,omitempty"`
	Export      string   `json:"export,omitempty"`
	Type        string   `json:"type"`
	Role        string   `json:"role"`
	Cardinality string   `json:"cardinality,omitempty"`
	Path        []string `json:"path,omitempty"`
}

type ComponentSchema struct {
	Stability      string     `json:"stability"`
	Doc            string     `json:"doc"`
	Attributes     []AttrDef  `json:"attributes"`
	Blocks         []BlockDef `json:"blocks"`
	Inputs         []PortDef  `json:"inputs"`
	Outputs        []PortDef  `json:"outputs"`
	DefaultSnippet string     `json:"default_snippet"`
	Opaque         bool       `json:"opaque,omitempty"`
}

type Artifact struct {
	Meta       map[string]any              `json:"_meta"`
	Components map[string]*ComponentSchema `json:"components"`
}

// ---- port extraction ----

// collectPorts walks an Arguments or Exports struct and returns one PortDef per
// field whose Go type is a wire type, carrying the field's dotted path.
//
// It descends into tagged single blocks (so otelcol/faro/beyla consumers, which
// live inside `output {}` rather than at the top level, get real names) and
// through `,squash` embeds. It does NOT descend into repeatable blocks: a port
// inside a block that may appear N times has no stable address for a stored
// graph to reference.
func collectPorts(t reflect.Type, export bool, path []string, depth int, stack []reflect.Type, stats *stats) []PortDef {
	t = derefType(t)
	if t == nil || t.Kind() != reflect.Struct || depth > maxDepth || onStack(stack, t) {
		return nil
	}
	stack = append(stack, t)

	var ports []PortDef
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("alloy")
		if tag == "" || tag == "-" {
			if f.Anonymous {
				ports = append(ports, collectPorts(f.Type, export, path, depth, stack, stats)...)
			}
			continue
		}
		parts := strings.Split(tag, ",")
		if len(parts) < 2 {
			continue
		}
		name, kind := parts[0], parts[1]

		switch kind {
		case "squash":
			ports = append(ports, collectPorts(f.Type, export, path, depth, stack, stats)...)
		case "attr":
			if name == "" {
				continue
			}
			wt, isList, ok := wireTypeForGoType(f.Type)
			if !ok {
				continue
			}
			p := PortDef{
				Type: RefineOtelWire(wt.Wire, name),
				Role: RoleFor(wt.Kind, export),
				Path: append(append([]string{}, path...), name),
			}
			if export {
				p.Export = PortName(p.Path)
			} else {
				p.Prop = PortName(p.Path)
				if isList {
					p.Cardinality = "list"
				}
			}
			ports = append(ports, p)
		case "block":
			if name == "" {
				continue
			}
			ft := derefType(f.Type)
			if ft.Kind() == reflect.Slice || ft.Kind() == reflect.Array {
				// Repeatable block: a port inside it has no stable address.
				if inner := collectPorts(derefType(ft.Elem()), export, append(append([]string{}, path...), name), depth+1, stack, stats); len(inner) > 0 {
					stats.portsInRepeatableBlocks += len(inner)
				}
				continue
			}
			ports = append(ports, collectPorts(ft, export, append(append([]string{}, path...), name), depth+1, stack, stats)...)
		}
	}
	return ports
}

func onStack(stack []reflect.Type, t reflect.Type) bool {
	for _, s := range stack {
		if s == t {
			return true
		}
	}
	return false
}

// ---- attribute / block extraction ----

// walkStruct extracts attributes and nested blocks from a struct type via alloy
// tags, reading defaults out of v (a value already passed through SetToDefault
// where the type implements it).
//
// Tag kinds handled: attr, block, squash (flattened in place, at the same
// depth), enum (a repeatable one-of; each alternative becomes a block named
// "<enum>.<alternative>", which is how Alloy addresses it). label is skipped.
func walkStruct(t reflect.Type, v reflect.Value, depth int, stack []reflect.Type, enums *enumIndex) ([]AttrDef, []BlockDef) {
	attrs := []AttrDef{}
	blocks := []BlockDef{}
	t = derefType(t)
	if t == nil || t.Kind() != reflect.Struct {
		return attrs, blocks
	}
	stack = append(stack, t)

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		fv := fieldValue(v, i)
		tag := f.Tag.Get("alloy")
		if tag == "" || tag == "-" {
			if f.Anonymous {
				ft := derefType(f.Type)
				if ft.Kind() == reflect.Struct {
					a, b := walkStruct(ft, subStructValue(fv, ft), depth, stack, enums)
					attrs = append(attrs, a...)
					blocks = append(blocks, b...)
				}
			}
			continue
		}
		parts := strings.Split(tag, ",")
		if len(parts) < 2 {
			continue
		}
		name, kind := parts[0], parts[1]
		optional := false
		for _, p := range parts[2:] {
			if p == "optional" {
				optional = true
			}
		}
		ft := derefType(f.Type)

		switch kind {
		case "squash":
			// An embedded config struct spliced into the parent's surface —
			// e.g. loki.write endpoint's *types.HTTPClientConfig, which is where
			// basic_auth / oauth2 / tls_config actually live.
			if ft.Kind() != reflect.Struct || onStack(stack, ft) {
				continue
			}
			a, b := walkStruct(ft, subStructValue(fv, ft), depth, stack, enums)
			attrs = append(attrs, a...)
			blocks = append(blocks, b...)

		case "attr":
			if name == "" {
				continue
			}
			ad := AttrDef{Name: name, Type: schemaType(ft), Required: !optional}
			if wt, _, ok := wireTypeForGoType(f.Type); ok {
				ad.InputType = RefineOtelWire(wt.Wire, name)
			}
			ad.Values = enums.valuesFor(ft)
			ad.Default = defaultLiteral(ft, fv)
			attrs = append(attrs, ad)

		case "block":
			if name == "" {
				continue
			}
			elem := ft
			repeatable := ft.Kind() == reflect.Slice || ft.Kind() == reflect.Array
			if repeatable {
				elem = derefType(ft.Elem())
			}
			bd := BlockDef{
				Name:       name,
				Repeatable: repeatable,
				Required:   !optional,
				Attributes: []AttrDef{},
				Blocks:     []BlockDef{},
			}
			if elem.Kind() == reflect.Struct {
				switch {
				case depth+1 > maxDepth, onStack(stack, elem):
					bd.Truncated = true
				default:
					sv := reflect.Value{}
					if !repeatable {
						sv = subStructValue(fv, elem)
					} else {
						sv = defaultsOf(elem)
					}
					bd.Attributes, bd.Blocks = walkStruct(elem, sv, depth+1, stack, enums)
				}
			}
			blocks = append(blocks, bd)

		case "enum":
			if name == "" {
				continue
			}
			elem := ft
			if elem.Kind() == reflect.Slice || elem.Kind() == reflect.Array {
				elem = derefType(elem.Elem())
			}
			if elem.Kind() != reflect.Struct || depth+1 > maxDepth || onStack(stack, elem) {
				blocks = append(blocks, BlockDef{
					Name: name, Repeatable: true, Required: !optional, Truncated: true,
					Attributes: []AttrDef{}, Blocks: []BlockDef{},
				})
				continue
			}
			_, alts := walkStruct(elem, defaultsOf(elem), depth+1, stack, enums)
			for _, alt := range alts {
				alt.Name = name + "." + alt.Name
				alt.Repeatable = true
				alt.Required = false
				blocks = append(blocks, alt)
			}
		}
	}
	return attrs, blocks
}

// fieldValue returns v's i-th field, or the zero Value when v is unusable.
func fieldValue(v reflect.Value, i int) reflect.Value {
	if !v.IsValid() || v.Kind() != reflect.Struct || i >= v.NumField() {
		return reflect.Value{}
	}
	f := v.Field(i)
	if !f.CanInterface() {
		return reflect.Value{}
	}
	return f
}

// subStructValue resolves the value to walk for a nested struct: the parent's
// already-defaulted field where one exists, otherwise a freshly defaulted
// instance of the type (which is what a nil block pointer means in practice).
func subStructValue(fv reflect.Value, t reflect.Type) reflect.Value {
	dv := derefValue(fv)
	if dv.IsValid() && dv.Kind() == reflect.Struct && !dv.IsZero() {
		return dv
	}
	return defaultsOf(t)
}

// defaultsOf returns a value of t with SetToDefault applied where implemented.
func defaultsOf(t reflect.Type) reflect.Value {
	t = derefType(t)
	if t == nil || t.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	pv := reflect.New(t)
	if d, ok := pv.Interface().(defaulter); ok {
		// A few SetToDefault implementations dereference sub-blocks; a panic
		// here must degrade to "no defaults", never kill the extraction (D4).
		func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "warn: SetToDefault panicked for %s: %v\n", t.String(), r)
				}
			}()
			d.SetToDefault()
		}()
	}
	return pv.Elem()
}

// defaultLiteral renders a field's default as a JSON-representable literal, or
// nil when there is nothing worth prefilling. Zero values are omitted (they are
// indistinguishable from "unset"), and secrets are never emitted.
func defaultLiteral(t reflect.Type, v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	v = derefValue(v)
	if !v.IsValid() || !v.CanInterface() || v.IsZero() {
		return nil
	}
	t = derefType(t)
	if schemaType(t) == "secret" {
		return nil
	}
	if t == durationType {
		if d, ok := v.Interface().(time.Duration); ok {
			return d.String()
		}
	}
	if text, ok := marshalText(v); ok {
		return text
	}
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Bool:
		return v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint()
	case reflect.Float32, reflect.Float64:
		return v.Float()
	case reflect.Slice, reflect.Array:
		if derefType(t.Elem()).Kind() == reflect.String {
			out := make([]string, 0, v.Len())
			for i := 0; i < v.Len(); i++ {
				out = append(out, derefValue(v.Index(i)).String())
			}
			return out
		}
	case reflect.Map:
		if t.Key().Kind() == reflect.String && derefType(t.Elem()).Kind() == reflect.String {
			out := map[string]string{}
			for _, k := range v.MapKeys() {
				out[k.String()] = derefValue(v.MapIndex(k)).String()
			}
			return out
		}
	}
	return nil
}

// marshalText renders v through encoding.TextMarshaler when it implements it,
// which is how Alloy itself writes such values (syntax/internal/value/type.go).
func marshalText(v reflect.Value) (string, bool) {
	if !v.IsValid() || !v.CanInterface() {
		return "", false
	}
	if v.Type() == durationType {
		return "", false
	}
	tryMarshal := func(i any) (string, bool) {
		tm, ok := i.(encoding.TextMarshaler)
		if !ok {
			return "", false
		}
		var b []byte
		var err error
		func() {
			defer func() { _ = recover() }()
			b, err = tm.MarshalText()
		}()
		if err != nil || len(b) == 0 {
			return "", false
		}
		return string(b), true
	}
	if s, ok := tryMarshal(v.Interface()); ok {
		return s, true
	}
	if v.CanAddr() {
		if s, ok := tryMarshal(v.Addr().Interface()); ok {
			return s, true
		}
	}
	return "", false
}

// schemaType maps a Go reflect.Type to a schema type string. It mirrors Alloy's
// own AlloyType (syntax/internal/value/type.go) — capsules, TextMarshalers and
// durations before the kind switch — with two refinements the inspector needs:
// "duration" and "secret" split out of "string".
func schemaType(t reflect.Type) string {
	if t == nil {
		return "capsule"
	}
	for t.Kind() == reflect.Ptr {
		if isSecretType(t) {
			return "secret"
		}
		if t.Implements(capsuleType) {
			return "capsule"
		}
		if t.Implements(textMarshalerType) {
			return "string"
		}
		t = t.Elem()
	}
	if isSecretType(t) {
		return "secret"
	}
	if t == durationType {
		return "duration"
	}
	if t.Implements(capsuleType) || reflect.PointerTo(t).Implements(capsuleType) {
		return "capsule"
	}
	if t.Implements(textMarshalerType) || reflect.PointerTo(t).Implements(textMarshalerType) {
		return "string"
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "list"
	case reflect.Map:
		return "map"
	}
	return "capsule"
}

func isSecretType(t reflect.Type) bool {
	t = derefType(t)
	if t == nil || t.PkgPath() == "" {
		return false
	}
	if !strings.Contains(t.PkgPath(), "alloytypes") {
		return false
	}
	return strings.Contains(t.Name(), "Secret") || strings.Contains(t.Name(), "secret")
}

// ---- enum values ----

// enumIndex resolves the documented value set of a named string type by
// re-parsing the alloy source package that declares it and collecting the
// string constants of that type. Nothing is invented: a plain `string`
// attribute whose valid values live only in prose or in a Validate() method
// yields no values, and none is fabricated for it.
type enumIndex struct {
	root  string
	cache map[string][]string
}

func newEnumIndex(root string) *enumIndex {
	return &enumIndex{root: root, cache: map[string][]string{}}
}

func (e *enumIndex) valuesFor(t reflect.Type) []string {
	t = derefType(t)
	if t == nil || t.Kind() != reflect.String || t.Name() == "" {
		return nil
	}
	pkg := t.PkgPath()
	if !strings.HasPrefix(pkg, alloyModulePrefix) {
		return nil
	}
	key := pkg + "." + t.Name()
	if v, ok := e.cache[key]; ok {
		return v
	}
	vals := e.scan(strings.TrimPrefix(pkg, alloyModulePrefix), t.Name())
	e.cache[key] = vals
	return vals
}

func (e *enumIndex) scan(relDir, typeName string) []string {
	dir := filepath.Join(e.root, relDir)
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					id, ok := vs.Type.(*ast.Ident)
					if !ok || id.Name != typeName {
						continue
					}
					for _, val := range vs.Values {
						lit, ok := val.(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						s, err := strconv.Unquote(lit.Value)
						if err != nil || s == "" || seen[s] {
							continue
						}
						seen[s] = true
						out = append(out, s)
					}
				}
			}
		}
	}
	sort.Strings(out)
	if len(out) < 2 {
		// A single constant is a named default, not a choice.
		return nil
	}
	return out
}

// ---- coverage stats (stderr only) ----

type stats struct {
	components              int
	inputs, outputs         int
	unnamedPorts            int
	rolesProduces           int
	rolesAccepts            int
	withBlocks              int
	attributes              int
	blocks                  int
	defaults                int
	enums                   int
	truncated               int
	portsInRepeatableBlocks int
	wireTypes               map[string]int
}

func (s *stats) countAttrs(attrs []AttrDef) {
	for _, a := range attrs {
		s.attributes++
		if a.Default != nil {
			s.defaults++
		}
		if len(a.Values) > 0 {
			s.enums++
		}
	}
}

func (s *stats) countBlocks(blocks []BlockDef) {
	for _, b := range blocks {
		s.blocks++
		if b.Truncated {
			s.truncated++
		}
		s.countAttrs(b.Attributes)
		s.countBlocks(b.Blocks)
	}
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cwd: %v\n", err)
		os.Exit(1)
	}
	enums := newEnumIndex(root)
	st := &stats{wireTypes: map[string]int{}}

	names := component.AllNames()
	sort.Strings(names)

	components := make(map[string]*ComponentSchema, len(names))

	for _, name := range names {
		reg, ok := component.Get(name)
		if !ok {
			continue
		}

		cs := &ComponentSchema{
			Stability:  stabilityStr(reg.Stability),
			Doc:        "", // overlay fills docs
			Attributes: []AttrDef{},
			Blocks:     []BlockDef{},
			Inputs:     []PortDef{},
			Outputs:    []PortDef{},
		}

		// Reflect Args struct for attributes + blocks.
		if reg.Args != nil {
			argsType := derefType(reflect.TypeOf(reg.Args))
			if argsType.Kind() == reflect.Struct {
				attrs, blocks := walkStruct(argsType, defaultsOf(argsType), 0, nil, enums)
				cs.Attributes, cs.Blocks = attrs, blocks
			} else {
				cs.Opaque = true
			}
		} else {
			cs.Opaque = true
		}

		// Ports come from the Arguments/Exports struct tags, because only those
		// carry the port NAME (targets, forward_to, receiver, output.metrics)
		// that a stored graph references. The alloy metadata package knows the
		// types but not the names, and its five types are exactly the five in
		// GoTypeWireMap, so descending through blocks and squashes here is a
		// strict superset of what it could report — there is no fallback.
		if reg.Args != nil {
			cs.Inputs = collectPorts(reflect.TypeOf(reg.Args), false, nil, 0, nil, st)
		}
		if reg.Exports != nil {
			cs.Outputs = collectPorts(reflect.TypeOf(reg.Exports), true, nil, 0, nil, st)
		}
		if cs.Inputs == nil {
			cs.Inputs = []PortDef{}
		}
		if cs.Outputs == nil {
			cs.Outputs = []PortDef{}
		}
		if cs.Attributes == nil {
			cs.Attributes = []AttrDef{}
		}
		if cs.Blocks == nil {
			cs.Blocks = []BlockDef{}
		}

		cs.DefaultSnippet = fmt.Sprintf("%s \"example\" {}\n", name)
		components[name] = cs

		st.components++
		st.inputs += len(cs.Inputs)
		st.outputs += len(cs.Outputs)
		if len(cs.Blocks) > 0 {
			st.withBlocks++
		}
		st.countAttrs(cs.Attributes)
		st.countBlocks(cs.Blocks)
		for _, p := range append(append([]PortDef{}, cs.Inputs...), cs.Outputs...) {
			st.wireTypes[p.Type]++
			if p.Prop == "" && p.Export == "" {
				st.unnamedPorts++
			}
			switch p.Role {
			case RoleProduces:
				st.rolesProduces++
			case RoleAccepts:
				st.rolesAccepts++
			default:
				fmt.Fprintf(os.Stderr, "ERROR: port on %s has no role\n", name)
				os.Exit(1)
			}
		}
	}

	if len(components) == 0 {
		fmt.Fprintln(os.Stderr, "ERROR: zero components extracted — blank import may have failed")
		os.Exit(1)
	}

	artifact := Artifact{
		Meta: map[string]any{
			"generated_by":     "shepherd-schema-gen/extract.go",
			"alloy_version":    os.Getenv("ALLOY_VERSION"),
			"generated_at":     time.Now().UTC().Format(time.RFC3339),
			"components_total": len(components),
		},
		Components: components,
	}

	printStats(st)

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(artifact); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}

func printStats(s *stats) {
	fmt.Fprintf(os.Stderr, "== extraction coverage ==\n")
	fmt.Fprintf(os.Stderr, "components:            %d\n", s.components)
	fmt.Fprintf(os.Stderr, "ports in/out:          %d / %d\n", s.inputs, s.outputs)
	fmt.Fprintf(os.Stderr, "unnamed ports:         %d\n", s.unnamedPorts)
	fmt.Fprintf(os.Stderr, "roles produces/accepts:%d / %d\n", s.rolesProduces, s.rolesAccepts)
	fmt.Fprintf(os.Stderr, "components w/ blocks:  %d\n", s.withBlocks)
	fmt.Fprintf(os.Stderr, "attributes (all depth):%d\n", s.attributes)
	fmt.Fprintf(os.Stderr, "blocks (all depth):    %d\n", s.blocks)
	fmt.Fprintf(os.Stderr, "attrs with default:    %d\n", s.defaults)
	fmt.Fprintf(os.Stderr, "attrs with enum values:%d\n", s.enums)
	fmt.Fprintf(os.Stderr, "truncated blocks:      %d\n", s.truncated)
	fmt.Fprintf(os.Stderr, "ports skipped (in repeatable blocks): %d\n", s.portsInRepeatableBlocks)
	keys := make([]string, 0, len(s.wireTypes))
	for k := range s.wireTypes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(os.Stderr, "wire %-20s %d\n", k, s.wireTypes[k])
	}
}

func stabilityStr(s featuregate.Stability) string {
	switch s {
	case featuregate.StabilityGenerallyAvailable:
		return "ga"
	case featuregate.StabilityPublicPreview:
		return "public-preview"
	case featuregate.StabilityExperimental:
		return "experimental"
	default:
		return "experimental"
	}
}
