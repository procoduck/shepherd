//go:build ignore

// extract.go — Shepherd schema extractor for grafana/alloy.
// Injected as cmd/shepherd-schema-dump/main.go inside the alloy checkout by
// tools/alloy-schema-gen/run.sh, then run with: go run ./cmd/shepherd-schema-dump
//
// Output: JSON to stdout, matching the schema/alloy-v<X>.json artifact shape.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	// Blank import registers every component into the global registry.
	_ "github.com/grafana/alloy/internal/component/all"

	"github.com/grafana/alloy/internal/component"
	"github.com/grafana/alloy/internal/component/metadata"
	"github.com/grafana/alloy/internal/featuregate"
)

// wireTypeMap maps metadata Type.Name → our canonical wire-type id (§3.1).
var wireTypeMap = map[string]string{
	"Targets":                         "targets",
	"Prometheus `MetricsReceiver`":     "prom.metrics",
	"Loki `LogsReceiver`":              "loki.logs",
	"OpenTelemetry `otelcol.Consumer`": "otel.any", // refined per-signal below when possible
	"Pyroscope `ProfilesReceiver`":     "pyroscope.profiles",
}

// ---- schema types ----

type AttrDef struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

type BlockDef struct {
	Name       string     `json:"name"`
	Repeatable bool       `json:"repeatable,omitempty"`
	Attributes []AttrDef  `json:"attributes,omitempty"`
	Blocks     []BlockDef `json:"blocks,omitempty"`
}

type PortDef struct {
	Prop        string `json:"prop,omitempty"`
	Export      string `json:"export,omitempty"`
	Type        string `json:"type"`
	Cardinality string `json:"cardinality,omitempty"`
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

func main() {
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
			argsType := reflect.TypeOf(reg.Args)
			for argsType.Kind() == reflect.Ptr {
				argsType = argsType.Elem()
			}
			if argsType.Kind() == reflect.Struct {
				attrs, blocks := walkStruct(argsType, 0)
				cs.Attributes = attrs
				cs.Blocks = blocks
			} else {
				cs.Opaque = true
			}
		} else {
			cs.Opaque = true
		}

		// Port types from metadata.
		meta, err := metadata.ForComponent(name)
		if err == nil {
			for _, t := range meta.AllTypesAccepted() {
				if wt, ok := wireTypeMap[t.Name]; ok {
					cs.Inputs = append(cs.Inputs, PortDef{
						Type:        wt,
						Cardinality: "list",
					})
				}
			}
			for _, t := range meta.AllTypesExported() {
				if wt, ok := wireTypeMap[t.Name]; ok {
					cs.Outputs = append(cs.Outputs, PortDef{
						Type: wt,
					})
				}
			}
		}

		cs.DefaultSnippet = fmt.Sprintf("%s \"example\" {}\n", name)
		components[name] = cs
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

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(artifact); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
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

// schemaType maps a Go reflect.Type to a schema type string.
func schemaType(t reflect.Type) string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == reflect.TypeOf(time.Duration(0)) {
		return "duration"
	}
	// Check for secret types by package+name
	if t.PkgPath() != "" {
		full := t.PkgPath() + "." + t.Name()
		if strings.Contains(full, "alloytypes") && (strings.Contains(t.Name(), "Secret") || strings.Contains(t.Name(), "secret")) {
			return "secret"
		}
		// OptionalSecret
		if strings.Contains(t.Name(), "OptionalSecret") {
			return "secret"
		}
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
	case reflect.Slice:
		return "list"
	case reflect.Map:
		return "map"
	}
	return "capsule"
}

const maxDepth = 4

// walkStruct extracts attributes and nested blocks from a struct type via alloy tags.
func walkStruct(t reflect.Type, depth int) ([]AttrDef, []BlockDef) {
	if depth > maxDepth {
		return nil, nil
	}
	var attrs []AttrDef
	var blocks []BlockDef

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("alloy")
		if tag == "" || tag == "-" {
			// Recurse into embedded structs.
			if f.Anonymous {
				ft := f.Type
				for ft.Kind() == reflect.Ptr {
					ft = ft.Elem()
				}
				if ft.Kind() == reflect.Struct {
					a, b := walkStruct(ft, depth+1)
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
		name := parts[0]
		kind := parts[1]
		required := true
		for _, p := range parts[2:] {
			if p == "optional" {
				required = false
			}
		}

		ft := f.Type
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}

		switch kind {
		case "attr":
			attrs = append(attrs, AttrDef{
				Name:     name,
				Type:     schemaType(ft),
				Required: required,
			})
		case "block":
			elemType := ft
			if ft.Kind() == reflect.Slice {
				elemType = ft.Elem()
				for elemType.Kind() == reflect.Ptr {
					elemType = elemType.Elem()
				}
			}
			bd := BlockDef{
				Name:       name,
				Repeatable: ft.Kind() == reflect.Slice,
			}
			if elemType.Kind() == reflect.Struct {
				subAttrs, subBlocks := walkStruct(elemType, depth+1)
				bd.Attributes = subAttrs
				bd.Blocks = subBlocks
			}
			blocks = append(blocks, bd)
		}
	}
	return attrs, blocks
}
