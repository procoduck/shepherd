package auth_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/store"
)

func testEncryptor() *crypto.Encryptor {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	Expect(err).NotTo(HaveOccurred())
	enc, err := crypto.NewEncryptor(base64.StdEncoding.EncodeToString(key))
	Expect(err).NotTo(HaveOccurred())
	return enc
}

func validSettings() *auth.Settings {
	return &auth.Settings{
		Enabled:        true,
		Provider:       auth.ProviderOkta,
		Issuer:         "https://acme.okta.com/oauth2/default",
		ClientID:       "client-id",
		ClientSecret:   "client-secret",
		RedirectURL:    "https://shepherd.example/auth/callback",
		AppAdminGroups: []string{"platform-admins"},
	}
}

var _ = Describe("Settings.Normalize", func() {
	It("fills claim names, scopes, and the button label from the provider preset", func() {
		s := &auth.Settings{Provider: auth.ProviderCognito, Issuer: "https://cognito-idp.eu-west-1.amazonaws.com/pool"}
		s.Normalize()

		Expect(s.GroupsClaim).To(Equal("cognito:groups"), "Cognito's group claim is not called 'groups'")
		Expect(s.SubjectClaim).To(Equal("sub"))
		Expect(s.DisplayName).To(Equal("AWS Cognito"))
		Expect(s.Scopes).To(ContainElement("openid"))
	})

	It("keeps the Entra subject claim, which is 'oid' and not 'sub'", func() {
		s := &auth.Settings{Provider: auth.ProviderEntra, Issuer: "https://login.microsoftonline.com/t/v2.0"}
		s.Normalize()
		Expect(s.SubjectClaim).To(Equal("oid"))
	})

	It("accepts a pasted discovery-document URL as the issuer", func() {
		// The likeliest wrong value for this field, and the fix is unambiguous.
		s := &auth.Settings{Issuer: "  https://acme.okta.com/oauth2/default/.well-known/openid-configuration  "}
		s.Normalize()
		Expect(s.Issuer).To(Equal("https://acme.okta.com/oauth2/default"))
	})

	It("trims a trailing slash except for Auth0, whose issuers genuinely carry one", func() {
		okta := &auth.Settings{Provider: auth.ProviderOkta, Issuer: "https://acme.okta.com/"}
		okta.Normalize()
		Expect(okta.Issuer).To(Equal("https://acme.okta.com"))

		auth0 := &auth.Settings{Provider: auth.ProviderAuth0, Issuer: "https://acme.us.auth0.com/"}
		auth0.Normalize()
		Expect(auth0.Issuer).To(Equal("https://acme.us.auth0.com/"))
	})

	It("restores a dropped openid scope rather than failing the save over it", func() {
		s := &auth.Settings{Provider: auth.ProviderOkta, Issuer: "https://acme.okta.com", Scopes: []string{"profile", "email"}}
		s.Normalize()
		Expect(s.Scopes).To(Equal([]string{"openid", "profile", "email"}))
	})

	It("de-duplicates and trims the app-admin group list", func() {
		s := &auth.Settings{Provider: auth.ProviderOkta, AppAdminGroups: []string{" admins ", "admins", "", "ops"}}
		s.Normalize()
		Expect(s.AppAdminGroups).To(Equal([]string{"admins", "ops"}))
	})

	It("clears the Microsoft Graph lookup for providers that have no Graph", func() {
		s := &auth.Settings{Provider: auth.ProviderKeycloak, UseGraphGroups: true}
		s.Normalize()
		Expect(s.UseGraphGroups).To(BeFalse(), "Graph is Entra's directory API; leaving this on elsewhere would be a setting that silently does nothing")
	})
})

