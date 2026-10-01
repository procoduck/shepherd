package wizard_test

import (
	"os"
	"strings"
	"testing"

	"shepherd/internal/validate"
	"shepherd/internal/wizard"
	"shepherd/internal/wizard/wizardtest"
)

// writerGoldenCases is every writer kind × auth mode RenderWriter supports
// (#229). Each golden is byte-compared here and run through the real pinned
// Alloy — Stage 1 and Stage 2, port shapes included — by
// TestWriterGoldensAgainstRealAlloy.
var writerGoldenCases = []struct {
	golden string
	kind   wizard.WriterKind
	label  string
	dest   wizard.Destination
}{
	{"writer-prometheus-none", wizard.WriterPrometheus, "metrics", wizard.Destination{
		Name: "prom-prod", Type: "prometheus", URL: "https://mimir.example.com/api/v1/push", AuthMode: wizard.AuthNone,
	}},
	{"writer-prometheus-basic", wizard.WriterPrometheus, "metrics", wizard.Destination{
		Name: "prom-prod", Type: "prometheus", URL: "https://mimir.example.com/api/v1/push", AuthMode: wizard.AuthBasicSecret,
		SecretNamespace: "monitoring", SecretName: "mimir-credentials",
	}},
	{"writer-prometheus-oauth2", wizard.WriterPrometheus, "metrics", wizard.Destination{
		Name: "prom-prod", Type: "prometheus", URL: "https://mimir.example.com/api/v1/push", AuthMode: wizard.AuthOAuth2Secret,
		SecretNamespace: "monitoring", SecretName: "mimir-oauth",
	}},
	{"writer-loki-none", wizard.WriterLoki, "logs", wizard.Destination{
		Name: "loki-prod", Type: "loki", URL: "https://loki.example.com/loki/api/v1/push", AuthMode: wizard.AuthNone,
	}},
	{"writer-loki-basic", wizard.WriterLoki, "logs", wizard.Destination{
		Name: "loki-prod", Type: "loki", URL: "https://loki.example.com/loki/api/v1/push", AuthMode: wizard.AuthBasicSecret,
		SecretNamespace: "monitoring", SecretName: "loki-credentials",
	}},
	{"writer-loki-oauth2-scopes", wizard.WriterLoki, "logs", wizard.Destination{
		Name: "loki-prod", Type: "loki", URL: "https://loki.example.com/loki/api/v1/push", AuthMode: wizard.AuthOAuth2Secret,
		SecretNamespace: "monitoring", SecretName: "loki-oauth", OAuth2Scopes: []string{"api://loki/.default", "logs.write"},
	}},
}

func TestWriterGoldens(t *testing.T) {
	for _, tc := range writerGoldenCases {
		t.Run(tc.golden, func(t *testing.T) {
			got, err := wizard.RenderWriter(tc.kind, tc.label, wizard.Destinations{tc.dest.Name: tc.dest}, tc.dest.Name)
			if err != nil {
				t.Fatalf("RenderWriter: %v", err)
			}
			path := "testdata/" + tc.golden + ".golden.alloy"
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("golden %s is missing — it must be committed: %v", path, err)
			}
			if got != string(want) {
				t.Fatalf("RenderWriter output differs from %s:\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
			}
			if r := validate.Stage1(got); !r.Valid {
				t.Fatalf("Stage 1 refused %s: %v", path, r.Diagnostics)
			}
		})
	}
}

// TestWriterGoldensAgainstRealAlloy is the gate check: every writer golden,
// auth blocks and remote.kubernetes.secret included, is accepted by the real
// pinned Alloy (Stage 2, which also runs the port-shape check).
func TestWriterGoldensAgainstRealAlloy(t *testing.T) {
	wizardtest.AssertGoldensAgainstRealAlloy(t, "testdata")
}

// TestRenderWriterAuthShape pins the security property of the contract
// rather than the bytes: a Secret mode references the Secret by namespace/name
// and its keys only, a credential value can never be in the output (there is
// no field to carry one), and AuthNone emits no Secret read at all.
func TestRenderWriterAuthShape(t *testing.T) {
	for _, tc := range writerGoldenCases {
		t.Run(tc.golden, func(t *testing.T) {
			got, err := wizard.RenderWriter(tc.kind, tc.label, wizard.Destinations{tc.dest.Name: tc.dest}, tc.dest.Name)
			if err != nil {
				t.Fatalf("RenderWriter: %v", err)
			}
			keys, err := wizard.SecretKeys(tc.dest.AuthMode)
			if err != nil {
				t.Fatal(err)
			}
			if tc.dest.AuthMode == wizard.AuthNone {
				if strings.Contains(got, "remote.kubernetes.secret") || strings.Contains(got, "basic_auth") || strings.Contains(got, "oauth2") {
					t.Fatalf("auth_mode none rendered an auth construct:\n%s", got)
				}
				return
			}
			for _, want := range []string{
				`remote.kubernetes.secret "` + tc.label + `_auth"`,
				`namespace = "` + tc.dest.SecretNamespace + `"`,
				`name      = "` + tc.dest.SecretName + `"`,
			} {
				if !strings.Contains(got, want) {
					t.Fatalf("missing %q in:\n%s", want, got)
				}
			}
			for _, k := range keys {
				if !strings.Contains(got, `.data["`+k+`"]`) {
					t.Fatalf("Secret key %q is not read in:\n%s", k, got)
				}
			}
		})
	}
}

