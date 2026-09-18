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

func TestRenderBaselinePipeline_StampsCollectorID(t *testing.T) {
	cfg := validConfig()
	cfg.CollectorID = "col-abc-123"
	out, err := RenderBaselinePipeline(cfg)
	if err != nil {
		t.Fatalf("RenderBaselinePipeline: %v", err)
	}
	if r := validate.Stage1(out); !r.Valid {
		t.Fatalf("baseline with a collector id is not valid Alloy: %+v\n---\n%s", r.Diagnostics, out)
	}
	for _, want := range []string{
		`target_label = "shepherd_collector_id"`,
		`replacement  = "col-abc-123"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stamped baseline missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderBaselinePipeline_NoCollectorIDNoStamp(t *testing.T) {
	// The default config has no CollectorID; the stamp rule must be absent so a
	// pre-#110 baseline renders byte-identical to before.
	out, err := RenderBaselinePipeline(validConfig())
	if err != nil {
		t.Fatalf("RenderBaselinePipeline: %v", err)
	}
	if strings.Contains(out, "shepherd_collector_id") {
		t.Errorf("baseline with no collector id must not stamp one\n---\n%s", out)
	}
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

func TestRenderBaselinePipeline_OAuth2AuthBlock(t *testing.T) {
	cfg := NewBaselineConfigOAuth2(
		"https://shepherd.example.com/beacon/v1/write",
		"https://idp.example.com/oauth2/token",
		[]string{"api://shepherd-collectors/.default"},
	)
	out, err := RenderBaselinePipeline(cfg)
	if err != nil {
		t.Fatalf("RenderBaselinePipeline: %v", err)
	}
	if r := validate.Stage1(out); !r.Valid {
		t.Fatalf("oauth2 baseline is not valid Alloy syntax: %+v\n---\n%s", r.Diagnostics, out)
	}
	for _, want := range []string{
		"oauth2 {",
		`client_id     = sys.env("SHEPHERD_OIDC_CLIENT_ID")`,
		`client_secret = sys.env("SHEPHERD_OIDC_CLIENT_SECRET")`,
		`token_url     = "https://idp.example.com/oauth2/token"`,
		`scopes        = ["api://shepherd-collectors/.default"]`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("oauth2 pipeline missing %q\n---\n%s", want, out)
		}
	}
	// The oauth2 block replaces basic_auth entirely.
	if strings.Contains(out, "basic_auth") {
		t.Errorf("oauth2 pipeline must not also render basic_auth\n---\n%s", out)
	}
	if strings.Contains(out, "SHEPHERD_AGENT_TOKEN") {
		t.Errorf("oauth2 pipeline must not reference the agent-token env vars\n---\n%s", out)
	}
}

func TestRenderBaselinePipeline_OAuth2RequiresTokenURL(t *testing.T) {
	cfg := NewBaselineConfigOAuth2("https://s/beacon/v1/write", "", nil)
	if _, err := RenderBaselinePipeline(cfg); err == nil {
		t.Fatal("an oauth2 baseline with no token_url must be rejected")
	}
}

func TestOAuth2ForBeacon(t *testing.T) {
	if OAuth2ForBeacon("basic", "https://idp/token", nil) != nil {
		t.Error("basic mode must yield no oauth2 descriptor")
	}
	if OAuth2ForBeacon("", "https://idp/token", nil) != nil {
		t.Error("empty mode must yield no oauth2 descriptor")
	}
	got := OAuth2ForBeacon("oauth2", "https://idp/token", []string{"s1"})
	if got == nil {
		t.Fatal("oauth2 mode must yield a descriptor")
	}
	if got.TokenURL != "https://idp/token" || got.ClientIDEnv != DefaultClientIDEnv || got.ClientSecretEnv != DefaultClientSecretEnv {
		t.Errorf("descriptor not built from defaults + args: %+v", got)
	}
}
