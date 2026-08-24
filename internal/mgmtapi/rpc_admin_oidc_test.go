package mgmtapi_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/mgmtapi"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Connect-path coverage for AdminService's OIDC settings procedures.
//
// The auth package's own suite proves the settings LOGIC (precedence,
// validation, encryption, the SSRF guard). This suite exists for the things
// only the RPC boundary can be wrong about: who is allowed through, what the
// wire response is allowed to contain, and which auth-package sentinel maps to
// which Connect code. The React page's Playwright specs cannot stand in for
// any of that — they run against hand-written mocks, so they can only prove
// the form renders what a mock returns.
var _ = Describe("shepherd.mgmt.v1 AdminService OIDC settings RPC", Label("integration"), func() {
	const (
		oidcTestSecret = "super-secret-client-value-9f3a"
		procGet        = "/shepherd.mgmt.v1.AdminService/GetOidcSettings"
		procUpdate     = "/shepherd.mgmt.v1.AdminService/UpdateOidcSettings"
		procDelete     = "/shepherd.mgmt.v1.AdminService/DeleteOidcSettings"
		procPresets    = "/shepherd.mgmt.v1.AdminService/ListOidcProviderPresets"
	)

	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		enc         *crypto.Encryptor
		server      *httptest.Server
		adminCookie *http.Cookie
	)

	// newOIDCRouter mirrors production wiring: session + CSRF middleware, then
	// MountRPC WITH the auth handler supplied — the same WithOIDCSettings call
	// internal/server/server.go makes.
	newOIDCRouter := func(cfg *config.Config, h *auth.Handler, withOIDC bool) http.Handler {
		r := chi.NewRouter()
		r.Group(func(r chi.Router) {
			r.Use(h.SessionMiddleware)
			r.Use(auth.CSRFMiddleware)
			if withOIDC {
				mgmtapi.MountRPC(r, st, cfg, enc, slog.Default(), mgmtapi.WithOIDCSettings(h))
			} else {
				mgmtapi.MountRPC(r, st, cfg, enc, slog.Default())
			}
		})
		return r
	}

	// validUpdate is a complete, well-formed settings submission. enabled is
	// false throughout this suite on purpose: enabling runs live OIDC
	// discovery, which has no place in a hermetic test.
	validUpdate := func() map[string]any {
		return map[string]any{
			"enabled":        false,
			"provider":       "okta",
			"displayName":    "Okta",
			"issuer":         "https://acme.okta.com/oauth2/default",
			"clientId":       "shepherd-client",
			"clientSecret":   oidcTestSecret,
			"redirectUrl":    "https://shepherd.example/auth/callback",
			"scopes":         []string{"openid", "profile", "email", "groups"},
			"subjectClaim":   "sub",
			"emailClaim":     "email",
			"nameClaim":      "name",
			"groupsClaim":    "groups",
			"appAdminGroups": []string{"platform-admins"},
			"useGraphGroups": false,
			"graphBaseUrl":   "https://graph.microsoft.com",
		}
	}

	// expectConnectError asserts on the Connect error CODE, not the HTTP
	// status. The Connect protocol collapses several codes onto HTTP 400
	// (invalid_argument and failed_precondition both land there), so the status
	// alone cannot distinguish "your input was wrong" from "this deployment
	// will not accept this write at all" — and the code is what the client
	// actually reads (web/src/api/transport.ts's toApiError).
	expectConnectError := func(resp *http.Response, wantCode string) map[string]any {
		GinkgoHelper()
		raw, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Body.Close()).To(Succeed())
		var body map[string]any
		Expect(json.Unmarshal(raw, &body)).To(Succeed(), "body: %s", raw)
		Expect(body["code"]).To(Equal(wantCode), "body: %s", raw)
		return body
	}

	decode := func(resp *http.Response) (map[string]any, string) {
		raw, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Body.Close()).To(Succeed())
		var out map[string]any
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &out) //nolint:errcheck // body asserted by the caller
		}
		return out, string(raw)
	}

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		key := make([]byte, 32)
		_, err = rand.Read(key)
		Expect(err).NotTo(HaveOccurred())
		enc, err = crypto.NewEncryptor(base64.StdEncoding.EncodeToString(key))
		Expect(err).NotTo(HaveOccurred())

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		h, err := auth.New(ctx, cfg, st, enc, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		server = httptest.NewServer(newOIDCRouter(cfg, h, true))
		adminCookie = newAppAdminSession(ctx, st)
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	It("returns coherent defaults before anything is configured", func() {
		resp := postJSON(server, procGet, map[string]any{}, adminCookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		body, _ := decode(resp)

		Expect(body["configured"]).To(BeNil(), "proto3 JSON omits a false bool")
		Expect(body["editable"]).To(BeTrue())
		Expect(body["source"]).To(Equal("database"))
		Expect(body["subjectClaim"]).To(Equal("sub"))
	})

	It("never puts the client secret on the wire", func() {
		// The single most important assertion in this file: the secret is
		// stored encrypted and the API answers clientSecretSet instead. Scanned
		// against the RAW body, not the decoded map, so an accidental new proto
		// field carrying it would still be caught.
		resp := postJSON(server, procUpdate, validUpdate(), adminCookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		body, raw := decode(resp)
		Expect(raw).NotTo(ContainSubstring(oidcTestSecret), "UpdateOidcSettings echoed the client secret")
		Expect(body["clientSecretSet"]).To(BeTrue())

		resp = postJSON(server, procGet, map[string]any{}, adminCookie)
		_, raw = decode(resp)
		Expect(raw).NotTo(ContainSubstring(oidcTestSecret), "GetOidcSettings returned the client secret")

		// And it is not sitting in the column in the clear either.
		row, err := st.Queries.GetOIDCSettings(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(row.ClientSecretEnc)).NotTo(ContainSubstring(oidcTestSecret))
	})

	It("keeps the stored secret when a save omits it", func() {
		Expect(postJSON(server, procUpdate, validUpdate(), adminCookie).StatusCode).To(Equal(http.StatusOK))

		// The form never has the secret to send back, so a blank one must mean
		// "unchanged" — otherwise editing any other field would erase it.
		update := validUpdate()
		update["clientSecret"] = ""
		update["displayName"] = "Okta (production)"
		resp := postJSON(server, procUpdate, update, adminCookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		body, _ := decode(resp)
		Expect(body["clientSecretSet"]).To(BeTrue())
		Expect(body["displayName"]).To(Equal("Okta (production)"))

		stored, err := auth.NewSettingsStore(st, enc).Get(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.ClientSecret).To(Equal(oidcTestSecret), "the stored secret was replaced by the blank one")
	})

	It("refuses a blank secret when there is nothing stored to keep", func() {
		update := validUpdate()
		update["clientSecret"] = ""
		expectConnectError(postJSON(server, procUpdate, update, adminCookie), "invalid_argument")
	})

	It("maps a validation failure to invalid_argument, not internal", func() {
		// The settings form renders this message against the field, so the code
		// has to be the one the client treats as "your input was wrong".
		update := validUpdate()
		update["issuer"] = "http://acme.okta.com"
		body := expectConnectError(postJSON(server, procUpdate, update, adminCookie), "invalid_argument")
		Expect(body["message"]).To(ContainSubstring("https"))
	})

	It("deletes a stored configuration", func() {
		Expect(postJSON(server, procUpdate, validUpdate(), adminCookie).StatusCode).To(Equal(http.StatusOK))
		Expect(postJSON(server, procDelete, map[string]any{}, adminCookie).StatusCode).To(Equal(http.StatusOK))

		_, err := auth.NewSettingsStore(st, enc).Get(ctx)
		Expect(err).To(MatchError(auth.ErrNoSettings))
	})

	It("serves the provider catalogue", func() {
		resp := postJSON(server, procPresets, map[string]any{}, adminCookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		_, raw := decode(resp)
		for _, key := range []string{"entra", "okta", "keycloak", "cognito", "generic"} {
			Expect(raw).To(ContainSubstring(`"`+key+`"`), "preset %q missing from the catalogue", key)
		}
	})

	Describe("authorization", func() {
		It("denies every OIDC procedure to a non-app-admin session", func() {
			// This configuration decides who can hold an app-admin session at
			// all, so there is deliberately no org-scoped variant to fall back
			// to — an org admin is simply refused.
			cookie := newNonAdminSession(ctx, st)
			for _, proc := range []string{procGet, procUpdate, procDelete, procPresets} {
				expectConnectError(postJSON(server, proc, map[string]any{}, cookie), "permission_denied")
			}
		})

		It("denies every OIDC procedure to an unauthenticated caller", func() {
			for _, proc := range []string{procGet, procUpdate, procDelete, procPresets} {
				expectConnectError(postJSON(server, proc, map[string]any{}, nil), "unauthenticated")
			}
		})
	})

	Describe("chart-managed deployments", func() {
		var helmServer *httptest.Server

		BeforeEach(func() {
			// NewLocalAdmin, not New: it builds the same handler without
			// running discovery, which is what lets this exercise the
			// chart-managed branch against an issuer that does not exist.
			cfg := &config.Config{
				Auth: config.AuthConfig{InsecureCookies: true, AppAdminGroupIDs: []string{"chart-group"}},
				OIDC: config.OIDCConfig{
					Issuer: "https://login.microsoftonline.com/tenant/v2.0", ClientID: "chart-client",
					ClientSecret: oidcTestSecret, RedirectURL: "https://shepherd.example/auth/callback",
					Provider: "entra", SubjectClaim: "oid", GroupsClaim: "groups",
					EmailClaim: "email", NameClaim: "name", UseGraphGroups: true,
				},
				Graph: config.GraphConfig{BaseURL: "https://graph.microsoft.com"},
			}
			h := auth.NewLocalAdmin(cfg, st, slog.Default())
			helmServer = httptest.NewServer(newOIDCRouter(cfg, h, true))
			DeferCleanup(helmServer.Close)
		})

		It("shows the chart's configuration as read-only, without the secret", func() {
			resp := postJSON(helmServer, procGet, map[string]any{}, adminCookie)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			body, raw := decode(resp)

			// Readable: an admin must be able to see which provider their
			// cluster trusts, even where they cannot change it.
			Expect(body["issuer"]).To(Equal("https://login.microsoftonline.com/tenant/v2.0"))
			Expect(body["source"]).To(Equal("helm"))
			Expect(body["editable"]).To(BeNil(), "proto3 JSON omits a false bool")
			Expect(body["statusMessage"]).To(ContainSubstring("Helm chart"))
			Expect(raw).NotTo(ContainSubstring(oidcTestSecret), "the chart's client secret reached the wire")
		})

		It("refuses writes with failed_precondition and audits the refusal", func() {
			body := expectConnectError(postJSON(helmServer, procUpdate, validUpdate(), adminCookie), "failed_precondition")
			Expect(body["message"]).To(ContainSubstring("Helm chart"))

			expectConnectError(postJSON(helmServer, procDelete, map[string]any{}, adminCookie), "failed_precondition")

			// The stored row is untouched — the refusal is real, not cosmetic.
			_, err := auth.NewSettingsStore(st, enc).Get(ctx)
			Expect(err).To(MatchError(auth.ErrNoSettings))

			// Re-pointing the identity provider is the highest-leverage write
			// in the product; an attempt that was REFUSED still has to be
			// visible to an incident review.
			rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Limit: 50})
			Expect(err).NotTo(HaveOccurred())
			var actions []string
			for _, row := range rows {
				actions = append(actions, row.Action)
			}
			Expect(actions).To(ContainElement("oidc_settings.update_denied"))
			Expect(actions).To(ContainElement("oidc_settings.delete_denied"))
		})
	})

	It("answers unavailable when the deployment has no OIDC handler wired", func() {
		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		h := auth.NewLocalAdmin(cfg, st, slog.Default())
		bare := httptest.NewServer(newOIDCRouter(cfg, h, false))
		DeferCleanup(bare.Close)

		expectConnectError(postJSON(bare, procGet, map[string]any{}, adminCookie), "unavailable")
	})
})

// newNonAdminSession creates a session with no app-admin flag and no groups —
// the shape of an ordinary org member.
func newNonAdminSession(ctx context.Context, st *store.Store) *http.Cookie {
	id := "non-admin-sess-" + time.Now().Format("150405.000000000")
	groupsJSON, err := json.Marshal([]string{"some-org-group"})
	Expect(err).NotTo(HaveOccurred())
	_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
		ID: id, UserOid: "user-" + id, Email: id + "@example.com", DisplayName: "Ordinary User",
		GroupIds:   groupsJSON,
		IsAppAdmin: false,
		ExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		Source:     "test",
	})
	Expect(err).NotTo(HaveOccurred())
	return &http.Cookie{Name: "shepherd_session", Value: id}
}

