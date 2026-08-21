package mgmtapi_test

import (
	"context"
	"log/slog"
	"testing"

	"connectrpc.com/connect"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/internal/config"
	"shepherd/internal/mgmtapi"
	"shepherd/internal/schema"
	"shepherd/internal/validate"
	"shepherd/internal/version"
)

// TestValidatePipeline_SurfacesSignals covers docs/gateway-tier-plan.md W1
// bullet 5: derived signals must be visible at authoring time (ValidatePipeline,
// called live while editing), not only discovered later when merge assembly
// excludes a role-mismatched pipeline. Uses a real *schema.Registry (embedded
// artifact) and a *validate.Validator with no Alloy binary configured — this
// path (Stages12, then signals.Derive) touches neither the database nor an
// external binary, so a plain *testing.T suffices; no ginkgo/DB fixture
// needed.
func TestValidatePipeline_SurfacesSignals(t *testing.T) {
	reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
	if err != nil {
		t.Fatalf("schema.New: %v", err)
	}
	v := validate.New(&config.ValidateConfig{AlloyBinary: "", StabilityLevel: "experimental", Timeout: 10e9})
	svc := mgmtapi.NewPipelineService(nil, v, reg, slog.Default())

	t.Run("metrics-only pipeline reports signals=[metrics], proven", func(t *testing.T) {
		const contents = `
prometheus.scrape "app" {
  forward_to = [prometheus.remote_write.sink.receiver]
}

prometheus.remote_write "sink" {
  endpoint {
    url = "https://example.com/write"
  }
}
`
		resp, err := svc.ValidatePipeline(context.Background(), connect.NewRequest(&mgmtv1.ValidatePipelineRequest{
			Name: "t", Contents: contents,
		}))
		if err != nil {
			t.Fatalf("ValidatePipeline: %v", err)
		}
		if !resp.Msg.GetValid() {
			t.Fatalf("expected valid=true, diagnostics=%+v", resp.Msg.GetDiagnostics())
		}
		if got := resp.Msg.GetSignals(); len(got) != 1 || got[0] != "metrics" {
			t.Fatalf("Signals = %v, want [metrics]", got)
		}
		if !resp.Msg.GetSignalsProven() {
			t.Fatalf("expected SignalsProven=true for an all-known-component pipeline")
		}
		if len(resp.Msg.GetUnknownComponents()) != 0 {
			t.Fatalf("UnknownComponents = %v, want none", resp.Msg.GetUnknownComponents())
		}
	})

	t.Run("unknown component is reported, not silently dropped, and signals_proven is false", func(t *testing.T) {
		const contents = `
totally.bogus.component "x" {
  foo = "bar"
}

prometheus.scrape "app" {
  forward_to = [prometheus.remote_write.sink.receiver]
}

prometheus.remote_write "sink" {
  endpoint {
    url = "https://example.com/write"
  }
}
`
		resp, err := svc.ValidatePipeline(context.Background(), connect.NewRequest(&mgmtv1.ValidatePipelineRequest{
			Name: "t", Contents: contents,
		}))
		if err != nil {
			t.Fatalf("ValidatePipeline: %v", err)
		}
		if resp.Msg.GetSignalsProven() {
			t.Fatalf("expected SignalsProven=false when a component is unrecognized")
		}
		if got := resp.Msg.GetUnknownComponents(); len(got) != 1 || got[0] != "totally.bogus.component" {
			t.Fatalf("UnknownComponents = %v, want [totally.bogus.component]", got)
		}
	})

	t.Run("unparseable contents: signals fields are left empty, not a second error", func(t *testing.T) {
		resp, err := svc.ValidatePipeline(context.Background(), connect.NewRequest(&mgmtv1.ValidatePipelineRequest{
			Name: "t", Contents: `prometheus.scrape "app" {`,
		}))
		if err != nil {
			t.Fatalf("ValidatePipeline: %v", err)
		}
		if resp.Msg.GetValid() {
			t.Fatalf("expected valid=false for unterminated block")
		}
		if len(resp.Msg.GetSignals()) != 0 || len(resp.Msg.GetUnknownComponents()) != 0 {
			t.Fatalf("expected no signals/unknown-components on a parse failure, got signals=%v unknown=%v",
				resp.Msg.GetSignals(), resp.Msg.GetUnknownComponents())
		}
	})
}
