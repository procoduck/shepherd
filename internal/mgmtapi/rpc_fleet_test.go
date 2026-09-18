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
	"github.com/prometheus/client_golang/prometheus/testutil"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/metrics"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Connect-path coverage for FleetService, over the same
// newRPCWiringRouter(...) wiring rpc_wiring_test.go proves 404-guards and
// unimplemented-stub behavior with — this suite exercises the now-migrated
// FleetService methods themselves: a happy path, an authz denial, and an
// error-code mapping case, per docs/archive/api-contract-design.md's testing rules.
// collectors_metadata_test.go and mgmtapi_test.go cover the REST shim paths
// (the compatibility oracle) and are unchanged by this migration.
var _ = Describe("shepherd.mgmt.v1.FleetService RPC", Label("integration"), func() {
	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		authHandler *auth.Handler
		server      *httptest.Server
		orgID       pgtype.UUID
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name:          "fleet-rpc-org",
			DisplayName:   "Fleet RPC Org",
			AdminGroupID:  "fleet-admin-group",
			ReaderGroupID: pgtype.Text{String: "fleet-reader-group", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID

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
		sessionID := fmt.Sprintf("fleet-rpc-session-%d", time.Now().UnixNano())
		_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: sessionID, UserOid: "fleet-user-oid", Email: "fleet-user@example.com", DisplayName: "Fleet User",
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

	It("lists collectors for an org-scoped reader (happy path)", func() {
		cluster, err := st.Queries.UpsertCluster(ctx, "fleet-rpc-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgID})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())

		cookie := createSession(false, []string{"fleet-reader-group"})
		resp := postConnect("/shepherd.mgmt.v1.FleetService/ListCollectors", map[string]any{"orgId": orgID.String()}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		payload := decodeBody(resp)
		items, ok := payload["items"].([]any)
		Expect(ok).To(BeTrue(), "expected an items array")
		Expect(items).To(HaveLen(1))
		item, ok := items[0].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(item["id"]).To(Equal(collector.ID.String()))
		Expect(item["cluster"]).To(Equal("fleet-rpc-cluster"))
	})

	It("persists grouping labels independently of reported Alloy attributes", func() {
		cluster, err := st.Queries.UpsertCluster(ctx, "labels-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgID})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())
		cookie := createSession(false, []string{"fleet-admin-group"})
		for key, value := range map[string]string{"team": "payments", "environment": "production"} {
			resp := postConnect("/shepherd.mgmt.v1.FleetService/SetCollectorLabel", map[string]any{
				"orgId": orgID.String(), "collectorId": collector.ID.String(), "key": key, "value": value,
			}, cookie)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			decodeBody(resp)
		}
		// The same upserts used on each Alloy poll must not overwrite UI labels.
		_, err = st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.UpsertCollectorInstance(ctx, sqlc.UpsertCollectorInstanceParams{
			ID: "labels-instance", CollectorID: collector.ID, Name: "node-a",
			LocalAttributes: json.RawMessage(`{"team":"alloy-team","custom.attribute":"visible"}`),
		})
		Expect(err).NotTo(HaveOccurred())
		readCookie := createSession(false, []string{"fleet-reader-group"})
		resp := postConnect("/shepherd.mgmt.v1.FleetService/GetCollector", map[string]any{
			"orgId": orgID.String(), "id": collector.ID.String(),
		}, readCookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		payload := decodeBody(resp)
		Expect(payload["labels"]).To(HaveKeyWithValue("team", "payments"))
		Expect(payload["localAttributes"]).To(HaveKeyWithValue("team", "alloy-team"))
		Expect(payload["localAttributes"]).To(HaveKeyWithValue("custom.attribute", "visible"))
		resp = postConnect("/shepherd.mgmt.v1.FleetService/ListCollectors", map[string]any{"orgId": orgID.String()}, readCookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		items, ok := decodeBody(resp)["items"].([]any)
		Expect(ok).To(BeTrue())
		Expect(items).To(HaveLen(1))
		Expect(items[0]).NotTo(HaveKey("localAttributes"), "list responses omit lossy dynamic attributes; detail retains them")
		Expect(items[0]).To(HaveKeyWithValue("labels", HaveKeyWithValue("team", "payments")))
		resp = postConnect("/shepherd.mgmt.v1.FleetService/SetCollectorLabel", map[string]any{
			"orgId": orgID.String(), "collectorId": collector.ID.String(), "key": "team", "value": "platform",
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(decodeBody(resp)["labels"]).To(HaveKeyWithValue("team", "platform"))
		resp = postConnect("/shepherd.mgmt.v1.FleetService/DeleteCollectorLabel", map[string]any{
			"orgId": orgID.String(), "collectorId": collector.ID.String(), "key": "team",
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		labels := decodeBody(resp)["labels"]
		Expect(labels).NotTo(HaveKey("team"))
		Expect(labels).To(HaveKeyWithValue("environment", "production"))
	})

	It("marks the per-collector serve cache dirty on label mutations", func() {
		cluster, err := st.Queries.UpsertCluster(ctx, "labels-cache-dirty")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgID})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())
		cookie := createSession(false, []string{"fleet-admin-group"})

		// Seed a clean (not-dirty) serve_cache row, as if this collector was
		// already served, so a label mutation is the only thing that could
		// dirty it again.
		_, err = st.Queries.UpsertServeCacheConditional(ctx, sqlc.UpsertServeCacheConditionalParams{
			CollectorID: collector.ID, Content: "v1", Hash: "h-v1", DirtySeq: 0,
		})
		Expect(err).NotTo(HaveOccurred())
		row, err := st.Queries.GetServeCache(ctx, collector.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(row.Dirty).To(BeFalse())

		resp := postConnect("/shepherd.mgmt.v1.FleetService/SetCollectorLabel", map[string]any{
			"orgId": orgID.String(), "collectorId": collector.ID.String(), "key": "team", "value": "payments",
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		decodeBody(resp)
		row, err = st.Queries.GetServeCache(ctx, collector.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(row.Dirty).To(BeTrue(), "SetCollectorLabel must mark this collector's serve cache dirty")
		dirtySeqAfterSet := row.DirtySeq

		// Clear the flag again so the next assertion isn't riding on the
		// mark SetCollectorLabel just made.
		_, err = st.Queries.UpsertServeCacheConditional(ctx, sqlc.UpsertServeCacheConditionalParams{
			CollectorID: collector.ID, Content: "v2", Hash: "h-v2", DirtySeq: dirtySeqAfterSet,
		})
		Expect(err).NotTo(HaveOccurred())
		row, err = st.Queries.GetServeCache(ctx, collector.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(row.Dirty).To(BeFalse())

		resp = postConnect("/shepherd.mgmt.v1.FleetService/DeleteCollectorLabel", map[string]any{
			"orgId": orgID.String(), "collectorId": collector.ID.String(), "key": "team",
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		decodeBody(resp)
		row, err = st.Queries.GetServeCache(ctx, collector.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(row.Dirty).To(BeTrue(), "DeleteCollectorLabel must mark this collector's serve cache dirty")
	})

	It("rejects invalid label keys and prevents reader and cross-org label writes", func() {
		cluster, err := st.Queries.UpsertCluster(ctx, "labels-permissions")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgID})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())
		request := map[string]any{"orgId": orgID.String(), "collectorId": collector.ID.String(), "key": "team", "value": "payments"}
		for _, method := range []string{"SetCollectorLabel", "DeleteCollectorLabel"} {
			body := map[string]any{"orgId": orgID.String(), "collectorId": collector.ID.String(), "key": "team"}
			resp := postConnect("/shepherd.mgmt.v1.FleetService/"+method, body, createSession(false, []string{"fleet-reader-group"}))
			Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
			decodeBody(resp)
			body["orgId"] = "00000000-0000-0000-0000-000000000001"
			resp = postConnect("/shepherd.mgmt.v1.FleetService/"+method, body, createSession(true, nil))
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
			decodeBody(resp)
		}
		for _, key := range []string{"", "bad key", "bad:key", strings.Repeat("k", 129)} {
			request["key"] = key
			resp := postConnect("/shepherd.mgmt.v1.FleetService/SetCollectorLabel", request, createSession(true, nil))
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			decodeBody(resp)
			resp = postConnect("/shepherd.mgmt.v1.FleetService/DeleteCollectorLabel", map[string]any{
				"orgId": orgID.String(), "collectorId": collector.ID.String(), "key": key,
			}, createSession(true, nil))
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			decodeBody(resp)
		}
		request["key"] = "team"
		for _, value := range []string{"", strings.Repeat("v", 513), "hidden\u200bvalue"} {
			request["value"] = value
			resp := postConnect("/shepherd.mgmt.v1.FleetService/SetCollectorLabel", request, createSession(true, nil))
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			decodeBody(resp)
		}
	})

	It("rejects reserved label keys at write time", func() {
		cluster, err := st.Queries.UpsertCluster(ctx, "labels-reserved")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgID})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())
		cookie := createSession(true, nil)
		for _, key := range []string{"role", "cluster", "id", "os", "alloy_version", "collector.foo", "shepherd.foo", "ROLE"} {
			resp := postConnect("/shepherd.mgmt.v1.FleetService/SetCollectorLabel", map[string]any{
				"orgId": orgID.String(), "collectorId": collector.ID.String(), "key": key, "value": "x",
			}, cookie)
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest), "key %q must be rejected as reserved", key)
			decodeBody(resp)
		}
		// A non-reserved key must still be settable — the reserved check must
		// not have swallowed the ordinary path.
		resp := postConnect("/shepherd.mgmt.v1.FleetService/SetCollectorLabel", map[string]any{
			"orgId": orgID.String(), "collectorId": collector.ID.String(), "key": "team", "value": "platform",
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(decodeBody(resp)["labels"]).To(HaveKeyWithValue("team", "platform"))
	})

	It("normalizes label keys and enforces the per-collector label cap", func() {
		cluster, err := st.Queries.UpsertCluster(ctx, "labels-cap")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgID})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())
		cookie := createSession(false, []string{"fleet-admin-group"})
		for i := range 64 {
			resp := postConnect("/shepherd.mgmt.v1.FleetService/SetCollectorLabel", map[string]any{
				"orgId": orgID.String(), "collectorId": collector.ID.String(), "key": fmt.Sprintf("key-%02d", i), "value": "set",
			}, cookie)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			decodeBody(resp)
		}
		resp := postConnect("/shepherd.mgmt.v1.FleetService/SetCollectorLabel", map[string]any{
			"orgId": orgID.String(), "collectorId": collector.ID.String(), "key": "KEY-00", "value": "updated",
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(decodeBody(resp)["labels"]).To(HaveKeyWithValue("key-00", "updated"))

		// A value change is auditable on its own: labels are not versioned, so
		// the audit row is the only record of what the value was and became.
		auditRows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Limit: 100})
		Expect(err).NotTo(HaveOccurred())
		var setDetail map[string]string
		for i := range auditRows {
			if auditRows[i].Action != "collector.label.set" {
				continue
			}
			var d map[string]string
			if json.Unmarshal(auditRows[i].Detail, &d) == nil && d["key"] == "key-00" && d["value"] == "updated" {
				setDetail = d
				break
			}
		}
		Expect(setDetail).NotTo(BeNil(), "expected an audit row for the key-00 value update")
		Expect(setDetail).To(HaveKeyWithValue("value", "updated"))
		Expect(setDetail).To(HaveKeyWithValue("previous_value", "set"))

		resp = postConnect("/shepherd.mgmt.v1.FleetService/SetCollectorLabel", map[string]any{
			"orgId": orgID.String(), "collectorId": collector.ID.String(), "key": "overflow", "value": "refused",
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusTooManyRequests))
		decodeBody(resp)
	})

	It("scopes ListCollectors to the requested org for an app-admin session, not every org", func() {
		cluster, err := st.Queries.UpsertCluster(ctx, "fleet-rpc-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgID})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())

		otherOrg, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name:          "fleet-rpc-other-org",
			DisplayName:   "Fleet RPC Other Org",
			AdminGroupID:  "fleet-other-admin-group",
			ReaderGroupID: pgtype.Text{String: "fleet-other-reader-group", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		otherCluster, err := st.Queries.UpsertCluster(ctx, "fleet-rpc-other-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: otherCluster.ID, OrgID: otherOrg.ID})).To(Succeed())
		_, err = st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: otherCluster.ID, Role: "logs"})
		Expect(err).NotTo(HaveOccurred())

		cookie := createSession(true, nil)
		resp := postConnect("/shepherd.mgmt.v1.FleetService/ListCollectors", map[string]any{"orgId": orgID.String()}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		payload := decodeBody(resp)
		items, ok := payload["items"].([]any)
		Expect(ok).To(BeTrue(), "expected an items array")
		Expect(items).To(HaveLen(1), "app-admin ListCollectors must return only the requested org's collectors")
		item, ok := items[0].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(item["id"]).To(Equal(collector.ID.String()))
	})

	It("denies ListCollectors for an authenticated session with no access to the org", func() {
		cookie := createSession(false, []string{"some-other-group"})
		resp := postConnect("/shepherd.mgmt.v1.FleetService/ListCollectors", map[string]any{"orgId": orgID.String()}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))

		payload := decodeBody(resp)
		Expect(payload["code"]).To(Equal("permission_denied"))
	})

	It("maps a not-found collector to the Connect not_found code (404)", func() {
		cookie := createSession(true, nil)
		missingID := "00000000-0000-0000-0000-000000000000"
		resp := postConnect("/shepherd.mgmt.v1.FleetService/GetCollector", map[string]any{
			"orgId": orgID.String(),
			"id":    missingID,
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusNotFound))

		payload := decodeBody(resp)
		Expect(payload["code"]).To(Equal("not_found"))
	})

	It("creates, lists, and deletes a collector's group assignments (org-admin)", func() {
		cluster, err := st.Queries.UpsertCluster(ctx, "fleet-rpc-assign-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgID})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())

		cookie := createSession(false, []string{"fleet-admin-group"})

		// No assignments yet.
		resp := postConnect("/shepherd.mgmt.v1.FleetService/ListAssignments", map[string]any{
			"orgId":       orgID.String(),
			"collectorId": collector.ID.String(),
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		payload := decodeBody(resp)
		Expect(payload["items"]).To(BeNil())
		Expect(payload["total"]).To(BeNil()) // omitempty: zero total is omitted, not rendered as 0

		// Create one.
		resp = postConnect("/shepherd.mgmt.v1.FleetService/CreateAssignment", map[string]any{
			"orgId":            orgID.String(),
			"collectorId":      collector.ID.String(),
			"groupId":          "readers-group",
			"groupDisplayName": "Readers",
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		// It shows up in the list.
		resp = postConnect("/shepherd.mgmt.v1.FleetService/ListAssignments", map[string]any{
			"orgId":       orgID.String(),
			"collectorId": collector.ID.String(),
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		payload = decodeBody(resp)
		items, ok := payload["items"].([]any)
		Expect(ok).To(BeTrue(), "expected an items array")
		Expect(items).To(HaveLen(1))
		item, ok := items[0].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(item["groupId"]).To(Equal("readers-group"))
		Expect(item["groupDisplayName"]).To(Equal("Readers"))

		// Delete it.
		resp = postConnect("/shepherd.mgmt.v1.FleetService/DeleteAssignment", map[string]any{
			"orgId":       orgID.String(),
			"collectorId": collector.ID.String(),
			"groupId":     "readers-group",
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		// Gone from the list.
		resp = postConnect("/shepherd.mgmt.v1.FleetService/ListAssignments", map[string]any{
			"orgId":       orgID.String(),
			"collectorId": collector.ID.String(),
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		payload = decodeBody(resp)
		Expect(payload["items"]).To(BeNil())
	})

	It("denies ListAssignments for an org reader (org-admin only)", func() {
		cluster, err := st.Queries.UpsertCluster(ctx, "fleet-rpc-assign-deny-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgID})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())

		cookie := createSession(false, []string{"fleet-reader-group"})
		resp := postConnect("/shepherd.mgmt.v1.FleetService/ListAssignments", map[string]any{
			"orgId":       orgID.String(),
			"collectorId": collector.ID.String(),
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
	})

	// LABEL-MATCHING-PLAN.md §7 / PR-5: a label mutation that flips which
	// pipelines match a collector must emit shepherd_pipeline_match_changes_total
	// and a "pipeline.match.changed" audit row — but only for an org that has
	// opted into allow_label_matching, and only on an actual flip, not every
	// label write.
	It("emits match-drift metrics and audit rows only for a real flip, only once allow_label_matching is on", func() {
		cluster, err := st.Queries.UpsertCluster(ctx, "match-drift-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgID})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())
		cookie := createSession(true, nil)

		_, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgID, Name: "match-drift-pipe", Contents: "// match-drift-pipe",
			Matchers: json.RawMessage(`["team=\"platform\""]`), Enabled: true, Source: "ui",
			CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		matchChangedRows := func() []sqlc.AuditLog {
			rows, listErr := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Limit: 100})
			Expect(listErr).NotTo(HaveOccurred())
			var out []sqlc.AuditLog
			for _, r := range rows {
				if r.Action == "pipeline.match.changed" {
					out = append(out, r)
				}
			}
			return out
		}

		// Flag off (default): setting the matching label must produce no
		// audit row and no metric movement, even though the pipeline's
		// matcher would flip if the flag were on.
		addedBefore := testutil.ToFloat64(metrics.PipelineMatchChangesTotal.WithLabelValues("added"))
		resp := postConnect("/shepherd.mgmt.v1.FleetService/SetCollectorLabel", map[string]any{
			"orgId": orgID.String(), "collectorId": collector.ID.String(), "key": "team", "value": "platform",
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		decodeBody(resp)
		Expect(testutil.ToFloat64(metrics.PipelineMatchChangesTotal.WithLabelValues("added"))).To(Equal(addedBefore),
			"flag off: no match-drift metric movement")
		Expect(matchChangedRows()).To(BeEmpty(), "flag off: no match-drift audit row")

		// Turn the flag on. The collector already carries team=platform from
		// above, so the pipeline is already matching -- flipping the flag
		// itself goes through no hook here (§7 only fires on a mutation), so
		// no drift row should appear from this step alone.
		_, err = st.Queries.UpdateOrg(ctx, sqlc.UpdateOrgParams{
			ID: orgID, DisplayName: "Fleet RPC Org", AdminGroupID: "fleet-admin-group",
			ReaderGroupID:      pgtype.Text{String: "fleet-reader-group", Valid: true},
			AllowLabelMatching: true,
		})
		Expect(err).NotTo(HaveOccurred())

		// An edit to an unrelated key must not be reported as a flip.
		addedBefore = testutil.ToFloat64(metrics.PipelineMatchChangesTotal.WithLabelValues("added"))
		resp = postConnect("/shepherd.mgmt.v1.FleetService/SetCollectorLabel", map[string]any{
			"orgId": orgID.String(), "collectorId": collector.ID.String(), "key": "unrelated", "value": "x",
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		decodeBody(resp)
		Expect(testutil.ToFloat64(metrics.PipelineMatchChangesTotal.WithLabelValues("added"))).To(Equal(addedBefore),
			"an unrelated key change must not be reported as a flip")

		// Flip the pipeline out of matching (team: platform -> staging): "removed".
		removedBefore := testutil.ToFloat64(metrics.PipelineMatchChangesTotal.WithLabelValues("removed"))
		resp = postConnect("/shepherd.mgmt.v1.FleetService/SetCollectorLabel", map[string]any{
			"orgId": orgID.String(), "collectorId": collector.ID.String(), "key": "team", "value": "staging",
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		decodeBody(resp)
		Expect(testutil.ToFloat64(metrics.PipelineMatchChangesTotal.WithLabelValues("removed"))).To(Equal(removedBefore + 1))
		removedRows := matchChangedRows()
		Expect(removedRows).To(HaveLen(1))
		var detail map[string]string
		Expect(json.Unmarshal(removedRows[0].Detail, &detail)).To(Succeed())
		Expect(detail).To(HaveKeyWithValue("pipeline_name", "match-drift-pipe"))
		Expect(detail).To(HaveKeyWithValue("collector_id", collector.ID.String()))
		Expect(detail).To(HaveKeyWithValue("direction", "removed"))
		Expect(detail).To(HaveKeyWithValue("cause", "collector.label.set"))

		// Flip it back into matching (team: staging -> platform): "added".
		addedBefore = testutil.ToFloat64(metrics.PipelineMatchChangesTotal.WithLabelValues("added"))
		resp = postConnect("/shepherd.mgmt.v1.FleetService/SetCollectorLabel", map[string]any{
			"orgId": orgID.String(), "collectorId": collector.ID.String(), "key": "team", "value": "platform",
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		decodeBody(resp)
		Expect(testutil.ToFloat64(metrics.PipelineMatchChangesTotal.WithLabelValues("added"))).To(Equal(addedBefore + 1))

		// Deleting the label flips it back out again: "removed", cause reflects the delete.
		removedBefore = testutil.ToFloat64(metrics.PipelineMatchChangesTotal.WithLabelValues("removed"))
		resp = postConnect("/shepherd.mgmt.v1.FleetService/DeleteCollectorLabel", map[string]any{
			"orgId": orgID.String(), "collectorId": collector.ID.String(), "key": "team",
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		decodeBody(resp)
		Expect(testutil.ToFloat64(metrics.PipelineMatchChangesTotal.WithLabelValues("removed"))).To(Equal(removedBefore + 1))
		allRows := matchChangedRows()
		Expect(len(allRows)).To(BeNumerically(">=", 2))
		mostRecent := allRows[0] // ListAuditLog orders ORDER BY at DESC, newest first.
		Expect(json.Unmarshal(mostRecent.Detail, &detail)).To(Succeed())
		Expect(detail).To(HaveKeyWithValue("cause", "collector.label.delete"))
	})
})
