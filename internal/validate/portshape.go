package validate

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"shepherd/internal/schema"
	"shepherd/internal/version"
	"shepherd/internal/visual"
)

// The port-shape check (#233) types component references against the
// embedded component schema for the pinned Alloy version — the same artifact
// and overlay the visual builder renders with — so it needs no caller wiring:
// every path that runs Stage 2 runs it.
var (
	portSchemaOnce sync.Once
	portSchema     visual.SchemaPayload
	portSchemaErr  error
)

func loadPortSchema() (visual.SchemaPayload, error) {
	portSchemaOnce.Do(func() {
		reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
		if err != nil {
			portSchemaErr = fmt.Errorf("validate: load schema: %w", err)
			return
		}
		merged, _, err := reg.Get(version.AlloySchemaVersion)
		if err != nil {
			portSchemaErr = fmt.Errorf("validate: load schema %s: %w", version.AlloySchemaVersion, err)
			return
		}
		b, err := json.Marshal(merged)
		if err != nil {
			portSchemaErr = fmt.Errorf("validate: encode schema: %w", err)
			return
		}
		if err := json.Unmarshal(b, &portSchema); err != nil {
			portSchemaErr = fmt.Errorf("validate: decode schema: %w", err)
		}
	})
	return portSchema, portSchemaErr
}

// PortShapes runs the schema port-shape check (part of Stage 2) on content and
// returns its diagnostics. It is in-process and needs no Alloy binary, so it
// runs even where `alloy validate` is skipped.
//
// An embedded schema that fails to load is a build defect the schema package's
// own tests catch; here it is logged and the check reports nothing rather than
// refusing every save — this check exists to add refusals that are certain,
// never ones that are not.
func PortShapes(content string) []Diagnostic {
	payload, err := loadPortSchema()
	if err != nil {
		slog.Error("port-shape check unavailable", "err", err)
		return nil
	}
	found := visual.CheckPortShapes(content, payload)
	if len(found) == 0 {
		return nil
	}
	out := make([]Diagnostic, len(found))
	for i, d := range found {
		out[i] = Diagnostic{Line: d.Line, Col: d.Col, Message: d.Message, Stage: 2}
	}
	return out
}