// Regression coverage for a defect a manual walkthrough found and neither the
// unit tests nor the code review did: the OIDC audit rows were being WRITTEN
// correctly and were unreachable in the product. They carry a NULL org_id
// (single sign-on belongs to no org), every caller of ListAuditLog passes an
// org, and `org_id = $1` silently excluded them — so the audit trail for the
// highest-leverage write in the product could only be read with psql.
//
// Asserting "the row exists in audit_log" is what let that ship. These specs
// assert it comes back through the API a human actually reads.
var _ = Describe("platform audit visibility", Label("integration"), func() {
	var (
		ctx      context.Context
		cancel   context.CancelFunc
		st       *store.Store
		server   *httptest.Server
		orgIDStr string
	)

	const procListAudit = "/shepherd.mgmt.v1.AuditService/ListAudit"

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "audit-vis-org", DisplayName: "Audit Vis Org",
			AdminGroupID: "audit-vis-admins", ReaderGroupID: pgtype.Text{},
		})
		Expect(err).NotTo(HaveOccurred())
		orgIDStr = o.ID.String()

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		h := auth.NewLocalAdmin(cfg, st, slog.Default())
		r := chi.NewRouter()
		r.Group(func(r chi.Router) {
			r.Use(h.SessionMiddleware)
			r.Use(auth.CSRFMiddleware)
			mgmtapi.MountRPC(r, st, cfg, nil, slog.Default())
		})
		server = httptest.NewServer(r)

		// A platform event (no org) alongside an ordinary org-scoped one.
		Expect(st.Queries.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
			Actor: "admin@example.com", ActorType: "user", OrgID: pgtype.UUID{},
			Action: "oidc_settings.update", ResourceType: "oidc_settings", ResourceID: "",
			Detail: []byte(`{}`),
		})).To(Succeed())
		Expect(st.Queries.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
			Actor: "admin@example.com", ActorType: "user", OrgID: o.ID,
			Action: "pipeline.create", ResourceType: "pipeline", ResourceID: "p-1",
			Detail: []byte(`{}`),
		})).To(Succeed())
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	listFor := func(cookie *http.Cookie) (string, float64) {
		resp := postJSON(server, procListAudit, map[string]any{"orgId": orgIDStr}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		raw, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Body.Close()).To(Succeed())
		var body map[string]any
		Expect(json.Unmarshal(raw, &body)).To(Succeed())
		total, ok := body["total"].(float64)
		Expect(ok).To(BeTrue(), "response carried no numeric total: %s", raw)
		return string(raw), total
	}

	It("shows an app admin platform events alongside the org's own", func() {
		raw, total := listFor(newAppAdminSession(ctx, st))
		Expect(raw).To(ContainSubstring("oidc_settings.update"),
			"an app admin viewing an org must still see platform-level events, or the SSO audit trail is unreadable in the product")
		Expect(raw).To(ContainSubstring("pipeline.create"))
		// The count labels the same set the page returns.
		Expect(total).To(BeNumerically("==", 2))
	})

	It("does not widen an org admin's view to platform events", func() {
		cookie := newOrgScopedAdminSession(ctx, st, "audit-vis-admins")
		raw, total := listFor(cookie)
		Expect(raw).To(ContainSubstring("pipeline.create"))
		Expect(raw).NotTo(ContainSubstring("oidc_settings.update"),
			"platform configuration is app-admin business; an org admin's trail must not start showing it")
		Expect(total).To(BeNumerically("==", 1))
	})
})

// newOrgScopedAdminSession creates a non-app-admin session that is an admin of
// the org whose admin group is groupID.
func newOrgScopedAdminSession(ctx context.Context, st *store.Store, groupID string) *http.Cookie {
	id := "org-admin-sess-" + time.Now().Format("150405.000000000")
	groupsJSON, err := json.Marshal([]string{groupID})
	Expect(err).NotTo(HaveOccurred())
	_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
		ID: id, UserOid: "user-" + id, Email: id + "@example.com", DisplayName: "Org Admin",
		GroupIds:   groupsJSON,
		IsAppAdmin: false,
		ExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		Source:     "test",
	})
	Expect(err).NotTo(HaveOccurred())
	return &http.Cookie{Name: "shepherd_session", Value: id}
}
