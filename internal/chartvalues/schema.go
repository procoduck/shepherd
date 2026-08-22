package chartvalues

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
	kyaml "sigs.k8s.io/yaml"
)

const (
	schemaPath = "testdata/values.schema.json"
	metaPath   = "testdata/values.schema.meta.json"
	// schemaResourceURL is an arbitrary in-memory identifier for
	// Compiler.AddResource/Compile — it is never dereferenced over the
	// network (this package never fetches at runtime; only `make
	// chart-verify` does, and that is a shell script, not this code path).
	schemaResourceURL = "https://shepherd.internal/chartvalues/values.schema.json"
)

// SchemaMeta is testdata/values.schema.meta.json's shape: the provenance
// record for the vendored schema (where it was fetched, at what chart
// version, its checksum at fetch time). `make check-chartvalues-pin` reads
// ChartVersion from the raw file directly (shell, no Go); LoadSchemaMeta is
// this package's own typed accessor for the same file, used by tests that
// assert PinnedChartVersion agrees with what was actually vendored.
type SchemaMeta struct {
	ChartVersion string `json:"chart_version"`
	SourceURL    string `json:"source_url"`
	ReleaseTag   string `json:"release_tag"`
	FetchedAt    string `json:"fetched_at"`
	SHA256       string `json:"sha256"`
}

// RawSchema returns the vendored values.schema.json's exact bytes, as fetched
// from upstream — the same bytes `make chart-verify` re-fetches and diffs
// against.
func RawSchema() ([]byte, error) {
	b, err := Embedded.ReadFile(schemaPath)
	if err != nil {
		return nil, fmt.Errorf("chartvalues: reading embedded %s: %w", schemaPath, err)
	}
	return b, nil
}

// LoadSchemaMeta parses the vendored schema's provenance record.
func LoadSchemaMeta() (SchemaMeta, error) {
	b, err := Embedded.ReadFile(metaPath)
	if err != nil {
		return SchemaMeta{}, fmt.Errorf("chartvalues: reading embedded %s: %w", metaPath, err)
	}
	var m SchemaMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return SchemaMeta{}, fmt.Errorf("chartvalues: parsing %s: %w", metaPath, err)
	}
	return m, nil
}

// SchemaDocument parses the vendored schema into a generic
// map[string]interface{} tree — the form schema_test.go's key-membership
// walker needs to resolve "$ref" indirections and read a node's declared
// "properties" set directly from the artifact, which is a stronger check
// than jsonschema.Schema.Validate can give: this chart's schema leaves
// several objects (notably the top-level "collectors" map) with no
// "properties"/"additionalProperties" restriction at all, so validating
// against the compiled schema alone would silently accept an invented key
// there. Walking the raw document and asserting membership in its declared
// property set is the check that actually enforces "every key exists in the
// vendored schema" (this package's brief) for those open objects.
func SchemaDocument() (map[string]any, error) {
	raw, err := RawSchema()
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("chartvalues: %s is not valid JSON: %w", schemaPath, err)
	}
	return doc, nil
}

var (
	compileOnce sync.Once
	compiled    *jsonschema.Schema
	compileErr  error
)

func compileValuesSchema() (*jsonschema.Schema, error) {
	raw, err := RawSchema()
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaResourceURL, bytes.NewReader(raw)); err != nil {
		return nil, fmt.Errorf("chartvalues: loading vendored schema into compiler: %w", err)
	}
	s, err := c.Compile(schemaResourceURL)
	if err != nil {
		return nil, fmt.Errorf("chartvalues: compiling vendored schema: %w", err)
	}
	return s, nil
}

// ValidateValues checks valuesYAML — a full or partial k8s-monitoring Helm
// values document, e.g. Render's output — against the vendored
// values.schema.json (G9). The compiled schema is cached process-wide
// (compilation is not free and this schema is large); the cache holds a
// *jsonschema.Schema, which the library documents as safe for concurrent
// validation.
func ValidateValues(valuesYAML []byte) error {
	compileOnce.Do(func() { compiled, compileErr = compileValuesSchema() })
	if compileErr != nil {
		return compileErr
	}

	valuesJSON, err := kyaml.YAMLToJSON(valuesYAML)
	if err != nil {
		return fmt.Errorf("chartvalues: values is not valid YAML: %w", err)
	}
	var v any
	if err := json.Unmarshal(valuesJSON, &v); err != nil {
		return fmt.Errorf("chartvalues: values did not decode as JSON after YAML conversion: %w", err)
	}

	if err := compiled.Validate(v); err != nil {
		return fmt.Errorf("chartvalues: generated values do not validate against %s (chart %s): %w",
			schemaPath, PinnedChartVersion, err)
	}
	return nil
}