func TestSecretKeysContract(t *testing.T) {
	cases := map[wizard.AuthMode][]string{
		wizard.AuthNone:         nil,
		wizard.AuthBasicSecret:  {"username", "password"},
		wizard.AuthOAuth2Secret: {"client_id", "client_secret", "token_url"},
	}
	for mode, want := range cases {
		got, err := wizard.SecretKeys(mode)
		if err != nil {
			t.Fatalf("SecretKeys(%s): %v", mode, err)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("SecretKeys(%s) = %v, want %v", mode, got, want)
		}
	}
	if _, err := wizard.SecretKeys("bearer_secret"); err == nil {
		t.Fatal("an unknown auth mode must be refused, not rendered without auth")
	}
}

func TestRenderWriterRefusals(t *testing.T) {
	ok := wizard.Destination{
		Name: "d", Type: "prometheus", URL: "https://mimir.example.com/api/v1/push", AuthMode: wizard.AuthBasicSecret,
		SecretNamespace: "monitoring", SecretName: "creds",
	}
	with := func(f func(*wizard.Destination)) wizard.Destinations {
		d := ok
		f(&d)
		return wizard.Destinations{d.Name: d}
	}
	cases := []struct {
		name    string
		kind    wizard.WriterKind
		dests   wizard.Destinations
		dest    string
		wantMsg string
	}{
		{"unknown destination", wizard.WriterPrometheus, wizard.Destinations{}, "nope", `"nope" does not exist`},
		{"wrong type", wizard.WriterLoki, with(func(*wizard.Destination) {}), "d", "needs a loki destination"},
		{"no url", wizard.WriterPrometheus, with(func(d *wizard.Destination) { d.URL = "" }), "d", "not an absolute"},
		{"javascript url", wizard.WriterPrometheus, with(func(d *wizard.Destination) { d.URL = "javascript:alert(1)" }), "d", "not an absolute"},
		{"relative url", wizard.WriterPrometheus, with(func(d *wizard.Destination) { d.URL = "/api/v1/push" }), "d", "not an absolute"},
		{"unknown auth mode", wizard.WriterPrometheus, with(func(d *wizard.Destination) { d.AuthMode = "bearer_secret" }), "d", "unknown auth_mode"},
		{"secret mode without namespace", wizard.WriterPrometheus, with(func(d *wizard.Destination) { d.SecretNamespace = "" }), "d", "secret_namespace"},
		{"secret mode without name", wizard.WriterPrometheus, with(func(d *wizard.Destination) { d.SecretName = "" }), "d", "secret_name"},
		{"secret name injection", wizard.WriterPrometheus, with(func(d *wizard.Destination) { d.SecretName = `x" }` }), "d", "secret_name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := wizard.RenderWriter(tc.kind, "metrics", tc.dests, tc.dest)
			if err == nil {
				t.Fatalf("RenderWriter accepted it and rendered:\n%s", got)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("error %q does not mention %q", err, tc.wantMsg)
			}
		})
	}
}

// TestRenderWriterQuotesURL: a URL is operator text, so a quote in it must
// stay inside the string literal rather than end it and inject an attribute.
func TestRenderWriterQuotesURL(t *testing.T) {
	d := wizard.Destination{
		Name: "d", Type: "prometheus", AuthMode: wizard.AuthNone,
		URL: `https://mimir.example.com/push?x="} remote_timeout = "1s`,
	}
	got, err := wizard.RenderWriter(wizard.WriterPrometheus, "metrics", wizard.Destinations{"d": d}, "d")
	if err != nil {
		t.Fatalf("RenderWriter: %v", err)
	}
	if r := validate.Stage1(got); !r.Valid {
		t.Fatalf("a quoted URL broke Stage 1: %v\n%s", r.Diagnostics, got)
	}
	if !strings.Contains(got, `url  = "https://mimir.example.com/push?x=\"} remote_timeout = \"1s"`) {
		t.Fatalf("the URL was not escaped into one literal:\n%s", got)
	}
}