var _ = Describe("Settings.Validate", func() {
	It("accepts a well-formed configuration", func() {
		s := validSettings()
		s.Normalize()
		Expect(s.Validate()).To(Succeed())
	})

	It("rejects an http issuer", func() {
		s := validSettings()
		s.Issuer = "http://acme.okta.com"
		s.Normalize()
		err := s.Validate()
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("https"))
	})

	It("rejects a redirect URL that does not end at the callback path", func() {
		s := validSettings()
		s.RedirectURL = "https://shepherd.example/"
		s.Normalize()
		err := s.Validate()
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(auth.CallbackPath))
	})

	It("requires a client secret", func() {
		s := validSettings()
		s.ClientSecret = ""
		s.Normalize()
		Expect(s.Validate()).To(MatchError(ContainSubstring("client secret")))
	})

	It("pins the Microsoft Graph base URL to Microsoft's own endpoints", func() {
		// Every signing-in user's delegated access token is sent to this host,
		// so "any https URL" would be an admin-settable token exfiltration
		// endpoint, not a configuration knob.
		s := validSettings()
		s.Provider = auth.ProviderEntra
		s.Issuer = "https://login.microsoftonline.com/tenant/v2.0"
		s.UseGraphGroups = true

		for _, bad := range []string{
			"https://collector.attacker.example",
			"https://graph.microsoft.com.attacker.example",
			"http://graph.microsoft.com",
			"https://graph.microsoft.com/v1.0/me",
			"https://user:pass@graph.microsoft.com",
		} {
			s.GraphBaseURL = bad
			s.Normalize()
			s.UseGraphGroups = true
			Expect(s.Validate()).To(HaveOccurred(), "should reject Graph base URL %q", bad)
		}

		for _, good := range []string{
			"https://graph.microsoft.com",
			"https://graph.microsoft.us",
			"https://microsoftgraph.chinacloudapi.cn",
		} {
			s.GraphBaseURL = good
			s.Normalize()
			s.UseGraphGroups = true
			Expect(s.Validate()).To(Succeed(), "should accept Graph base URL %q", good)
		}
	})

	It("requires the redirect URL path to be exactly the callback path", func() {
		// A suffix test would accept /evil/auth/callback, which can never work
		// and reads as if it might.
		s := validSettings()
		for _, bad := range []string{
			"https://shepherd.example/evil/auth/callback",
			"https://shepherd.example/",
			"https://shepherd.example/auth/callback/extra",
			"https://user:pass@shepherd.example/auth/callback",
		} {
			s.RedirectURL = bad
			s.Normalize()
			Expect(s.Validate()).To(HaveOccurred(), "should reject redirect URL %q", bad)
		}
		for _, good := range []string{
			"https://shepherd.example/auth/callback",
			"https://shepherd.example/auth/callback/",
			"http://localhost:8080/auth/callback",
		} {
			s.RedirectURL = good
			s.Normalize()
			Expect(s.Validate()).To(Succeed(), "should accept redirect URL %q", good)
		}
	})

	It("refuses the Graph lookup on a non-Entra provider", func() {
		// Normalize would clear this; Validate is the second line, for a write
		// path that somehow skipped normalization.
		s := validSettings()
		s.UseGraphGroups = true
		s.Normalize()
		s.UseGraphGroups = true
		Expect(s.Validate()).To(MatchError(ContainSubstring("Entra")))
	})
})

var _ = Describe("claim readers", func() {
	It("reads a groups claim whatever shape the provider emits it in", func() {
		Expect(auth.ClaimStrings(map[string]any{"groups": []any{"a", "b"}}, "groups")).To(Equal([]string{"a", "b"}))
		Expect(auth.ClaimStrings(map[string]any{"groups": "solo"}, "groups")).To(Equal([]string{"solo"}))
		Expect(auth.ClaimStrings(map[string]any{"groups": "a b"}, "groups")).To(Equal([]string{"a", "b"}))
		Expect(auth.ClaimStrings(map[string]any{"groups": 7}, "groups")).To(BeEmpty())
		Expect(auth.ClaimStrings(map[string]any{}, "cognito:groups")).To(BeEmpty())
	})

	It("reads a string claim, rendering numbers and booleans rather than returning empty", func() {
		Expect(auth.ClaimString(map[string]any{"sub": "u-1"}, "sub")).To(Equal("u-1"))
		Expect(auth.ClaimString(map[string]any{"n": float64(42)}, "n")).To(Equal("42"))
		Expect(auth.ClaimString(map[string]any{"sub": "u-1"}, "oid")).To(BeEmpty())
	})
})

