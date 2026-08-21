package mgmtapi_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

// newGitOpsRPCRouter builds a router shaped like production wiring (session +
// CSRF middleware ahead of the Connect handlers) with only MountRPC's
// GitOpsService reachable, so tests exercise the Connect path in-process
// exactly as internal/server/server.go wires it.
func newGitOpsRPCRouter(st *store.Store, authHandler *auth.Handler, cfg *config.Config, enc *crypto.Encryptor) http.Handler {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(authHandler.SessionMiddleware)
		r.Use(auth.CSRFMiddleware)
		mgmtapi.MountRPC(r, st, cfg, enc, slog.Default())
	})
	return r
}

func testEncryptor() *crypto.Encryptor {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	enc, err := crypto.NewEncryptor(base64.StdEncoding.EncodeToString(key))
	Expect(err).NotTo(HaveOccurred())
	return enc
}

// createCredWire is the Connect wire shape (camelCase) for
// CreateCredentialRequest, used instead of hand-built JSON strings so
// values containing newlines/quotes (PEM keys) are escaped correctly.
type createCredWire struct {
	OrgID                 string         `json:"orgId"`
	Name                  string         `json:"name"`
	Kind                  string         `json:"kind,omitempty"`
	Username              string         `json:"username,omitempty"`
	AdoOrgURL             string         `json:"adoOrgUrl,omitempty"`
	EntraTenantID         string         `json:"entraTenantId,omitempty"`
	ClientID              string         `json:"clientId,omitempty"`
	ProviderConfig        map[string]any `json:"providerConfig,omitempty"`
	ClientSecret          string         `json:"clientSecret,omitempty"`
	Secret2               string         `json:"secret2,omitempty"`
	SSHKnownHosts         string         `json:"sshKnownHosts,omitempty"`
	CaCert                string         `json:"caCert,omitempty"`
	TLSInsecureSkipVerify bool           `json:"tlsInsecureSkipVerify,omitempty"`
}

// testCredWire is the Connect wire shape for TestCredentialRequest.
type testCredWire struct {
	OrgID   string `json:"orgId"`
	ID      string `json:"id"`
	RepoURL string `json:"repoUrl"`
	Branch  string `json:"branch,omitempty"`
}

