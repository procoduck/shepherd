package chartvalues_test

import (
	"strings"
	"testing"

	"shepherd/internal/chartvalues"
)

func TestRenderCredentialsLayerValidatesAgainstTheChartSchema(t *testing.T) {
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			out, err := chartvalues.RenderCredentialsLayer(fixture(name).Roles, "shepherd-agent-token")
			if err != nil {
				t.Fatal(err)
			}
			if err := chartvalues.ValidateValues(out); err != nil {
				t.Fatalf("credentials layer does not validate against the pinned chart schema: %v\n%s", err, out)
			}
			for _, role := range fixture(name).Roles {
				if !strings.Contains(string(out), "  alloy-"+role+":\n") {
					t.Errorf("no extraEnv for collector alloy-%s:\n%s", role, out)
				}
			}
		})
	}
}

func TestRenderCredentialsLayerNeverCarriesASecretValue(t *testing.T) {
	out, err := chartvalues.RenderCredentialsLayer([]string{"metrics"}, "shepherd-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"secretKeyRef:", `name: "shepherd-agent-token"`, "key: token-id", "key: token-secret"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	// The only literal value is the chart's unused placeholder.
	if n := strings.Count(string(out), "value:"); n != 1 {
		t.Errorf("%d literal value: entries, want exactly 1 (the unused %s):\n%s", n, chartvalues.RequiredOperatorExtraEnvVar, out)
	}
}

func TestRenderCredentialsLayerRefusesBadInput(t *testing.T) {
	for _, tc := range []struct {
		roles  []string
		secret string
	}{
		{[]string{"metrics"}, "Bad_Name"},
		{[]string{"metrics"}, ""},
		{[]string{"nope"}, "ok"},
		{nil, "ok"},
	} {
		if _, err := chartvalues.RenderCredentialsLayer(tc.roles, tc.secret); err == nil {
			t.Errorf("RenderCredentialsLayer(%v, %q) succeeded, want an error", tc.roles, tc.secret)
		}
	}
}