var _ = Describe("SettingsStore", Label("integration"), func() {
	var (
		ctx context.Context
		st  *store.Store
		enc *crypto.Encryptor
	)

	BeforeEach(func() {
		ctx = GinkgoT().Context()
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		enc = testEncryptor()
	})

	It("reports ErrNoSettings before anything is configured", func() {
		_, err := auth.NewSettingsStore(st, enc).Get(ctx)
		Expect(err).To(MatchError(auth.ErrNoSettings))
	})

	It("round-trips settings and stores the client secret encrypted", func() {
		ss := auth.NewSettingsStore(st, enc)
		_, err := ss.Save(ctx, validSettings(), "admin@example.com")
		Expect(err).NotTo(HaveOccurred())

		got, err := ss.Get(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Issuer).To(Equal("https://acme.okta.com/oauth2/default"))
		Expect(got.ClientSecret).To(Equal("client-secret"))
		Expect(got.GroupsClaim).To(Equal("groups"))
		Expect(got.AppAdminGroups).To(Equal([]string{"platform-admins"}))
		Expect(got.UpdatedBy).To(Equal("admin@example.com"))
		Expect(got.Source).To(Equal(auth.SourceDatabase))

		// The column must not hold the plaintext: this is the whole reason the
		// secret goes through internal/crypto rather than into a text column.
		row, err := st.Queries.GetOIDCSettings(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(row.ClientSecretEnc)).NotTo(ContainSubstring("client-secret"))
	})

	It("is a singleton: a second save replaces the first", func() {
		ss := auth.NewSettingsStore(st, enc)
		_, err := ss.Save(ctx, validSettings(), "a")
		Expect(err).NotTo(HaveOccurred())

		second := validSettings()
		second.Issuer = "https://other.okta.com"
		_, err = ss.Save(ctx, second, "b")
		Expect(err).NotTo(HaveOccurred())

		got, err := ss.Get(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Issuer).To(Equal("https://other.okta.com"))
	})

	It("names the recovery when the encryption key changed under a stored secret", func() {
		Expect(auth.NewSettingsStore(st, enc).Save(ctx, validSettings(), "a")).Error().NotTo(HaveOccurred())

		_, err := auth.NewSettingsStore(st, testEncryptor()).Get(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("re-enter the client secret"))
	})

	It("refuses to store a secret with no encryptor rather than writing plaintext", func() {
		_, err := auth.NewSettingsStore(st, nil).Save(ctx, validSettings(), "a")
		Expect(err).To(MatchError(auth.ErrEncryptionUnavailable))
	})
})

