package mgmtapi_test

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/internal/config"
	"shepherd/internal/mgmtapi"
	"shepherd/internal/schema"
	"shepherd/internal/validate"
	"shepherd/internal/version"
	"shepherd/internal/wizard/wizardtest"
)

// TestValidatePipeline_DiagnosticPositions covers walkthrough F2: the
// ValidatePipeline RPC — what the pipeline editor's gutter and problem list
// render, and what MCP's validate_pipeline/propose_pipeline_revision return —
// reports line:col in the text the caller sent, not in the declare-wrapped
// document the gate validates (which put everything +1 line, +2 columns).
func TestValidatePipeline_DiagnosticPositions(t *testing.T) {
	reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
	if err != nil {
		t.Fatalf("schema.New: %v", err)
	}
	call := func(t *testing.T, bin, contents string) []string {
		t.Helper()
		v := validate.New(&config.ValidateConfig{AlloyBinary: bin, StabilityLevel: "experimental", Timeout: 2 * time.Minute})
		svc := mgmtapi.NewPipelineService(nil, v, reg, slog.Default())
		resp, err := svc.ValidatePipeline(context.Background(), connect.NewRequest(&mgmtv1.ValidatePipelineRequest{
			Name: "preview", Contents: contents,
		}))
		if err != nil {
			t.Fatalf("ValidatePipeline: %v", err)
		}
		if resp.Msg.GetValid() {
			t.Fatalf("expected valid=false for %q", contents)
		}
		var got []string
		for _, d := range resp.Msg.GetDiagnostics() {
			got = append(got, fmt.Sprintf("%d:%d %s", d.GetLine(), d.GetCol(), d.GetMessage()))
		}
		return got
	}

	t.Run("stage 1 error inside a nested block on line 3", func(t *testing.T) {
		got := call(t, "", "prometheus.remote_write \"a\" {\n  endpoint {\n    url = = \"x\"\n  }\n}\n")
		if len(got) == 0 || !strings.HasPrefix(got[0], "3:11 ") {
			t.Fatalf("diagnostics = %q, want the first at 3:11", got)
		}
	})

	t.Run("one-line document: every diagnostic stays on line 1", func(t *testing.T) {
		got := call(t, "", "x")
		want := []string{
			"1:2 expected block label, got TERMINATOR",
			"1:2 expected attribute assignment or block body, got }",
			"1:2 expected TERMINATOR, got IDENT",
			"1:2 expected }, got EOF",
		}
		if !slices.Equal(got, want) {
			t.Fatalf("diagnostics = %q, want %q", got, want)
		}
	})

	// The walkthrough's exact reproduction, through the real pinned
	// `alloy validate` (Stage 2). Self-skips without Docker/alloy.
	t.Run("stage 2 unrecognized attribute on line 1", func(t *testing.T) {
		bin := wizardtest.AlloyBinary()
		if bin == "" {
			t.Skip("no alloy binary and no docker")
		}
		got := call(t, bin, `prometheus.exporter.self "a" { bogus = 1 }`)
		want := []string{`1:32 unrecognized attribute name "bogus"`}
		if !slices.Equal(got, want) {
			t.Fatalf("diagnostics = %q, want %q", got, want)
		}
	})
}
