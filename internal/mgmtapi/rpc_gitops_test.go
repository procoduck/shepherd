package mgmtapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
		BeforeEach(func() {
			server = httptest.NewServer(newGitOpsRPCRouter(st, authHandler, cfg, testEncryptor()))
		})

		It("creates and lists an ADO credential over the Connect handler (happy path)", func() {
			admin := newSession("gitops-admin", []string{"gitops-admin-group"}, false)

			// The raw Connect wire uses connect-go's default JSON codec
			// (camelCase field names, unlike the REST shim's UseProtoNames
			// snake_case) — see docs/api-contract-design.md's note that
			// Connect endpoints are plain HTTP POST + JSON in their own
			// dialect, separate from the REST shim's byte-compatible JSON.
			createBody := `{"orgId":"` + orgID + `","name":"primary","adoOrgUrl":"https://dev.azure.com/acme","entraTenantId":"tenant-1","clientId":"client-1","clientSecret":"super-secret"}`
			createResp := postConnect("/shepherd.mgmt.v1.GitOpsService/CreateCredential", createBody, admin)
			Expect(createResp.StatusCode).To(Equal(http.StatusOK))

			var created map[string]any
			decodeBody(createResp, &created)
			Expect(created["name"]).To(Equal("primary"))
			Expect(created["clientId"]).To(Equal("client-1"))
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
	})

	Context("without encryption configured", func() {
		BeforeEach(func() {
			server = httptest.NewServer(newGitOpsRPCRouter(st, authHandler, cfg, nil))
		})

		It("maps the nil-encryptor guard to the Connect unavailable code (503)", func() {
			admin := newSession("gitops-admin-noenc", []string{"gitops-admin-group"}, false)

			createBody := `{"orgId":"` + orgID + `","name":"primary","adoOrgUrl":"https://dev.azure.com/acme","entraTenantId":"tenant-1","clientId":"client-1","clientSecret":"super-secret"}`
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
	})
})
