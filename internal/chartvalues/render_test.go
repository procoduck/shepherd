package chartvalues_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"shepherd/internal/chartvalues"
)

func TestRenderGoldens(t *testing.T) {
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			out, err := chartvalues.Render(fixture(name))
			if err != nil {
				t.Fatalf("render: %v", err)
			}

			goldenPath := "testdata/golden/" + name + ".values.yaml"
			want, readErr := os.ReadFile(goldenPath)
			// A missing golden must fail, never self-heal — writing current
			// output as the baseline would make the comparison prove
			// nothing (internal/receiver/render_test.go's own convention).
			// Regenerate with: GEN_GOLDENS=1 go test ./internal/chartvalues/ -run TestGenGoldens
			if readErr != nil {
				t.Fatalf("reading golden %s: %v (run GEN_GOLDENS=1 go test -run TestGenGoldens to create it, then review the diff)", goldenPath, readErr)
			}
			if !bytes.Equal(out, want) {
				t.Errorf("%s: output does not match golden %s\n--- got ---\n%s\n--- want ---\n%s", name, goldenPath, out, want)
			}
		})
	}
}

// TestRenderDeterministicRoleOrder proves Render's output does not depend on
// the order Spec.Roles was given in — the "all-roles" fixture deliberately
// lists roles out of canonical order (fixtures_test.go), so a byte-identical
// golden match already covers this for that one fixture; this test makes
// the property explicit and independent of the golden file ever changing.
func TestRenderDeterministicRoleOrder(t *testing.T) {
	base := chartvalues.Spec{
		ClusterName: "c",
		ShepherdURL: "https://shepherd.example.com",
		Roles:       []string{"metrics", "logs", "receiver", "singleton"},
	}
	shuffled := base
	shuffled.Roles = []string{"singleton", "receiver", "logs", "metrics"}

	a, err := chartvalues.Render(base)
	if err != nil {
		t.Fatalf("render base: %v", err)
	}
	b, err := chartvalues.Render(shuffled)
	if err != nil {
		t.Fatalf("render shuffled: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("Render output depends on Spec.Roles input order:\n--- base ---\n%s\n--- shuffled ---\n%s", a, b)
	}
}

// TestRenderNoSecretInOutput is the render-side half of doc.go's "no secret
// is ever in this file" claim: the two credential fields are always the
// sys.env(...) indirection, never a literal. A future change that starts
// plumbing a literal password through Spec would trip this immediately.
func TestRenderNoSecretInOutput(t *testing.T) {
	out, err := chartvalues.Render(fixture("all-roles"))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `usernameFrom: 'sys.env("SHEPHERD_AGENT_TOKEN_ID")'`) {
		t.Errorf("expected usernameFrom to be the sys.env(...) indirection; got:\n%s", s)
	}
	if !strings.Contains(s, `passwordFrom: 'sys.env("SHEPHERD_AGENT_TOKEN_SECRET")'`) {
		t.Errorf("expected passwordFrom to be the sys.env(...) indirection; got:\n%s", s)
	}
	if strings.Contains(s, "password:") || strings.Contains(s, "username:") {
		t.Errorf("output contains a literal username/password key, which Render must never emit:\n%s", s)
	}
}

func TestValidate(t *testing.T) {
	valid := chartvalues.Spec{
		ClusterName: "c",
		ShepherdURL: "https://shepherd.example.com",
		Roles:       []string{"metrics"},
	}

	for _, tc := range []struct {
		name    string
		mutate  func(chartvalues.Spec) chartvalues.Spec
		wantErr bool
		wantIn  string
	}{
		{name: "valid as-is", mutate: func(s chartvalues.Spec) chartvalues.Spec { return s }},
		{
			name:    "empty cluster name",
			mutate:  func(s chartvalues.Spec) chartvalues.Spec { s.ClusterName = ""; return s },
			wantErr: true, wantIn: "ClusterName",
		},
		{
			name:    "cluster name with a quote",
			mutate:  func(s chartvalues.Spec) chartvalues.Spec { s.ClusterName = `bad"name`; return s },
			wantErr: true, wantIn: "ClusterName",
		},
		{
			name:    "missing shepherd URL",
			mutate:  func(s chartvalues.Spec) chartvalues.Spec { s.ShepherdURL = ""; return s },
			wantErr: true, wantIn: "ShepherdURL",
		},
		{
			name:    "shepherd URL not http(s)",
			mutate:  func(s chartvalues.Spec) chartvalues.Spec { s.ShepherdURL = "ftp://shepherd.example.com"; return s },
			wantErr: true, wantIn: "http(s)",
		},
		{
			name:    "shepherd URL with no host",
			mutate:  func(s chartvalues.Spec) chartvalues.Spec { s.ShepherdURL = "https:///path"; return s },
			wantErr: true, wantIn: "no host",
		},
		{
			name:    "no roles",
			mutate:  func(s chartvalues.Spec) chartvalues.Spec { s.Roles = nil; return s },
			wantErr: true, wantIn: "at least one Role",
		},
		{
			name:    "unknown role",
			mutate:  func(s chartvalues.Spec) chartvalues.Spec { s.Roles = []string{"profiles"}; return s },
			wantErr: true, wantIn: "not a recognized collector role",
		},
		{
			name:    "duplicate role",
			mutate:  func(s chartvalues.Spec) chartvalues.Spec { s.Roles = []string{"metrics", "metrics"}; return s },
			wantErr: true, wantIn: "repeated",
		},
		{
			name:    "tenant with a backslash",
			mutate:  func(s chartvalues.Spec) chartvalues.Spec { s.Tenant = `a\b`; return s },
			wantErr: true, wantIn: "Tenant",
		},
		{
			name:    "malformed poll frequency",
			mutate:  func(s chartvalues.Spec) chartvalues.Spec { s.PollFrequency = "soon"; return s },
			wantErr: true, wantIn: "PollFrequency",
		},
		{
			name:    "negative poll frequency",
			mutate:  func(s chartvalues.Spec) chartvalues.Spec { s.PollFrequency = "-1s"; return s },
			wantErr: true, wantIn: "positive",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := chartvalues.Validate(tc.mutate(valid))
			if tc.wantErr && err == nil {
				t.Fatalf("expected an error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if tc.wantErr && !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantIn)
			}
		})
	}
}

// TestRenderRefusesInvalidSpec proves Render itself refuses rather than
// rendering a partial/garbage file when Validate would fail — Render calls
// Validate first, but this pins that call staying there against a future
// refactor that reorders it.
func TestRenderRefusesInvalidSpec(t *testing.T) {
	_, err := chartvalues.Render(chartvalues.Spec{})
	if err == nil {
		t.Fatal("expected Render to refuse an empty Spec, got nil error")
	}
}
