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

// FormatPipeline canonicalises valid Alloy in-process and rejects unparseable
// input with InvalidArgument (so the editor leaves the buffer untouched). No
// DB or Alloy binary is needed — formatting is a pure syntax transform.
func TestFormatPipeline(t *testing.T) {
	reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
	if err != nil {
		t.Fatalf("schema.New: %v", err)
	}
	v := validate.New(&config.ValidateConfig{AlloyBinary: "", StabilityLevel: "experimental", Timeout: 10e9})
	svc := mgmtapi.NewPipelineService(nil, v, reg, slog.Default())

	t.Run("canonicalises valid source and is idempotent", func(t *testing.T) {
		const messy = "prometheus.scrape \"app\"   {\ntargets=[]\n    forward_to = []\n}"
		resp, err := svc.FormatPipeline(context.Background(), connect.NewRequest(&mgmtv1.FormatPipelineRequest{
			Contents: messy,
		}))
		if err != nil {
			t.Fatalf("FormatPipeline: %v", err)
		}
		out := resp.Msg.GetFormatted()
		if !strings.HasSuffix(out, "\n") {
			t.Fatalf("formatted output should end with a newline, got %q", out)
		}
		// Formatting already-formatted output changes nothing.
		again, err := svc.FormatPipeline(context.Background(), connect.NewRequest(&mgmtv1.FormatPipelineRequest{
			Contents: out,
		}))
		if err != nil {
			t.Fatalf("FormatPipeline (second pass): %v", err)
		}
		if again.Msg.GetFormatted() != out {
			t.Fatalf("format is not idempotent:\nfirst:\n%q\nsecond:\n%q", out, again.Msg.GetFormatted())
		}
	})

	t.Run("unparseable content is InvalidArgument", func(t *testing.T) {
		_, err := svc.FormatPipeline(context.Background(), connect.NewRequest(&mgmtv1.FormatPipelineRequest{
			Contents: "prometheus.scrape \"t\" {\n  targets = [\n",
		}))
		if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
			t.Fatalf("FormatPipeline error code = %v, want invalid_argument", got)
		}
	})
}
