package wizard_test

import (
	"errors"
	"os"
	"regexp"
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
	// #261: the destination's tenant_id.
	{"writer-prometheus-basic-tenant", wizard.WriterPrometheus, "metrics", wizard.Destination{
		Name: "prom-prod", Type: "prometheus", URL: "https://mimir.example.com/api/v1/push", AuthMode: wizard.AuthBasicSecret,
		SecretNamespace: "monitoring", SecretName: "mimir-credentials", TenantID: "acme",
	}},
	{"writer-loki-tenant", wizard.WriterLoki, "logs", wizard.Destination{
		Name: "loki-prod", Type: "loki", URL: "https://loki.example.com/loki/api/v1/push", AuthMode: wizard.AuthNone,
		TenantID: "acme",
	}},
	// #261: TLS from Secrets/ConfigMaps on the spoke.
	{"writer-prometheus-tls-ca-configmap", wizard.WriterPrometheus, "metrics", wizard.Destination{
		Name: "prom-prod", Type: "prometheus", URL: "https://mimir.example.com/api/v1/push", AuthMode: wizard.AuthNone,
		TLS: &wizard.TLS{
			CA:         &wizard.TLSCA{Kind: wizard.ObjectConfigMap, Namespace: "monitoring", Name: "backend-ca"},
			ServerName: "mimir.internal.example",
		},
	}},
	{"writer-loki-tls-ca-key-override", wizard.WriterLoki, "logs", wizard.Destination{
		Name: "loki-prod", Type: "loki", URL: "https://loki.example.com/loki/api/v1/push", AuthMode: wizard.AuthNone,
		TenantID: "acme",
		TLS: &wizard.TLS{
			CA: &wizard.TLSCA{Kind: wizard.ObjectConfigMap, Namespace: "cert-manager", Name: "org-trust", Key: "trust-bundle.pem"},
		},
	}},
	{"writer-prometheus-mtls-cert-manager", wizard.WriterPrometheus, "metrics", wizard.Destination{
		Name: "prom-prod", Type: "prometheus", URL: "https://mimir.example.com/api/v1/push", AuthMode: wizard.AuthBasicSecret,
		SecretNamespace: "monitoring", SecretName: "mimir-credentials", TenantID: "acme",
		TLS: &wizard.TLS{
			CA:         &wizard.TLSCA{Kind: wizard.ObjectSecret, Namespace: "monitoring", Name: "collector-mtls"},
			ClientCert: &wizard.TLSClientCert{Namespace: "monitoring", Name: "collector-mtls"},
			ServerName: "mimir.internal.example",
		},
	}},
	{"writer-loki-mtls-shared-auth-secret", wizard.WriterLoki, "logs", wizard.Destination{
		Name: "loki-prod", Type: "loki", URL: "https://loki.example.com/loki/api/v1/push", AuthMode: wizard.AuthBasicSecret,
		SecretNamespace: "monitoring", SecretName: "loki-credentials",
		TLS: &wizard.TLS{ClientCert: &wizard.TLSClientCert{Kind: wizard.ObjectSecret, Namespace: "monitoring", Name: "loki-credentials"}},
	}},
}

// TestRenderWriterTLSShape pins the TLS contract (#261) rather than the
// bytes: no PEM-shaped value can reach the output (there is no field to carry
// one), the key is read as a secret and never converted, every reference is
// read through remote.kubernetes.*, and no TLS means no tls_config.
var convertedKeyRE = regexp.MustCompile(`convert\.nonsensitive\([^)]*"tls\.key"`)

func TestRenderWriterTLSShape(t *testing.T) {
	for _, tc := range writerGoldenCases {
		t.Run(tc.golden, func(t *testing.T) {
			got, err := wizard.RenderWriter(tc.kind, tc.label, wizard.Destinations{tc.dest.Name: tc.dest}, tc.dest.Name)
			if err != nil {
				t.Fatalf("RenderWriter: %v", err)
			}
			if tc.dest.TLS.Empty() {
				if strings.Contains(got, "tls_config") || strings.Contains(got, "remote.kubernetes.configmap") {
					t.Fatalf("no TLS, but TLS was rendered:\n%s", got)
				}
				return
			}
			if strings.Contains(got, "BEGIN") || strings.Contains(got, "insecure_skip_verify") {
				t.Fatalf("rendered PEM material or skip-verify:\n%s", got)
			}
			if tls := tc.dest.TLS; tls.ClientCert != nil {
				if !strings.Contains(got, `key_pem     = remote.kubernetes.secret.`) || convertedKeyRE.MatchString(got) {
					t.Fatalf("tls.key must be read from a Secret and never converted:\n%s", got)
				}
			}
			if ca := tc.dest.TLS.CA; ca != nil && !strings.Contains(got, `.data["`+ca.CAKey()+`"]`) {
				t.Fatalf("CA key %q not read:\n%s", ca.CAKey(), got)
			}
		})
	}
}

