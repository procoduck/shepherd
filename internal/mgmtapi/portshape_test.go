package mgmtapi_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"connectrpc.com/connect"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/internal/config"
	"shepherd/internal/mgmtapi"
	"shepherd/internal/schema"
	"shepherd/internal/validate"
	"shepherd/internal/version"
)

// issue233Contents is the issue's reproduction: `alloy validate` accepts it,
// Alloy refuses it at load. The marker comment keeps it out of the
// repository-wide no-false-positives corpus (internal/validate).
const issue233Contents = `// #233 reproduction: a list of lists
discovery.kubernetes "pods" {
  role = "pod"
}

prometheus.scrape "demo" {
  targets    = [discovery.kubernetes.pods.targets]
  forward_to = [prometheus.remote_write.demo.receiver]
}

prometheus.remote_write "demo" {
  endpoint {
    url = "https://prom.example.com/api/v1/push"
  }
}
`

// TestValidatePipeline_RefusesListOfLists exercises the check where the editor
// consumes it: the ValidatePipeline RPC, through the same Validator production
// wires (no Alloy binary here — the check is in-process and must not depend on
// one). CreatePipeline's refusal is covered against a real database in
// rpc_pipeline_test.go.
func TestValidatePipeline_RefusesListOfLists(t *testing.T) {
	reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
	if err != nil {
		t.Fatalf("schema.New: %v", err)
	}
	v := validate.New(&config.ValidateConfig{AlloyBinary: "", StabilityLevel: "experimental", Timeout: 10e9})
	svc := mgmtapi.NewPipelineService(nil, v, reg, slog.Default())

	resp, err := svc.ValidatePipeline(context.Background(), connect.NewRequest(&mgmtv1.ValidatePipelineRequest{
		Name: "demo", Contents: issue233Contents,
	}))
	if err != nil {
		t.Fatalf("ValidatePipeline: %v", err)
	}
	if resp.Msg.GetValid() {
		t.Fatalf("expected valid=false for a list-of-lists targets wire")
	}
	diags := resp.Msg.GetDiagnostics()
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %+v, want exactly one", diags)
	}
	d := diags[0]
	if d.GetStage() != 2 || !strings.Contains(d.GetMessage(), "is a list of lists") || d.GetLine() == 0 || d.GetCol() == 0 {
		t.Fatalf("diagnostic = %+v, want a stage-2 list-of-lists diagnostic with a position", d)
	}

	fixed := strings.Replace(issue233Contents, "[discovery.kubernetes.pods.targets]", "discovery.kubernetes.pods.targets", 1)
	resp, err = svc.ValidatePipeline(context.Background(), connect.NewRequest(&mgmtv1.ValidatePipelineRequest{
		Name: "demo", Contents: fixed,
	}))
	if err != nil {
		t.Fatalf("ValidatePipeline (fixed): %v", err)
	}
	if !resp.Msg.GetValid() {
		t.Fatalf("the corrected config must validate, diagnostics=%+v", resp.Msg.GetDiagnostics())
	}
}
