package mgmtapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// #229: a destination's Secret auth mode reaches the pipeline a wizard
// renders for it. Before this, auth_mode/secret_name/secret_namespace were
// stored and returned but every wizard emitted
// `url = sys.env("SHEPHERD_DEST_<NAME>_URL")` and no auth block — a
// basic_secret destination shipped with no credentials at all. Red run: on
// the pre-#229 code the first spec fails on its very first assertion (no
// remote.kubernetes.secret in the committed contents).
var _ = Describe("WizardService renders destination auth (#229)", Label("integration"), func() {
	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		server      *httptest.Server
		orgID       string
		adminCookie *http.Cookie
	)

	createDest := func(org pgtype.UUID, name, typ, url, mode, ns, secret, extra string) {
		_, err := st.Queries.CreateDestination(ctx, sqlc.CreateDestinationParams{
			OrgID: org, Name: name, Type: typ, Url: url, AuthMode: mode,
			SecretNamespace: ns, SecretName: secret, Extra: json.RawMessage(extra),
		})
		Expect(err).NotTo(HaveOccurred())
	}

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "dest-auth-org", DisplayName: "Dest Auth Org", AdminGroupID: "dest-auth-admin-grp",
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()
		other, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "dest-auth-other", DisplayName: "Other", AdminGroupID: "dest-auth-other-grp",
		})
		Expect(err).NotTo(HaveOccurred())

		createDest(o.ID, "mimir-basic", "prometheus", "https://mimir.example.com/api/v1/push",
			"basic_secret", "monitoring", "mimir-credentials", `{}`)
		createDest(o.ID, "loki-oauth", "loki", "https://loki.example.com/loki/api/v1/push",
			"oauth2_secret", "monitoring", "loki-oauth", `{"oauth2_scopes":["api://loki/.default"]}`)
		// Same name, different org: must never resolve for orgID.
		createDest(other.ID, "foreign-mimir", "prometheus", "https://foreign.example.com/api/v1/push", "none", "", "", `{}`)

		adminCookie = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "dest-auth-admin-grp")}
		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		server = httptest.NewServer(newRPCWiringRouter(st, auth.NewLocalAdmin(cfg, st, slog.Default()), cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	call := func(procedure string, body map[string]any) (int, map[string]any) {
		resp := postConnectJSON(server, procedure, adminCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		b, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		var out map[string]any
		Expect(json.Unmarshal(b, &out)).To(Succeed(), string(b))
		return resp.StatusCode, out
	}

	wizardBody := func(name string, state map[string]any) map[string]any {
		return map[string]any{"org_id": orgID, "kind": "self-monitoring", "name": name, "state": state}
	}

	It("commits a basic_secret metrics writer and an oauth2_secret logs writer, preview and stored text identical", func() {
		state := map[string]any{
			"metrics_dest_name": "mimir-basic",
			"logs_enabled":      true,
			"log_path":          "/var/log/alloy/*.log",
			"logs_dest_name":    "loki-oauth",
		}
		code, render := call("/shepherd.mgmt.v1.WizardService/RenderWizard", wizardBody("dest-auth", state))
		Expect(code).To(Equal(http.StatusOK))
		Expect(render["valid"]).To(Or(BeNil(), BeTrue()), "rendered config must pass Stages 1-2: %v", render["diagnostics"])

		code, pipeline := call("/shepherd.mgmt.v1.WizardService/CommitWizard", wizardBody("dest-auth", state))
		Expect(code).To(Equal(http.StatusOK), "%v", pipeline)
		contents, _ := pipeline["contents"].(string) //nolint:errcheck // asserted below
		Expect(contents).To(Equal(render["contents"]), "the preview must be exactly what is stored")

		Expect(contents).To(ContainSubstring(`remote.kubernetes.secret "metrics_auth" {
  namespace = "monitoring"
  name      = "mimir-credentials"
}`))
		Expect(contents).To(ContainSubstring(`url  = "https://mimir.example.com/api/v1/push"`))
		Expect(contents).To(ContainSubstring(`username = convert.nonsensitive(remote.kubernetes.secret.metrics_auth.data["username"])`))
		Expect(contents).To(ContainSubstring(`password = remote.kubernetes.secret.metrics_auth.data["password"]`))
		Expect(contents).To(ContainSubstring(`remote.kubernetes.secret "logs_auth"`))
		Expect(contents).To(ContainSubstring(`client_secret = remote.kubernetes.secret.logs_auth.data["client_secret"]`))
		Expect(contents).To(ContainSubstring(`scopes        = ["api://loki/.default"]`))
		Expect(contents).NotTo(ContainSubstring("sys.env"))
		Expect(contents).NotTo(ContainSubstring("injected by Shepherd"))
	})

	DescribeTable("refuses a destination the wizard cannot render, as failed_precondition naming it",
		func(state map[string]any, wantMsg string) {
			for _, proc := range []string{"RenderWizard", "CommitWizard"} {
				code, body := call("/shepherd.mgmt.v1.WizardService/"+proc, wizardBody("refused", state))
				Expect(code).To(Equal(http.StatusBadRequest), proc)
				Expect(body["code"]).To(Equal("failed_precondition"), proc)
				Expect(body["message"]).To(ContainSubstring(wantMsg), proc)
			}
		},
		Entry("a name the org does not have", map[string]any{"metrics_dest_name": "nope", "logs_enabled": false},
			`"nope" does not exist`),
		Entry("another org's destination", map[string]any{"metrics_dest_name": "foreign-mimir", "logs_enabled": false},
			`"foreign-mimir" does not exist`),
		Entry("a loki destination for the metrics writer", map[string]any{"metrics_dest_name": "loki-oauth", "logs_enabled": false},
			"needs a prometheus destination"),
	)

	// #261 step 1: one destination whose stored extra does not decode (a row
	// written before the API validated it, or by a path that skipped the
	// API) used to fail wizardDestinations for the WHOLE org, so every wizard
	// render and every destination update in it failed. Now only a render
	// that names that destination is refused. Red run: on the old
	// wizardDestinations the first spec fails — RenderWizard for a wizard
	// naming only mimir-basic is refused with failed_precondition
	// `destination "broken": extra.oauth2_scopes must be a list of strings`.
	Describe("a destination whose stored extra does not decode", func() {
		BeforeEach(func() {
			createDest(orgUUID(orgID), "broken", "prometheus", "https://broken.example.com/api/v1/push",
				"none", "", "", `{"oauth2_scopes":"not-a-list"}`)
		})

		It("does not stop wizards that name other destinations, or updates to them", func() {
			state := map[string]any{"metrics_dest_name": "mimir-basic", "logs_enabled": false}
			for _, proc := range []string{"RenderWizard", "CommitWizard"} {
				code, body := call("/shepherd.mgmt.v1.WizardService/"+proc, wizardBody("healthy", state))
				Expect(code).To(Equal(http.StatusOK), "%s: %v", proc, body)
			}

			var id string
			rows, err := st.Queries.ListDestinationsByOrg(ctx, orgUUID(orgID))
			Expect(err).NotTo(HaveOccurred())
			for i := range rows {
				if rows[i].Name == "mimir-basic" {
					id = rows[i].ID.String()
				}
			}
			code, out := call("/shepherd.mgmt.v1.DestinationService/UpdateDestination", map[string]any{
				"orgId": orgID, "id": id, "name": "mimir-basic", "type": "prometheus",
				"url": "https://mimir2.example.com/api/v1/push", "authMode": "basic_secret",
				"secretNamespace": "monitoring", "secretName": "mimir-credentials",
			})
			Expect(code).To(Equal(http.StatusOK), "%v", out)
			p, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: orgUUID(orgID), Name: "healthy"})
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Contents).To(ContainSubstring(`url  = "https://mimir2.example.com/api/v1/push"`))
		})

		It("refuses a wizard that names it, as failed_precondition naming it", func() {
			state := map[string]any{"metrics_dest_name": "broken", "logs_enabled": false}
			for _, proc := range []string{"RenderWizard", "CommitWizard"} {
				code, body := call("/shepherd.mgmt.v1.WizardService/"+proc, wizardBody("named-broken", state))
				Expect(code).To(Equal(http.StatusBadRequest), proc)
				Expect(body["code"]).To(Equal("failed_precondition"), proc)
				Expect(body["message"]).To(ContainSubstring(`destination "broken"`), proc)
				Expect(body["message"]).To(ContainSubstring("oauth2_scopes"), proc)
			}
		})
	})

	DescribeTable("CreateDestination refuses an auth setting no writer could render, as invalid_argument",
		func(fields map[string]any, wantMsg string) {
			body := map[string]any{"orgId": orgID, "name": "bad", "type": "prometheus", "url": "https://m.example.com/api/v1/push"}
			for k, v := range fields {
				body[k] = v
			}
			code, out := call("/shepherd.mgmt.v1.DestinationService/CreateDestination", body)
			Expect(code).To(Equal(http.StatusBadRequest))
			Expect(out["code"]).To(Equal("invalid_argument"))
			Expect(out["message"]).To(ContainSubstring(wantMsg))
		},
		Entry("unknown auth mode", map[string]any{"authMode": "bearer_secret"}, "unknown auth_mode"),
		Entry("basic_secret without a Secret name", map[string]any{"authMode": "basic_secret", "secretNamespace": "monitoring"}, "secret_name"),
		Entry("oauth2_secret without a namespace", map[string]any{"authMode": "oauth2_secret", "secretName": "creds"}, "secret_namespace"),
		Entry("scopes that are not a list of strings", map[string]any{
			"authMode": "oauth2_secret", "secretNamespace": "monitoring", "secretName": "creds",
			"extra": map[string]any{"oauth2_scopes": "a b"},
		}, "oauth2_scopes"),
		Entry("a tenant outside Mimir's charset (#261)", map[string]any{"authMode": "none", "tenantId": "acme/prod"}, "tenant_id"),
		Entry("a reserved tenant (#261)", map[string]any{"authMode": "none", "tenantId": "__mimir_cluster"}, "tenant_id"),
		Entry("insecure_skip_verify, which is not offered (#261)", map[string]any{
			"authMode": "none", "extra": map[string]any{"tls": map[string]any{"insecure_skip_verify": true}},
		}, "insecure_skip_verify"),
		Entry("TLS options on an http:// URL (#261)", map[string]any{
			"authMode": "none", "url": "http://m.example.com/api/v1/push",
			"extra": map[string]any{"tls": map[string]any{"server_name": "m.example.com"}},
		}, "https://"),
		Entry("a client certificate in a ConfigMap (#261)", map[string]any{
			"authMode": "none",
			"extra":    map[string]any{"tls": map[string]any{"client_cert": map[string]any{"kind": "configmap", "namespace": "m", "name": "c"}}},
		}, "client_cert"),
		Entry("an illegal CA key (#261)", map[string]any{
			"authMode": "none",
			"extra": map[string]any{"tls": map[string]any{"ca": map[string]any{
				"kind": "configmap", "namespace": "m", "name": "ca", "key": "../ca.crt",
			}}},
		}, "ca.key"),
	)

	// #261: a destination's extra.tls reaches the committed writer, the CA
	// read from a ConfigMap under an overridden key and the client
	// certificate from the auth Secret (one read of it). Red run: before
	// #261 the contents carry no tls_config.
	It("commits a writer with the destination's TLS options", func() {
		code, out := call("/shepherd.mgmt.v1.DestinationService/CreateDestination", map[string]any{
			"orgId": orgID, "name": "mimir-mtls", "type": "prometheus", "url": "https://mimir.example.com/api/v1/push",
			"authMode": "basic_secret", "secretNamespace": "monitoring", "secretName": "mimir-credentials",
			"extra": map[string]any{"tls": map[string]any{
				"ca":          map[string]any{"kind": "configmap", "namespace": "monitoring", "name": "org-trust", "key": "trust-bundle.pem"},
				"client_cert": map[string]any{"namespace": "monitoring", "name": "mimir-credentials"},
				"server_name": "mimir.internal.example",
			}},
		})
		Expect(code).To(Equal(http.StatusOK), "%v", out)

		code, pipeline := call("/shepherd.mgmt.v1.WizardService/CommitWizard",
			wizardBody("tls", map[string]any{"metrics_dest_name": "mimir-mtls", "logs_enabled": false}))
		Expect(code).To(Equal(http.StatusOK), "%v", pipeline)
		contents, _ := pipeline["contents"].(string) //nolint:errcheck // asserted below
		Expect(contents).To(ContainSubstring(`remote.kubernetes.configmap "metrics_tls_ca"`))
		Expect(contents).To(ContainSubstring(`ca_pem      = remote.kubernetes.configmap.metrics_tls_ca.data["trust-bundle.pem"]`))
		Expect(contents).To(ContainSubstring(`cert_pem    = convert.nonsensitive(remote.kubernetes.secret.metrics_auth.data["tls.crt"])`))
		Expect(contents).To(ContainSubstring(`key_pem     = remote.kubernetes.secret.metrics_auth.data["tls.key"]`))
		Expect(contents).To(ContainSubstring(`server_name = "mimir.internal.example"`))
		Expect(strings.Count(contents, `remote.kubernetes.secret "`)).To(Equal(1), "the auth Secret is read once")
	})

	It("UpdateDestination applies the same refusal", func() {
		var id string
		rows, err := st.Queries.ListDestinationsByOrg(ctx, orgUUID(orgID))
		Expect(err).NotTo(HaveOccurred())
		for i := range rows {
			if rows[i].Name == "mimir-basic" {
				id = rows[i].ID.String()
			}
		}
		code, out := call("/shepherd.mgmt.v1.DestinationService/UpdateDestination", map[string]any{
			"orgId": orgID, "id": id, "name": "mimir-basic", "type": "prometheus",
			"url": "https://mimir.example.com/api/v1/push", "authMode": "basic_secret", "secretNamespace": "monitoring",
		})
		Expect(code).To(Equal(http.StatusBadRequest))
		Expect(out["code"]).To(Equal("invalid_argument"))
	})
})
