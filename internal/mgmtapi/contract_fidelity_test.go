package mgmtapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Regression coverage for the field-presence (`,omitempty`) contract-fidelity
// fix: several REST responses now always emit fields the legacy hand-written
// structs marked `,omitempty`, because the shared MarshalOpts sets
// EmitUnpopulated unconditionally. writeProtoJSONOmit (shim.go) restores the
// omitted-when-zero behavior for the specific fields the legacy structs
// exempted. See docs/api-contract-design.md's byte-compatibility
// requirement.
var _ = Describe("REST shim field-presence fidelity", Label("integration"), func() {
	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		server      *httptest.Server
		orgID       string
		adminCookie *http.Cookie
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "fidelity-org", DisplayName: "Fidelity Org",
			AdminGroupID: "fidelity-admin-group", ReaderGroupID: pgtype.Text{},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRESTRouter(st, authHandler, cfg, testEncryptor()))
		adminCookie = newAppAdminSession(ctx, st)
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	decode := func(resp *http.Response) map[string]any {
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		var result map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
		return result
	}

	Describe("GET /orgs/{org}/collectors/{id}/served-config", func() {
		It("omits computed_at on a cache miss, but still emits empty content/hash", func() {
			cluster, err := st.Queries.UpsertCluster(ctx, "fidelity-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgUUID(orgID)})).To(Succeed())
			collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
			Expect(err).NotTo(HaveOccurred())

			resp := getRequest(server, fmt.Sprintf("/orgs/%s/collectors/%s/served-config", orgID, collector.ID.String()), adminCookie)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			result := decode(resp)
			Expect(result["content"]).To(Equal(""))
			Expect(result["hash"]).To(Equal(""))
			Expect(result).NotTo(HaveKey("computed_at"), "computed_at never existed on a cache miss")
		})
	})

	Describe("POST /admin/orgs and GET /admin/orgs", func() {
		It("omits reader_group_id when unset", func() {
			resp := postJSON(server, "/admin/orgs", map[string]any{
				"name": "no-reader-org", "display_name": "No Reader Org", "admin_group_id": "no-reader-admin-group",
			}, adminCookie)
			Expect(resp.StatusCode).To(Equal(http.StatusCreated))
			created := decode(resp)
			Expect(created).NotTo(HaveKey("reader_group_id"))

			listResp := getRequest(server, "/admin/orgs", adminCookie)
			listed := decode(listResp)
			items, ok := listed["items"].([]any)
			Expect(ok).To(BeTrue())
			var found map[string]any
			for _, raw := range items {
				item, _ := raw.(map[string]any) //nolint:errcheck // decoded JSON array elements are always objects here
				if item["name"] == "no-reader-org" {
					found = item
				}
			}
			Expect(found).NotTo(BeNil())
			Expect(found).NotTo(HaveKey("reader_group_id"))
			Expect(found).To(HaveKey("id"), "always-present fields must still be emitted")
		})
	})

	Describe("GET /admin/clusters?unclaimed=true", func() {
		It("omits org_id for an unclaimed cluster", func() {
			_, err := st.Queries.UpsertCluster(ctx, "fidelity-unclaimed-cluster")
			Expect(err).NotTo(HaveOccurred())

			resp := getRequest(server, "/admin/clusters?unclaimed=true", adminCookie)
			result := decode(resp)
			items, ok := result["items"].([]any)
			Expect(ok).To(BeTrue())
			var found map[string]any
			for _, raw := range items {
				item, _ := raw.(map[string]any) //nolint:errcheck // decoded JSON array elements are always objects here
				if item["name"] == "fidelity-unclaimed-cluster" {
					found = item
				}
			}
			Expect(found).NotTo(BeNil())
			Expect(found).NotTo(HaveKey("org_id"))
		})
	})

	Describe("pipeline responses", func() {
		It("omits revision/revisions on List/Create, but Get with revision history includes revisions", func() {
			created := decode(postJSON(server, fmt.Sprintf("/orgs/%s/pipelines", orgID), map[string]any{
				"name": "fidelity-pipe", "contents": "// ok", "matchers": []string{},
			}, adminCookie))
			Expect(created).NotTo(HaveKey("revision"))
			Expect(created).NotTo(HaveKey("revisions"))
			id, _ := created["id"].(string) //nolint:errcheck // asserted via update below

			listResp := getRequest(server, fmt.Sprintf("/orgs/%s/pipelines", orgID), adminCookie)
			listed := decode(listResp)
			items, ok := listed["items"].([]any)
			Expect(ok).To(BeTrue())
			Expect(items).NotTo(BeEmpty())
			first, ok := items[0].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(first).NotTo(HaveKey("revision"))
			Expect(first).NotTo(HaveKey("revisions"))

			// An update creates revision history, which GetPipeline attaches.
			updated := decode(mustDo(http.MethodPut, server.URL+fmt.Sprintf("/orgs/%s/pipelines/%s", orgID, id), map[string]any{
				"name": "fidelity-pipe", "contents": "// updated", "matchers": []string{},
			}, adminCookie))
			Expect(updated).NotTo(HaveKey("revision"), "revision (singular) is never populated by any handler")
			Expect(updated).NotTo(HaveKey("revisions"), "PUT never attaches revision history")

			getResp := getRequest(server, fmt.Sprintf("/orgs/%s/pipelines/%s", orgID, id), adminCookie)
			got := decode(getResp)
			revs, ok := got["revisions"].([]any)
			Expect(ok).To(BeTrue(), "GetPipeline attaches revision history once one exists")
			Expect(revs).NotTo(BeEmpty())
		})
	})

	Describe("POST /orgs/{org}/repo-links", func() {
		It("omits sync_status/last_synced_at on a just-created repo link", func() {
			cluster, err := st.Queries.UpsertCluster(ctx, "fidelity-repolink-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgUUID(orgID)})).To(Succeed())
			collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
			Expect(err).NotTo(HaveOccurred())
			cred, err := st.Queries.CreateGitCredential(ctx, sqlc.CreateGitCredentialParams{
				OrgID: orgUUID(orgID), Name: "fidelity-cred", Kind: "ado_sp",
				AdoOrgUrl:       pgtype.Text{String: "https://dev.azure.com/x", Valid: true},
				EntraTenantID:   pgtype.Text{String: "tenant", Valid: true},
				ClientID:        pgtype.Text{String: "client", Valid: true},
				ClientSecretEnc: []byte("not-really-encrypted"),
				ProviderConfig:  json.RawMessage("{}"),
			})
			Expect(err).NotTo(HaveOccurred())

			resp := postJSON(server, fmt.Sprintf("/orgs/%s/repo-links", orgID), map[string]any{
				"collector_id": collector.ID.String(), "credential_id": cred.ID.String(),
				"repo_url": "https://dev.azure.com/x/proj/_git/repo",
			}, adminCookie)
			Expect(resp.StatusCode).To(Equal(http.StatusCreated))
			created := decode(resp)
			Expect(created).NotTo(HaveKey("sync_status"))
			Expect(created).NotTo(HaveKey("last_synced_at"))
			Expect(created["repo_url"]).To(Equal("https://dev.azure.com/x/proj/_git/repo"), "always-present fields must still be emitted")
		})
	})
})

// mustDo issues a JSON request with the given method, a session cookie, and
// the CSRF header the production web transport always sends on mutating
// requests, returning the raw *http.Response for the caller to decode.
func mustDo(method, url string, body any, cookie *http.Cookie) *http.Response {
	var buf bytes.Buffer
	Expect(json.NewEncoder(&buf).Encode(body)).To(Succeed())
	req, err := http.NewRequest(method, url, &buf)
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
