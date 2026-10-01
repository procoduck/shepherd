package mgmtapi_test

import (
	"context"
	"log/slog"
	"os/exec"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/internal/config"
	"shepherd/internal/mgmtapi"
	"shepherd/internal/schema"
	"shepherd/internal/validate"
	"shepherd/internal/version"
)

// TestValidatePipeline_ReportsSkippedStages covers #209: with no Alloy binary
// configured, Stage 2 never runs, and ValidatePipeline must say so in
// skipped_stages rather than return a bare valid=true the editor renders as
// "No problems". Same DB-free shape as TestValidatePipeline_SurfacesSignals.
func TestValidatePipeline_ReportsSkippedStages(t *testing.T) {
	reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
	if err != nil {
		t.Fatalf("schema.New: %v", err)
	}
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
	call := func(t *testing.T, bin string) *mgmtv1.ValidatePipelineResponse {
		t.Helper()
		v := validate.New(&config.ValidateConfig{AlloyBinary: bin, StabilityLevel: "experimental", Timeout: 10 * time.Second})
		svc := mgmtapi.NewPipelineService(nil, v, reg, slog.Default())
		resp, err := svc.ValidatePipeline(context.Background(), connect.NewRequest(&mgmtv1.ValidatePipelineRequest{
			Name: "t", Contents: contents,
		}))
		if err != nil {
			t.Fatalf("ValidatePipeline: %v", err)
		}
		return resp.Msg
	}

	t.Run("no alloy binary: valid, with stage 2 reported skipped", func(t *testing.T) {
		msg := call(t, "")
		if !msg.GetValid() {
			t.Fatalf("expected valid=true, diagnostics=%+v", msg.GetDiagnostics())
		}
		if got := msg.GetSkippedStages(); !slices.Equal(got, []int32{2}) {
			t.Fatalf("SkippedStages = %v, want [2]", got)
		}
	})

	t.Run("alloy binary configured: nothing skipped", func(t *testing.T) {
		bin, err := exec.LookPath("true")
		if err != nil {
			t.Skipf("no `true` binary on PATH: %v", err)
		}
		msg := call(t, bin)
		if !msg.GetValid() {
			t.Fatalf("expected valid=true, diagnostics=%+v", msg.GetDiagnostics())
		}
		if got := msg.GetSkippedStages(); len(got) != 0 {
			t.Fatalf("SkippedStages = %v, want none", got)
		}
	})
}