func TestParseTLS(t *testing.T) {
	ok := []struct{ name, extra string }{
		{"absent", `{}`},
		{"null", `{"tls":null}`},
		{"empty", `{"tls":{}}`},
		{"ca configmap default key", `{"tls":{"ca":{"kind":"configmap","namespace":"monitoring","name":"ca"}}}`},
		{"ca secret with key override", `{"tls":{"ca":{"kind":"secret","namespace":"monitoring","name":"ca","key":"trust-bundle.pem"}}}`},
		{"client cert without kind", `{"tls":{"client_cert":{"namespace":"monitoring","name":"mtls"}}}`},
		{"server name", `{"tls":{"server_name":"Mimir.Internal.example"}}`},
		{"other extra keys untouched", `{"oauth2_scopes":["a"],"owner":"x"}`},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := wizard.ParseTLS([]byte(tc.extra)); err != nil {
				t.Fatalf("ParseTLS(%s): %v", tc.extra, err)
			}
		})
	}
	tlsDefault, err := wizard.ParseTLS([]byte(`{"tls":{"ca":{"kind":"configmap","namespace":"m","name":"ca"}}}`))
	if err != nil || tlsDefault.CA.CAKey() != "ca.crt" {
		t.Fatalf("default CA key: got %+v, %v; want ca.crt", tlsDefault, err)
	}

	bad := []struct{ name, extra, wantMsg string }{
		{"insecure_skip_verify is not offered", `{"tls":{"insecure_skip_verify":true}}`, "insecure_skip_verify"},
		{"unknown key in ca", `{"tls":{"ca":{"kind":"configmap","namespace":"m","name":"ca","file":"/x"}}}`, "file"},
		{"ca kind missing", `{"tls":{"ca":{"namespace":"m","name":"ca"}}}`, "ca.kind"},
		{"ca kind unknown", `{"tls":{"ca":{"kind":"file","namespace":"m","name":"ca"}}}`, "ca.kind"},
		{"ca namespace invalid", `{"tls":{"ca":{"kind":"secret","namespace":"Mon","name":"ca"}}}`, "ca.namespace"},
		{"ca name injection", `{"tls":{"ca":{"kind":"secret","namespace":"m","name":"x\" }"}}}`, "ca.name"},
		{"ca key with slash", `{"tls":{"ca":{"kind":"secret","namespace":"m","name":"ca","key":"a/b"}}}`, "ca.key"},
		{"ca key dot-dot", `{"tls":{"ca":{"kind":"secret","namespace":"m","name":"ca","key":".."}}}`, "ca.key"},
		{"ca key quote", `{"tls":{"ca":{"kind":"secret","namespace":"m","name":"ca","key":"a\"b"}}}`, "ca.key"},
		{"client cert in a configmap", `{"tls":{"client_cert":{"kind":"configmap","namespace":"m","name":"c"}}}`, "client_cert"},
		{"client cert without name", `{"tls":{"client_cert":{"namespace":"m"}}}`, "client_cert.name"},
		{"server name with a path", `{"tls":{"server_name":"mimir/x"}}`, "server_name"},
		{"tls is not an object", `{"tls":"yes"}`, "extra.tls"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := wizard.ParseTLS([]byte(tc.extra))
			if err == nil {
				t.Fatalf("ParseTLS(%s) accepted it", tc.extra)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("error %q does not mention %q", err, tc.wantMsg)
			}
		})
	}
}

// TestRenderWriterTenant pins the tenant contract (#261) rather than the
// bytes: a tenant reaches the writer as the gateway's tenant header (or
// loki.write's tenant_id attribute, which sets that header), and no tenant
// means no tenant construct at all.
func TestRenderWriterTenant(t *testing.T) {
	for _, tc := range writerGoldenCases {
		t.Run(tc.golden, func(t *testing.T) {
			got, err := wizard.RenderWriter(tc.kind, tc.label, wizard.Destinations{tc.dest.Name: tc.dest}, tc.dest.Name)
			if err != nil {
				t.Fatalf("RenderWriter: %v", err)
			}
			if tc.dest.TenantID == "" {
				if strings.Contains(got, "X-Scope-OrgID") || strings.Contains(got, "tenant_id") {
					t.Fatalf("no tenant, but a tenant construct was rendered:\n%s", got)
				}
				return
			}
			want := `"X-Scope-OrgID" = "` + tc.dest.TenantID + `",`
			if tc.kind == wizard.WriterLoki {
				want = `tenant_id = "` + tc.dest.TenantID + `"`
			}
			if !strings.Contains(got, want) {
				t.Fatalf("tenant %q not rendered as %q in:\n%s", tc.dest.TenantID, want, got)
			}
		})
	}
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

// TestWriterGoldensLoadInRealAlloy starts the pinned Alloy image on every
// writer golden and requires the initial load to succeed: remote.kubernetes.secret
// fetches its Secret while being built, which `alloy validate` never does —
// see wizardtest.AssertGoldensLoadInRealAlloy's doc.
func TestWriterGoldensLoadInRealAlloy(t *testing.T) {
	wizardtest.AssertGoldensLoadInRealAlloy(t, "testdata")
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
		{"tenant outside Mimir's charset", wizard.WriterPrometheus, with(func(d *wizard.Destination) { d.TenantID = "acme/prod" }), "d", "tenant_id"},
		{"tenant injection", wizard.WriterLoki, with(func(d *wizard.Destination) {
			d.Type, d.TenantID = "loki", `a" }`
		}), "d", "tenant_id"},
		{"TLS on an http:// URL", wizard.WriterPrometheus, with(func(d *wizard.Destination) {
			d.URL = "http://mimir.example.com/api/v1/push"
			d.TLS = &wizard.TLS{ServerName: "mimir"}
		}), "d", "https://"},
		{"TLS reference invalid", wizard.WriterPrometheus, with(func(d *wizard.Destination) {
			d.TLS = &wizard.TLS{ClientCert: &wizard.TLSClientCert{Namespace: "m", Name: `x" }`}}
		}), "d", "client_cert.name"},
		{"reserved tenant", wizard.WriterPrometheus, with(func(d *wizard.Destination) { d.TenantID = "__mimir_cluster" }), "d", "tenant_id"},
		{"stored row did not load", wizard.WriterPrometheus, with(func(d *wizard.Destination) {
			d.LoadErr = errors.New("extra.oauth2_scopes must be a list of strings")
		}), "d", `destination "d": extra.oauth2_scopes`},
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
