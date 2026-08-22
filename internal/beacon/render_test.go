package beacon

import (
	"strings"
	"testing"

	"shepherd/internal/validate"
)

func validConfig() BaselineConfig {
	return NewBaselineConfig("https://shepherd.example.com/beacon/v1/write")
}

func TestRenderBaselinePipeline_ParsesAsAlloySyntax(t *testing.T) {
	out, err := RenderBaselinePipeline(validConfig())
	if err != nil {
		t.Fatalf("RenderBaselinePipeline: %v", err)
	}
	if r := validate.Stage1(out); !r.Valid {
		t.Fatalf("rendered baseline pipeline is not valid Alloy syntax: %+v\n---\n%s", r.Diagnostics, out)
	}
}

func TestRenderBaselinePipeline_ContainsExpectedComponents(t *testing.T) {
	out, err := RenderBaselinePipeline(validConfig())
	if err != nil {
		t.Fatalf("RenderBaselinePipeline: %v", err)
	}
	for _, want := range []string{
		`prometheus.exporter.self "beacon"`,
		`prometheus.scrape "beacon"`,
		`prometheus.relabel "beacon"`,
		`prometheus.remote_write "beacon"`,
		`sys.env("SHEPHERD_AGENT_TOKEN_ID")`,
		`sys.env("SHEPHERD_AGENT_TOKEN_SECRET")`,
		runningComponentsMetric,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered pipeline missing %q\n---\n%s", want, out)
		}
	}
	// No plaintext credential is ever a candidate for appearing here since
	// BaselineConfig has no field to hold one — see the doc comment on
	// DefaultTokenIDEnv/DefaultTokenSecretEnv for why.
}

func TestRenderBaselinePipeline_RequiresEveryField(t *testing.T) {
	base := validConfig()
	cases := []func(*BaselineConfig){
		func(c *BaselineConfig) { c.Label = "" },
		func(c *BaselineConfig) { c.RemoteWriteURL = "" },
		func(c *BaselineConfig) { c.ScrapeInterval = "" },
		func(c *BaselineConfig) { c.TokenIDEnv = "" },
		func(c *BaselineConfig) { c.TokenSecretEnv = "" },
	}
	for i, mutate := range cases {
		cfg := base
		mutate(&cfg)
		if _, err := RenderBaselinePipeline(cfg); err == nil {
			t.Errorf("case %d: expected an error for an incomplete BaselineConfig, got nil", i)
		}
	}
}

func TestRenderBaselinePipeline_RejectsBadLabel(t *testing.T) {
	cfg := validConfig()
	cfg.Label = "not a valid label!"
	if _, err := RenderBaselinePipeline(cfg); err == nil {
		t.Fatal("expected an error for an invalid Alloy identifier Label")
	}
}

func TestAppendBaseline_EmptyURLDisablesIt(t *testing.T) {
	got, err := AppendBaseline("existing content", NewBaselineConfig(""))
	if err != nil {
		t.Fatalf("AppendBaseline: %v", err)
	}
	if got != "existing content" {
		t.Fatalf("got %q, want content unchanged when RemoteWriteURL is empty", got)
	}
}

// TestAppendBaseline_ReachesServedContent is the "reaches BOTH paths" claim
// pinned at the unit level: whatever content a caller already assembled,
// AppendBaseline adds the SAME baseline pipeline text on top of it,
// regardless of what that content was — the two real callers
// (internal/agentapi's recomputeServeCache, internal/mgmtapi's
// recomputeOrgCaches) differ only in how they got `content`, never in how
// the baseline gets appended.
func TestAppendBaseline_ReachesServedContent(t *testing.T) {
	cfg := NewBaselineConfig("https://shepherd.example.com/beacon/v1/write")

	for _, content := range []string{"", "declare \"pipe_x\" {\n}\n"} {
		got, err := AppendBaseline(content, cfg)
		if err != nil {
			t.Fatalf("AppendBaseline(%q): %v", content, err)
		}
		if !strings.Contains(got, `prometheus.remote_write "beacon"`) {
			t.Fatalf("AppendBaseline(%q) = %q, missing the baseline pipeline", content, got)
		}
		if content != "" && !strings.Contains(got, content) {
			t.Fatalf("AppendBaseline(%q) = %q, lost the caller's existing content", content, got)
		}
	}
}