// newRSAPrivateKeyPEM generates a fresh RSA key and PEM-encodes it (PKCS#1),
// matching one of the two formats internal/gitrepo's github_app strategy
// accepts.
func newRSAPrivateKeyPEM() []byte {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	Expect(err).NotTo(HaveOccurred())
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

var _ = Describe("GitOpsService (Connect RPC)", Label("integration"), func() {
	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		authHandler *auth.Handler
		cfg         *config.Config
		server      *httptest.Server
		orgID       string
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "gitops-rpc-org", DisplayName: "GitOps RPC Org",
			AdminGroupID: "gitops-admin-group", ReaderGroupID: pgtype.Text{},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()

		cfg = &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler = auth.NewLocalAdmin(cfg, st, slog.Default())
	})

	AfterEach(func() {
		if server != nil {
			server.Close()
		}
		st.Close()
		cancel()
	})

	// newSession creates a session row and returns its cookie. isAdmin grants
	// membership in the test org's admin group; app grants global app-admin.
	newSession := func(id string, groupIDs []string, isAppAdmin bool) *http.Cookie {
		groupsJSON, err := json.Marshal(groupIDs)
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: id, UserOid: "oid-" + id, Email: id + "@example.com", DisplayName: id,
			GroupIds:   groupsJSON,
			IsAppAdmin: isAppAdmin,
			ExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
			Source:     "test",
		})
		Expect(err).NotTo(HaveOccurred())
		return &http.Cookie{Name: "shepherd_session", Value: id}
	}

	postConnect := func(procedure string, body string, cookie *http.Cookie) *http.Response {
		req, err := http.NewRequest(http.MethodPost, server.URL+procedure, strings.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		return resp
	}

	decodeBody := func(resp *http.Response, v any) {
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(body, v)).To(Succeed())
	}

	Context("with encryption configured", func() {
		var admin *http.Cookie

		BeforeEach(func() {
			server = httptest.NewServer(newGitOpsRPCRouter(st, authHandler, cfg, testEncryptor()))
			admin = newSession("gitops-admin", []string{"gitops-admin-group"}, false)
		})

		createCredential := func(w createCredWire) map[string]any {
			w.OrgID = orgID
			body, err := json.Marshal(w)
			Expect(err).NotTo(HaveOccurred())
			resp := postConnect("/shepherd.mgmt.v1.GitOpsService/CreateCredential", string(body), admin)
			Expect(resp.StatusCode).To(Equal(http.StatusOK), "kind=%s", w.Kind)
			var created map[string]any
			decodeBody(resp, &created)
			return created
		}

		It("creates and lists a credential over the Connect handler (happy path, legacy kind default)", func() {
			// The raw Connect wire uses connect-go's default JSON codec
			// (camelCase field names, unlike the REST shim's UseProtoNames
			// snake_case) — see docs/archive/api-contract-design.md's note that
			// Connect endpoints are plain HTTP POST + JSON in their own
			// dialect, separate from the REST shim's byte-compatible JSON.
			created := createCredential(createCredWire{
				Name: "primary", AdoOrgURL: "https://dev.azure.com/acme",
				EntraTenantID: "tenant-1", ClientID: "client-1", ClientSecret: "super-secret",
			})
			Expect(created["name"]).To(Equal("primary"))
			Expect(created["clientId"]).To(Equal("client-1"))
			// kind omitted -> defaults to ado_sp (the legacy REST shim's
			// pre-rename shape never sent kind).
			Expect(created["kind"]).To(Equal("ado_sp"))
			Expect(created).NotTo(HaveKey("clientSecret"))
			Expect(created).NotTo(HaveKey("client_secret"))

			listBody := `{"orgId":"` + orgID + `"}`
			listResp := postConnect("/shepherd.mgmt.v1.GitOpsService/ListCredentials", listBody, admin)
			Expect(listResp.StatusCode).To(Equal(http.StatusOK))

			var listed struct {
				Items []map[string]any `json:"items"`
				Total int              `json:"total"`
			}
			decodeBody(listResp, &listed)
			Expect(listed.Total).To(Equal(1))
			Expect(listed.Items).To(HaveLen(1))
			Expect(listed.Items[0]["name"]).To(Equal("primary"))
		})

		It("denies a non-member session with the Connect permission-denied code", func() {
			outsider := newSession("gitops-outsider", []string{"some-other-group"}, false)

			resp := postConnect("/shepherd.mgmt.v1.GitOpsService/ListCredentials", `{"orgId":"`+orgID+`"}`, outsider)
			Expect(resp.StatusCode).To(Equal(http.StatusForbidden))

			var payload struct {
				Code string `json:"code"`
			}
			decodeBody(resp, &payload)
			Expect(payload.Code).To(Equal("permission_denied"))
		})

		It("rejects an unsupported kind", func() {
			body, err := json.Marshal(createCredWire{OrgID: orgID, Name: "bad", Kind: "carrier-pigeon", ClientSecret: "x"})
			Expect(err).NotTo(HaveOccurred())
			resp := postConnect("/shepherd.mgmt.v1.GitOpsService/CreateCredential", string(body), admin)
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("rejects kind=ssh without ssh_known_hosts (no accept-any-host-key mode)", func() {
			body, err := json.Marshal(createCredWire{
				OrgID: orgID, Name: "no-known-hosts", Kind: "ssh", ClientSecret: string(newRSAPrivateKeyPEM()),
			})
			Expect(err).NotTo(HaveOccurred())
			resp := postConnect("/shepherd.mgmt.v1.GitOpsService/CreateCredential", string(body), admin)
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

		DescribeTable("round-trips every credential kind without ever returning a secret",
			func(w createCredWire, assertExtra func(map[string]any)) {
				created := createCredential(w)
				Expect(created["kind"]).To(Equal(w.Kind))
				for _, secretKey := range []string{"clientSecret", "secret2", "client_secret", "privateKey"} {
					Expect(created).NotTo(HaveKey(secretKey))
				}
				if assertExtra != nil {
					assertExtra(created)
				}
			},
			Entry("none", createCredWire{Name: "cred-none", Kind: "none"}, nil),
			Entry("basic", createCredWire{Name: "cred-basic", Kind: "basic", Username: "alice", ClientSecret: "hunter2"},
				func(c map[string]any) { Expect(c["username"]).To(Equal("alice")) }),
			Entry("pat", createCredWire{Name: "cred-pat", Kind: "pat", Username: "oauth2", ClientSecret: "ghp_abc123"},
				func(c map[string]any) { Expect(c["username"]).To(Equal("oauth2")) }),
			Entry("ssh", createCredWire{
				Name: "cred-ssh", Kind: "ssh", Username: "git",
				ClientSecret: "-----BEGIN OPENSSH PRIVATE KEY-----\nfake\n-----END OPENSSH PRIVATE KEY-----",
				Secret2:      "key-passphrase", SSHKnownHosts: "example.com ssh-ed25519 AAAA...",
			}, func(c map[string]any) { Expect(c["username"]).To(Equal("git")) }),
			Entry("ado_sp", createCredWire{
				Name: "cred-ado", Kind: "ado_sp", AdoOrgURL: "https://dev.azure.com/acme",
				EntraTenantID: "tenant-2", ClientID: "client-2", ClientSecret: "sp-secret",
			}, func(c map[string]any) {
				Expect(c["adoOrgUrl"]).To(Equal("https://dev.azure.com/acme"))
				Expect(c["entraTenantId"]).To(Equal("tenant-2"))
			}),
			Entry("github_app", createCredWire{
				Name: "cred-ghapp", Kind: "github_app",
				ProviderConfig: map[string]any{"app_id": "1234", "installation_id": "5678", "api_base_url": "https://api.github.com"},
				ClientSecret:   "-----BEGIN RSA PRIVATE KEY-----\nfake\n-----END RSA PRIVATE KEY-----",
			}, func(c map[string]any) {
				pc, ok := c["providerConfig"].(map[string]any)
				Expect(ok).To(BeTrue(), "providerConfig should round-trip as an object")
				Expect(pc["app_id"]).To(Equal("1234"))
				Expect(pc["installation_id"]).To(Equal("5678"))
			}),
		)

		Describe("TestCredential", func() {
			testCredential := func(id, repoURL string) map[string]any {
				body, err := json.Marshal(testCredWire{OrgID: orgID, ID: id, RepoURL: repoURL})
				Expect(err).NotTo(HaveOccurred())
				resp := postConnect("/shepherd.mgmt.v1.GitOpsService/TestCredential", string(body), admin)
				Expect(resp.StatusCode).To(Equal(http.StatusOK))
				var got map[string]any
				decodeBody(resp, &got)
				return got
			}

			It("reports a git reachability failure (connection refused) as reachable=false with no token exchange involved", func() {
				created := createCredential(createCredWire{Name: "test-pat", Kind: "pat", Username: "x", ClientSecret: "tok"})
				credID, ok := created["id"].(string)
				Expect(ok).To(BeTrue())
				// Port 1 on loopback: nothing listens there, so this fails
				// fast and deterministically without touching the network,
				// unlike a DNS-based failure would.
				got := testCredential(credID, "http://127.0.0.1:1/nope.git")
				Expect(got["reachable"]).To(BeNil(), "proto3 false is omitted by connect-go's default JSON codec")
				Expect(got["error"]).NotTo(BeEmpty())
				Expect(got["tokenExchangeRequired"]).To(BeNil())
			})

			It("distinguishes a token-exchange failure from a git reachability failure (github_app)", func() {
				badTokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusUnauthorized)
				}))
				defer badTokenServer.Close()

				created := createCredential(createCredWire{
					Name: "test-ghapp-badtoken", Kind: "github_app",
					ProviderConfig: map[string]any{"app_id": "1", "installation_id": "1", "api_base_url": badTokenServer.URL},
					ClientSecret:   string(newRSAPrivateKeyPEM()),
				})

				credID, ok := created["id"].(string)
				Expect(ok).To(BeTrue())
				got := testCredential(credID, "http://127.0.0.1:1/nope.git")
				Expect(got["reachable"]).To(BeNil(), "proto3 false is omitted by connect-go's default JSON codec")
				Expect(got["tokenExchangeRequired"]).To(BeTrue())
				Expect(got["tokenExchangeOk"]).To(BeNil(), "proto3 false is omitted by connect-go's default JSON codec")
				Expect(got["error"]).To(ContainSubstring("token exchange failed"))
			})

			It("reports token exchange succeeding but git being unreachable separately (github_app)", func() {
				okTokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test
						"token": "installation-token-xyz", "expires_at": time.Now().Add(time.Hour),
					})
				}))
				defer okTokenServer.Close()

				created := createCredential(createCredWire{
					Name: "test-ghapp-oktoken", Kind: "github_app",
					ProviderConfig: map[string]any{"app_id": "1", "installation_id": "1", "api_base_url": okTokenServer.URL},
					ClientSecret:   string(newRSAPrivateKeyPEM()),
				})

				credID, ok := created["id"].(string)
				Expect(ok).To(BeTrue())
				got := testCredential(credID, "http://127.0.0.1:1/nope.git")
				Expect(got["reachable"]).To(BeNil(), "proto3 false is omitted by connect-go's default JSON codec")
				Expect(got["tokenExchangeRequired"]).To(BeTrue())
				Expect(got["tokenExchangeOk"]).To(BeTrue(), "the token minted fine; only the git leg failed")
				Expect(got["error"]).NotTo(ContainSubstring("token exchange failed"), "a git-reachability error, not a token one")
			})

			It("404s for a credential id from another org", func() {
				created := createCredential(createCredWire{Name: "test-cross-org", Kind: "pat", ClientSecret: "tok"})
				credID, ok := created["id"].(string)
				Expect(ok).To(BeTrue())

				other, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
					Name: "gitops-rpc-org-2", DisplayName: "GitOps RPC Org 2",
					AdminGroupID: "gitops-admin-group-2", ReaderGroupID: pgtype.Text{},
				})
				Expect(err).NotTo(HaveOccurred())
				otherAdmin := newSession("gitops-admin-2", []string{"gitops-admin-group-2"}, false)

				body, err := json.Marshal(testCredWire{OrgID: other.ID.String(), ID: credID, RepoURL: "http://127.0.0.1:1/x.git"})
				Expect(err).NotTo(HaveOccurred())
				resp := postConnect("/shepherd.mgmt.v1.GitOpsService/TestCredential", string(body), otherAdmin)
				Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
			})
		})
	})

	Context("without encryption configured", func() {
		BeforeEach(func() {
			server = httptest.NewServer(newGitOpsRPCRouter(st, authHandler, cfg, nil))
		})

		It("maps the nil-encryptor guard to the Connect unavailable code (503)", func() {
			admin := newSession("gitops-admin-noenc", []string{"gitops-admin-group"}, false)

			createBody := `{"orgId":"` + orgID + `","name":"primary","kind":"ado_sp","adoOrgUrl":"https://dev.azure.com/acme","entraTenantId":"tenant-1","clientId":"client-1","clientSecret":"super-secret"}`
			resp := postConnect("/shepherd.mgmt.v1.GitOpsService/CreateCredential", createBody, admin)
			Expect(resp.StatusCode).To(Equal(http.StatusServiceUnavailable))

			var payload struct {
				Code string `json:"code"`
			}
			decodeBody(resp, &payload)
			Expect(payload.Code).To(Equal("unavailable"))

			// Reads still degrade to an empty list rather than erroring.
			listResp := postConnect("/shepherd.mgmt.v1.GitOpsService/ListCredentials", `{"orgId":"`+orgID+`"}`, admin)
			Expect(listResp.StatusCode).To(Equal(http.StatusOK))
			var listed struct {
				Items []map[string]any `json:"items"`
				Total int              `json:"total"`
			}
			decodeBody(listResp, &listed)
			Expect(listed.Total).To(Equal(0))
		})

		It("maps the nil-encryptor guard for TestCredential too", func() {
			admin := newSession("gitops-admin-noenc-2", []string{"gitops-admin-group"}, false)
			body := `{"orgId":"` + orgID + `","id":"00000000-0000-0000-0000-000000000000","repoUrl":"http://127.0.0.1:1/x.git"}`
			resp := postConnect("/shepherd.mgmt.v1.GitOpsService/TestCredential", body, admin)
			Expect(resp.StatusCode).To(Equal(http.StatusServiceUnavailable))
		})
	})
})
