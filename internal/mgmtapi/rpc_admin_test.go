package mgmtapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Connect-path coverage for AdminService and MeService, over the same
// newRPCWiringRouter(...) wiring rpc_wiring_test.go proves 404-guards and
// unimplemented-stub behavior with — this suite exercises the now-migrated
// AdminService/MeService methods themselves: a happy path, an authz denial,
// and an error-code mapping case per service, per
// docs/archive/api-contract-design.md's testing rules. mgmtapi_test.go covers the
// REST shim paths (the compatibility oracle) and is unchanged by this
// migration.
var _ = Describe("shepherd.mgmt.v1 AdminService and MeService RPC", Label("integration"), func() {
	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		authHandler *auth.Handler
		server      *httptest.Server
		orgIDStr    string
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name:          "admin-rpc-org",
			DisplayName:   "Admin RPC Org",
			AdminGroupID:  "admin-rpc-admin-group",
			ReaderGroupID: pgtype.Text{String: "admin-rpc-reader-group", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgIDStr = o.ID.String()

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler = auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	createSession := func(isAppAdmin bool, groupIDs []string) *http.Cookie {
		groupsJSON, err := json.Marshal(groupIDs)
		Expect(err).NotTo(HaveOccurred())
		sessionID := fmt.Sprintf("admin-rpc-session-%d", time.Now().UnixNano())
		_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: sessionID, UserOid: "admin-user-oid", Email: "admin-user@example.com", DisplayName: "Admin User",
			GroupIds:   groupsJSON,
			IsAppAdmin: isAppAdmin,
			ExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
			Source:     "test",
		})
		Expect(err).NotTo(HaveOccurred())
		return &http.Cookie{Name: "shepherd_session", Value: sessionID}
	}

	postConnect := func(procedure string, body map[string]any, cookie *http.Cookie) *http.Response {
		buf, err := json.Marshal(body)
		Expect(err).NotTo(HaveOccurred())
		req, err := http.NewRequest(http.MethodPost, server.URL+procedure, strings.NewReader(string(buf)))
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

	decodeBody := func(resp *http.Response) map[string]any {
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		var payload map[string]any
		Expect(json.Unmarshal(body, &payload)).To(Succeed())
		return payload
	}

	Describe("AdminService", func() {
		It("creates and lists an org for an app-admin session (happy path)", func() {
			appAdmin := createSession(true, nil)

			createResp := postConnect("/shepherd.mgmt.v1.AdminService/CreateOrg", map[string]any{
				"name": "created-via-rpc", "displayName": "Created via RPC", "adminGroupId": "created-admin-group",
			}, appAdmin)
			Expect(createResp.StatusCode).To(Equal(http.StatusOK))
			created := decodeBody(createResp)
			Expect(created["name"]).To(Equal("created-via-rpc"))
			Expect(created["displayName"]).To(Equal("Created via RPC"))

			listResp := postConnect("/shepherd.mgmt.v1.AdminService/ListOrgs", map[string]any{}, appAdmin)
			Expect(listResp.StatusCode).To(Equal(http.StatusOK))
			listed := decodeBody(listResp)
			items, ok := listed["items"].([]any)
			Expect(ok).To(BeTrue(), "expected an items array")
			Expect(len(items)).To(BeNumerically(">=", 2), "seed org plus the one just created")
		})

		It("UpdateOrg persists the attribute-matching rollout flags independently (#139)", func() {
			appAdmin := createSession(true, nil)

			updateResp := postConnect("/shepherd.mgmt.v1.AdminService/UpdateOrg", map[string]any{
				"orgId": orgIDStr, "displayName": "Admin RPC Org", "adminGroupId": "admin-rpc-admin-group",
				"allowLabelMatching": true,
			}, appAdmin)
			Expect(updateResp.StatusCode).To(Equal(http.StatusOK))
			updated := decodeBody(updateResp)
			Expect(updated["allowLabelMatching"]).To(BeTrue())
			// allowLocalAttributeMatching must default to false and not be
			// flipped as a side effect of setting the other flag — they are
			// two independent flags, not one combined toggle.
			_, present := updated["allowLocalAttributeMatching"]
			if present {
				Expect(updated["allowLocalAttributeMatching"]).To(BeFalse())
			}

			// A second update flips the other flag and must leave the first
			// one exactly as it was, since both are read from the request on
			// every UpdateOrg call rather than merged against the stored row.
			updateResp2 := postConnect("/shepherd.mgmt.v1.AdminService/UpdateOrg", map[string]any{
				"orgId": orgIDStr, "displayName": "Admin RPC Org", "adminGroupId": "admin-rpc-admin-group",
				"allowLabelMatching": true, "allowLocalAttributeMatching": true,
			}, appAdmin)
			Expect(updateResp2.StatusCode).To(Equal(http.StatusOK))
			updated2 := decodeBody(updateResp2)
			Expect(updated2["allowLabelMatching"]).To(BeTrue())
			Expect(updated2["allowLocalAttributeMatching"]).To(BeTrue())
		})

		It("denies ListOrgs for an org-admin session that is not an app admin", func() {
			orgAdmin := createSession(false, []string{"admin-rpc-admin-group"})

			resp := postConnect("/shepherd.mgmt.v1.AdminService/ListOrgs", map[string]any{}, orgAdmin)
			Expect(resp.StatusCode).To(Equal(http.StatusForbidden))

			payload := decodeBody(resp)
			Expect(payload["code"]).To(Equal("permission_denied"))
		})

		It("maps ClaimCluster on an unknown cluster to the Connect not_found code (404)", func() {
			appAdmin := createSession(true, nil)

			resp := postConnect("/shepherd.mgmt.v1.AdminService/ClaimCluster", map[string]any{
				"cluster": "no-such-cluster", "orgId": orgIDStr,
			}, appAdmin)
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound))

			payload := decodeBody(resp)
			Expect(payload["code"]).To(Equal("not_found"))
		})

		It("DeleteOrg refuses an org that still has clusters or pipelines, and deletes one that is empty", func() {
			appAdmin := createSession(true, nil)

			nonEmptyOrg, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
				Name: "delete-org-rpc-nonempty", DisplayName: "Delete Org RPC Non-Empty", AdminGroupID: "delete-org-rpc-admin",
			})
			Expect(err).NotTo(HaveOccurred())
			cluster, err := st.Queries.UpsertCluster(ctx, "delete-org-rpc-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: nonEmptyOrg.ID})).To(Succeed())

			refuseResp := postConnect("/shepherd.mgmt.v1.AdminService/DeleteOrg", map[string]any{
				"orgId": nonEmptyOrg.ID.String(),
			}, appAdmin)
			Expect(refuseResp.StatusCode).To(Equal(http.StatusConflict))
			refusePayload := decodeBody(refuseResp)
			Expect(refusePayload["code"]).To(Equal("already_exists"))
			Expect(refusePayload["message"]).To(ContainSubstring("1 clusters"))

			emptyOrg, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
				Name: "delete-org-rpc-empty", DisplayName: "Delete Org RPC Empty", AdminGroupID: "delete-org-rpc-empty-admin",
			})
			Expect(err).NotTo(HaveOccurred())
			deleteResp := postConnect("/shepherd.mgmt.v1.AdminService/DeleteOrg", map[string]any{
				"orgId": emptyOrg.ID.String(),
			}, appAdmin)
			Expect(deleteResp.StatusCode).To(Equal(http.StatusOK))
		})

		It("UnclaimCluster marks every collector in the cluster dirty and clears the org assignment", func() {
			appAdmin := createSession(true, nil)

			cluster, err := st.Queries.UpsertCluster(ctx, "unclaim-rpc-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgUUID(orgIDStr)})).To(Succeed())

			metrics, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
			Expect(err).NotTo(HaveOccurred())
			logs, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "logs"})
			Expect(err).NotTo(HaveOccurred())

			// Start both collectors clean (dirty=false) so the assertion below
			// proves UnclaimCluster is what dirtied them, not the row default.
			_, err = st.Queries.UpsertServeCacheConditional(ctx, sqlc.UpsertServeCacheConditionalParams{
				CollectorID: metrics.ID, Content: "x", Hash: "h1",
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = st.Queries.UpsertServeCacheConditional(ctx, sqlc.UpsertServeCacheConditionalParams{
				CollectorID: logs.ID, Content: "x", Hash: "h2",
			})
			Expect(err).NotTo(HaveOccurred())

			resp := postConnect("/shepherd.mgmt.v1.AdminService/UnclaimCluster", map[string]any{
				"cluster": "unclaim-rpc-cluster",
			}, appAdmin)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			payload := decodeBody(resp)
			Expect(payload["status"]).To(Equal("unclaimed"))

			metricsCache, err := st.Queries.GetServeCache(ctx, metrics.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(metricsCache.Dirty).To(BeTrue(), "metrics collector's serve_cache must be marked dirty by UnclaimCluster")
			logsCache, err := st.Queries.GetServeCache(ctx, logs.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(logsCache.Dirty).To(BeTrue(), "logs collector's serve_cache must be marked dirty by UnclaimCluster")

			reloaded, err := st.Queries.GetClusterByID(ctx, cluster.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(reloaded.OrgID.Valid).To(BeFalse(), "cluster must no longer be assigned to an org")
		})

		It("UnclaimCluster surfaces the failure and rolls back the unclaim when marking the serve cache dirty cannot complete", func() {
			appAdmin := createSession(true, nil)

			cluster, err := st.Queries.UpsertCluster(ctx, "unclaim-rollback-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgUUID(orgIDStr)})).To(Succeed())

			collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
			Expect(err).NotTo(HaveOccurred())
			_, err = st.Queries.UpsertServeCacheConditional(ctx, sqlc.UpsertServeCacheConditionalParams{
				CollectorID: collector.ID, Content: "x", Hash: "h1",
			})
			Expect(err).NotTo(HaveOccurred())

			// Hold a row lock on the collector's serve_cache row from a separate
			// connection so UnclaimCluster's dirty-marking statement blocks on
			// it, then cancel that blocked backend — the deterministic way to
			// force the second statement in the unclaim to fail without a
			// fault-injection seam in store.go.
			lockConn, err := st.Pool().Acquire(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer lockConn.Release()
			lockTx, err := lockConn.Begin(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer func() { _ = lockTx.Rollback(ctx) }() //nolint:errcheck // best-effort cleanup; explicit Rollback below is the real one
			_, err = lockTx.Exec(ctx, `SELECT * FROM serve_cache WHERE collector_id = $1 FOR UPDATE`, collector.ID)
			Expect(err).NotTo(HaveOccurred())

			respCh := make(chan *http.Response, 1)
			go func() {
				defer GinkgoRecover()
				respCh <- postConnect("/shepherd.mgmt.v1.AdminService/UnclaimCluster", map[string]any{
					"cluster": "unclaim-rollback-cluster",
				}, appAdmin)
			}()

			var pid int
			Eventually(func() error {
				return st.Pool().QueryRow(ctx,
					`SELECT pid FROM pg_stat_activity
					 WHERE datname = current_database() AND wait_event_type = 'Lock' AND query ILIKE '%serve_cache%'`,
				).Scan(&pid)
			}, "5s", "20ms").Should(Succeed(), "UnclaimCluster's dirty-mark statement never blocked on the held lock")
			_, err = st.Pool().Exec(ctx, `SELECT pg_cancel_backend($1)`, pid)
			Expect(err).NotTo(HaveOccurred())

			resp := <-respCh
			Expect(resp.StatusCode).NotTo(Equal(http.StatusOK), "a failed dirty-mark must not report success")
			payload := decodeBody(resp)
			Expect(payload["code"]).To(Equal("internal"))

			Expect(lockTx.Rollback(ctx)).To(Succeed())

			reloaded, err := st.Queries.GetClusterByID(ctx, cluster.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(reloaded.OrgID.Valid).To(BeTrue(), "the unclaim must roll back when the dirty-mark fails, not partially apply")

			cache, err := st.Queries.GetServeCache(ctx, collector.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(cache.Dirty).To(BeFalse(), "serve_cache must not be left dirty by a rolled-back unclaim")
		})

		It("denies UnclaimCluster for an org-admin session that is not an app admin", func() {
			orgAdmin := createSession(false, []string{"admin-rpc-admin-group"})

			resp := postConnect("/shepherd.mgmt.v1.AdminService/UnclaimCluster", map[string]any{
				"cluster": "no-such-cluster",
			}, orgAdmin)
			Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
			payload := decodeBody(resp)
			Expect(payload["code"]).To(Equal("permission_denied"))
		})

		It("creates an agent token whose secret is returned exactly once", func() {
			appAdmin := createSession(true, nil)

			resp := postConnect("/shepherd.mgmt.v1.AdminService/CreateAgentToken", map[string]any{
				"name": "rpc-created-token",
			}, appAdmin)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			payload := decodeBody(resp)
			Expect(payload["name"]).To(Equal("rpc-created-token"))
			secret, ok := payload["secret"].(string)
			Expect(ok).To(BeTrue())
			Expect(secret).NotTo(BeEmpty())

			listResp := postConnect("/shepherd.mgmt.v1.AdminService/ListAgentTokens", map[string]any{}, appAdmin)
			Expect(listResp.StatusCode).To(Equal(http.StatusOK))
			listed := decodeBody(listResp)
			items, ok := listed["items"].([]any)
			Expect(ok).To(BeTrue())
			for _, raw := range items {
				item, ok := raw.(map[string]any)
				Expect(ok).To(BeTrue())
				Expect(item).NotTo(HaveKey("secret"), "list response must never carry the plaintext secret")
			}
		})

		It("creates, lists and deletes a collector OIDC identity binding (app-admin only)", func() {
			appAdmin := createSession(true, nil)

			// Create against the seeded org (admin-rpc-org).
			createResp := postConnect("/shepherd.mgmt.v1.AdminService/CreateAgentIdentity", map[string]any{
				"issuer": "https://idp.example/", "appId": "client-eu", "org": "admin-rpc-org",
				"clusters": []string{"prod-eu-1"}, "roles": []string{},
			}, appAdmin)
			Expect(createResp.StatusCode).To(Equal(http.StatusOK))
			created := decodeBody(createResp)
			Expect(created["appId"]).To(Equal("client-eu"))
			Expect(created["orgName"]).To(Equal("admin-rpc-org"))

			listResp := postConnect("/shepherd.mgmt.v1.AdminService/ListAgentIdentities", map[string]any{}, appAdmin)
			Expect(listResp.StatusCode).To(Equal(http.StatusOK))
			items, _ := decodeBody(listResp)["items"].([]any) //nolint:errcheck // shape known
			Expect(items).To(HaveLen(1))

			delResp := postConnect("/shepherd.mgmt.v1.AdminService/DeleteAgentIdentity", map[string]any{
				"issuer": "https://idp.example/", "appId": "client-eu",
			}, appAdmin)
			Expect(delResp.StatusCode).To(Equal(http.StatusOK))

			// Deleting again is a not-found.
			delAgain := postConnect("/shepherd.mgmt.v1.AdminService/DeleteAgentIdentity", map[string]any{
				"issuer": "https://idp.example/", "appId": "client-eu",
			}, appAdmin)
			Expect(delAgain.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("rejects an unknown org and a non-app-admin caller", func() {
			appAdmin := createSession(true, nil)
			bad := postConnect("/shepherd.mgmt.v1.AdminService/CreateAgentIdentity", map[string]any{
				"issuer": "https://idp/", "appId": "c", "org": "no-such-org",
			}, appAdmin)
			Expect(bad.StatusCode).To(Equal(http.StatusBadRequest))

			nonAdmin := createSession(false, []string{"some-group"})
			denied := postConnect("/shepherd.mgmt.v1.AdminService/ListAgentIdentities", map[string]any{}, nonAdmin)
			Expect(denied.StatusCode).To(Equal(http.StatusForbidden))
		})
	})

	Describe("MeService", func() {
		It("returns the caller's identity and org memberships (happy path)", func() {
			cookie := createSession(false, []string{"admin-rpc-admin-group"})

			resp := postConnect("/shepherd.mgmt.v1.MeService/GetMe", map[string]any{}, cookie)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			Expect(resp.Header.Get("Cache-Control")).To(Equal("no-store, no-cache, must-revalidate"))

			payload := decodeBody(resp)
			Expect(payload["email"]).To(Equal("admin-user@example.com"))
			orgs, ok := payload["orgs"].([]any)
			Expect(ok).To(BeTrue(), "expected an orgs array")
			Expect(orgs).To(HaveLen(1))
			org, ok := orgs[0].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(org["id"]).To(Equal(orgIDStr))
			Expect(org["role"]).To(Equal("admin"))
		})

		It("reports the attribute-matching rollout flags on the org membership (#139)", func() {
			appAdmin := createSession(true, nil)
			updateResp := postConnect("/shepherd.mgmt.v1.AdminService/UpdateOrg", map[string]any{
				"orgId": orgIDStr, "displayName": "Admin RPC Org", "adminGroupId": "admin-rpc-admin-group",
				"allowLabelMatching": true, "allowLocalAttributeMatching": true,
			}, appAdmin)
			Expect(updateResp.StatusCode).To(Equal(http.StatusOK))

			cookie := createSession(false, []string{"admin-rpc-admin-group"})
			resp := postConnect("/shepherd.mgmt.v1.MeService/GetMe", map[string]any{}, cookie)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			payload := decodeBody(resp)
			orgs, ok := payload["orgs"].([]any)
			Expect(ok).To(BeTrue(), "expected an orgs array")
			org, ok := orgs[0].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(org["allowLabelMatching"]).To(BeTrue())
			Expect(org["allowLocalAttributeMatching"]).To(BeTrue())
		})

		It("denies GetMe for a request with no session with the Connect unauthenticated code", func() {
			resp := postConnect("/shepherd.mgmt.v1.MeService/GetMe", map[string]any{}, nil)
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))

			payload := decodeBody(resp)
			Expect(payload["code"]).To(Equal("unauthenticated"))
		})
	})
})
