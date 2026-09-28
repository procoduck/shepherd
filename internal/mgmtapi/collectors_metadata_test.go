package mgmtapi_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Coverage for the collector detail/list instance metadata that GetCollector
// and ListCollectors now expose (R2-C3): name, alloy_version, os, last_seen,
// remote_config_status, remote_config_error, and local_attributes per
// instance, plus the latest-instance summary rolled up onto the collector
// itself and onto each list item.
var _ = Describe("Collector instance metadata", Label("integration"), func() {
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
			Name:          "metaorg",
			DisplayName:   "Meta Org",
			AdminGroupID:  "admin-group",
			ReaderGroupID: pgtype.Text{},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()

		cfg := &config.Config{
			Auth: config.AuthConfig{InsecureCookies: true},
			Validate: config.ValidateConfig{
				AlloyBinary: "", StabilityLevel: "experimental", Timeout: 10e9,
			},
		}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
		adminCookie = newAppAdminSession(ctx, st)
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	// createCollector claims a fresh cluster into the org and creates a
	// single "metrics" collector under it, returning the collector's ID.
	createCollector := func(clusterName string) sqlc.Collector {
		cluster, err := st.Queries.UpsertCluster(ctx, clusterName)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgUUID(orgID)})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())
		return collector
	}

	upsertInstance := func(id string, collectorID pgtype.UUID, name, version, os string, attrs string) sqlc.CollectorInstance {
		inst, err := st.Queries.UpsertCollectorInstance(ctx, sqlc.UpsertCollectorInstanceParams{
			ID:              id,
			CollectorID:     collectorID,
			Name:            name,
			LocalAttributes: json.RawMessage(attrs),
			AlloyVersion:    pgtype.Text{String: version, Valid: version != ""},
			Os:              pgtype.Text{String: os, Valid: os != ""},
		})
		Expect(err).NotTo(HaveOccurred())
		return inst
	}

	getCollector := func(id pgtype.UUID) map[string]any {
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.FleetService/GetCollector", adminCookie,
			map[string]any{"orgId": orgID, "id": id.String()})
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var result map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
		return result
	}

	Describe("FleetService/GetCollector", func() {
		It("includes the instances array with per-instance metadata, newest last_seen first", func() {
			collector := createCollector("meta-cluster-detail")

			upsertInstance("inst-older", collector.ID, "node-a", "v1.1.0", "linux", `{"env":"prod"}`)
			time.Sleep(10 * time.Millisecond)
			upsertInstance("inst-newer", collector.ID, "node-b", "v1.2.0", "darwin", `{"env":"prod"}`)
			Expect(st.Queries.UpdateInstanceStatus(ctx, sqlc.UpdateInstanceStatusParams{
				ID:                 "inst-newer",
				RemoteConfigStatus: pgtype.Text{String: "FAILED", Valid: true},
				RemoteConfigError:  pgtype.Text{String: "schema validation failed", Valid: true},
			})).To(Succeed())

			result := getCollector(collector.ID)

			Expect(result["cluster"]).To(Equal("meta-cluster-detail"))
			Expect(result["role"]).To(Equal("metrics"))

			instancesRaw, ok := result["instances"].([]any)
			Expect(ok).To(BeTrue(), "expected an instances array in the response")
			Expect(instancesRaw).To(HaveLen(2))

			newest, ok := instancesRaw[0].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(newest["name"]).To(Equal("node-b"))
			Expect(newest["alloyVersion"]).To(Equal("v1.2.0"))
			Expect(newest["os"]).To(Equal("darwin"))
			Expect(newest["remoteConfigStatus"]).To(Equal("FAILED"))
			Expect(newest["remoteConfigError"]).To(Equal("schema validation failed"))
			Expect(newest["lastSeen"]).NotTo(BeEmpty())
			Expect(newest["localAttributes"]).To(HaveKeyWithValue("env", "prod"))

			older, ok := instancesRaw[1].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(older["name"]).To(Equal("node-a"))

			// The collector-level fields roll up from the latest instance.
			Expect(result["alloyVersion"]).To(Equal("v1.2.0"))
			Expect(result["remoteConfigStatus"]).To(Equal("FAILED"))
			Expect(result["remoteConfigError"]).To(Equal("schema validation failed"))
			Expect(result["localAttributes"]).To(HaveKeyWithValue("env", "prod"))
			Expect(result["lastSeen"]).NotTo(BeEmpty())
		})

		It("omits unregistered instances from the instances array", func() {
			collector := createCollector("meta-cluster-unregistered")
			upsertInstance("inst-gone", collector.ID, "node-gone", "v1.0.0", "linux", `{}`)
			Expect(st.Queries.UnregisterInstance(ctx, "inst-gone")).To(Succeed())

			result := getCollector(collector.ID)
			instancesRaw, _ := result["instances"].([]any) //nolint:errcheck // absent field decodes to nil, asserted below
			Expect(instancesRaw).To(BeEmpty())
		})

		It("returns a collector with no reported instances yet without error", func() {
			collector := createCollector("meta-cluster-empty")

			result := getCollector(collector.ID)
			Expect(result["remoteConfigStatus"]).To(BeNil())
			Expect(result["lastSeen"]).To(BeNil())
		})

		// No byte-preservation spec for local_attributes here. The /api REST
		// shim spliced the stored jsonb bytes back onto the wire so numbers
		// kept their exact text; Connect carries local_attributes as a
		// google.protobuf.Struct, where every number is a float64 (an integer
		// beyond 2^53 or "1.500000"'s trailing zeros do not survive). That was
		// already what the SPA saw, and the shim that preserved the bytes was
		// removed with the rest of /api.
	})

	Describe("FleetService/ListCollectors", func() {
		It("includes each item's latest last_seen and alloy_version", func() {
			collector := createCollector("meta-cluster-list")
			upsertInstance("inst-list-1", collector.ID, "node-list", "v2.0.0", "linux", `{}`)

			resp := postConnectJSON(server, "/shepherd.mgmt.v1.FleetService/ListCollectors", adminCookie,
				map[string]any{"orgId": orgID})
			defer resp.Body.Close() //nolint:errcheck // test cleanup
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var result struct {
				Items []map[string]any `json:"items"`
			}
			Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())

			var found map[string]any
			for _, item := range result.Items {
				if item["id"] == collector.ID.String() {
					found = item
					break
				}
			}
			Expect(found).NotTo(BeNil(), "expected the created collector in the list response")
			Expect(found["alloyVersion"]).To(Equal("v2.0.0"))
			Expect(found["lastSeen"]).NotTo(BeEmpty())
		})
	})
})