var _ = Describe("configuration precedence", Label("integration"), func() {
	var (
		ctx context.Context
		st  *store.Store
		enc *crypto.Encryptor
	)

	BeforeEach(func() {
		ctx = GinkgoT().Context()
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		enc = testEncryptor()
	})

	It("lets the chart win, and refuses UI writes, when oidc.issuer is set", func() {
		cfg := &config.Config{OIDC: config.OIDCConfig{
			Issuer: "https://login.microsoftonline.com/tenant/v2.0", ClientID: "chart-client",
			ClientSecret: "chart-secret", RedirectURL: "https://shepherd.example/auth/callback",
			Provider: auth.ProviderEntra, SubjectClaim: "oid", GroupsClaim: "groups",
			EmailClaim: "email", NameClaim: "name", UseGraphGroups: true,
		}, Auth: config.AuthConfig{AppAdminGroupIDs: []string{"chart-group"}}}
		h := auth.NewSettingsTestHandler(cfg, st, enc)

		// A stored row exists and is still ignored — precedence is not "first
		// one written wins".
		_, err := auth.NewSettingsStore(st, enc).Save(ctx, validSettings(), "admin")
		Expect(err).NotTo(HaveOccurred())

		effective, err := h.EffectiveSettings(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(effective.Source).To(Equal(auth.SourceHelm))
		Expect(effective.Issuer).To(Equal("https://login.microsoftonline.com/tenant/v2.0"))
		Expect(effective.AppAdminGroups).To(Equal([]string{"chart-group"}))
		Expect(h.HelmManaged()).To(BeTrue())

		_, err = h.SaveSettings(ctx, validSettings(), "admin")
		Expect(err).To(MatchError(auth.ErrHelmManaged))
		Expect(h.DeleteSettings(ctx)).To(MatchError(auth.ErrHelmManaged))
	})

	It("falls back to the stored row when the chart configured no issuer", func() {
		h := auth.NewSettingsTestHandler(&config.Config{}, st, enc)

		effective, err := h.EffectiveSettings(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(effective).To(BeNil(), "nothing configured is a normal state, not an error")

		_, err = auth.NewSettingsStore(st, enc).Save(ctx, validSettings(), "admin")
		Expect(err).NotTo(HaveOccurred())

		effective, err = h.EffectiveSettings(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(effective.Source).To(Equal(auth.SourceDatabase))
		Expect(effective.Issuer).To(Equal("https://acme.okta.com/oauth2/default"))
	})

	It("does not activate a provider whose discovery fails, and says so", func() {
		h := auth.NewSettingsTestHandler(&config.Config{}, st, enc)
		s := validSettings()
		// Loopback: this also proves the save path runs through the SSRF dial
		// guard, not just the Test-connection probe. An app admin cannot use a
		// saved issuer to reach the deployment's own network either.
		s.Issuer = "https://127.0.0.1:1/nowhere"

		_, err := h.SaveSettings(ctx, s, "admin")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("private or loopback"))
		Expect(h.OIDCEnabled()).To(BeFalse())

		// Nothing was written: enabling a provider that cannot discover would
		// put a dead-end sign-in button in front of every user.
		_, err = auth.NewSettingsStore(st, enc).Get(ctx)
		Expect(err).To(MatchError(auth.ErrNoSettings))
	})

	It("refuses a Test-connection probe aimed at the deployment's own network", func() {
		// The SSRF case the adversarial review found: go-oidc's discovery error
		// embeds the whole response body, and TestSettings hands its error to
		// the caller. Both halves are closed — the request never leaves, and
		// the message names only the address class.
		h := auth.NewSettingsTestHandler(&config.Config{}, st, enc)
		_, err := h.TestSettings(ctx, &auth.Settings{
			Provider: auth.ProviderGeneric,
			Issuer:   "https://169.254.169.254",
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("private or loopback"))
	})

	It("stores a disabled provider without probing it, so a half-finished one can be parked", func() {
		h := auth.NewSettingsTestHandler(&config.Config{}, st, enc)
		s := validSettings()
		s.Enabled = false
		s.Issuer = "https://127.0.0.1:1/nowhere"

		saved, err := h.SaveSettings(ctx, s, "admin")
		Expect(err).NotTo(HaveOccurred())
		Expect(saved.Enabled).To(BeFalse())
		Expect(h.OIDCEnabled()).To(BeFalse())
		Expect(h.StatusMessage(saved)).To(ContainSubstring("not enabled"))
	})
})

var _ = Describe("group resolution", func() {
	// resolveGroups is where an ID token becomes an authorization decision:
	// its output is matched against the app-admin list and every org's
	// admin/reader group. Every branch is covered here because a silent wrong
	// answer is a privilege decision, not a display bug.
	var h *auth.Handler

	BeforeEach(func() { h = auth.NewGroupsTestHandler() })

	It("reads the configured claim when Graph is off", func() {
		got := h.ResolveGroups(GinkgoT().Context(),
			auth.Settings{GroupsClaim: "cognito:groups"},
			map[string]any{"cognito:groups": []any{"platform", "sre"}, "groups": []any{"ignored"}},
			"", "u-1")
		Expect(got).To(Equal([]string{"platform", "sre"}))
	})

	It("prefers Graph over the claim when Graph is on", func() {
		// Entra drops the groups claim entirely past ~200 groups, so the
		// directory has to win wherever the two disagree.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test
				"value": []map[string]any{{"id": "graph-group"}},
			})
		}))
		DeferCleanup(srv.Close)

		got := h.ResolveGroups(GinkgoT().Context(),
			auth.Settings{GroupsClaim: "groups", UseGraphGroups: true, GraphBaseURL: srv.URL},
			map[string]any{"groups": []any{"claim-group"}},
			"token", "u-1")
		Expect(got).To(Equal([]string{"graph-group"}))
	})

	It("falls back to the claim when Graph fails, rather than demoting the user", func() {
		// A Graph outage used to yield NO groups, silently stripping every
		// administrator of access. This is a deliberate behaviour change — the
		// claim is signed by the same provider, so it is not forgeable — and it
		// is called out as such in docs/spec.md §7.1a.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		DeferCleanup(srv.Close)

		got := h.ResolveGroups(GinkgoT().Context(),
			auth.Settings{GroupsClaim: "groups", UseGraphGroups: true, GraphBaseURL: srv.URL},
			map[string]any{"groups": []any{"claim-group"}},
			"token", "u-1")
		Expect(got).To(Equal([]string{"claim-group"}))
	})

	It("returns nothing when Graph fails and the token carries no groups", func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		DeferCleanup(srv.Close)

		got := h.ResolveGroups(GinkgoT().Context(),
			auth.Settings{GroupsClaim: "groups", UseGraphGroups: true, GraphBaseURL: srv.URL},
			map[string]any{}, "token", "u-1")
		Expect(got).To(BeEmpty())
	})
})
