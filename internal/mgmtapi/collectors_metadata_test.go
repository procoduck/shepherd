package mgmtapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
		server = httptest.NewServer(newRESTRouter(st, authHandler, cfg, nil))
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

	Describe("GET /orgs/{org}/collectors/{id}", func() {
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

			resp := getRequest(server, fmt.Sprintf("/orgs/%s/collectors/%s", orgID, collector.ID.String()), adminCookie)
			defer resp.Body.Close() //nolint:errcheck // test cleanup
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var result map[string]any
			Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())

			Expect(result["cluster"]).To(Equal("meta-cluster-detail"))
			Expect(result["role"]).To(Equal("metrics"))

			instancesRaw, ok := result["instances"].([]any)
			Expect(ok).To(BeTrue(), "expected an instances array in the response")
			Expect(instancesRaw).To(HaveLen(2))

			newest, ok := instancesRaw[0].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(newest["name"]).To(Equal("node-b"))
			Expect(newest["alloy_version"]).To(Equal("v1.2.0"))
			Expect(newest["os"]).To(Equal("darwin"))
			Expect(newest["remote_config_status"]).To(Equal("FAILED"))
			Expect(newest["remote_config_error"]).To(Equal("schema validation failed"))
			Expect(newest["last_seen"]).NotTo(BeEmpty())
			Expect(newest["local_attributes"]).To(HaveKeyWithValue("env", "prod"))

			older, ok := instancesRaw[1].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(older["name"]).To(Equal("node-a"))

			// The collector-level fields roll up from the latest instance.
			Expect(result["alloy_version"]).To(Equal("v1.2.0"))
			Expect(result["remote_config_status"]).To(Equal("FAILED"))
			Expect(result["remote_config_error"]).To(Equal("schema validation failed"))
			Expect(result["local_attributes"]).To(HaveKeyWithValue("env", "prod"))
			Expect(result["last_seen"]).NotTo(BeEmpty())
		})

		It("omits unregistered instances from the instances array", func() {
			collector := createCollector("meta-cluster-unregistered")
			upsertInstance("inst-gone", collector.ID, "node-gone", "v1.0.0", "linux", `{}`)
			Expect(st.Queries.UnregisterInstance(ctx, "inst-gone")).To(Succeed())

			resp := getRequest(server, fmt.Sprintf("/orgs/%s/collectors/%s", orgID, collector.ID.String()), adminCookie)
			defer resp.Body.Close() //nolint:errcheck // test cleanup
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var result map[string]any
			Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
			instancesRaw, _ := result["instances"].([]any) //nolint:errcheck // absent field decodes to nil, asserted below
			Expect(instancesRaw).To(BeEmpty())
		})

		It("returns a collector with no reported instances yet without error", func() {
			collector := createCollector("meta-cluster-empty")

			resp := getRequest(server, fmt.Sprintf("/orgs/%s/collectors/%s", orgID, collector.ID.String()), adminCookie)
			defer resp.Body.Close() //nolint:errcheck // test cleanup
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var result map[string]any
			Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
			Expect(result["remote_config_status"]).To(BeNil())
			Expect(result["last_seen"]).To(BeNil())
		})

		// Contract-fidelity regression: the legacy handler passed the stored
		// local_attributes jsonb bytes straight onto the wire
		// (json.RawMessage). Decoding them into a google.protobuf.Struct and
		// re-marshaling through protojson is not byte-preserving — every
		// number becomes a Struct Value's float64 — so the REST shim must
		// substitute the untouched stored bytes back in, both on the
		// collector-level rollup and on each instance. Postgres's jsonb
		// storage itself normalizes object key order (verified separately),
		// so this asserts on what a Struct round-trip provably cannot
		// reproduce regardless of storage normalization: a fractional
		// value's exact decimal text, and an integer beyond float64's
		// 2^53 exact-integer range.
		It("preserves local_attributes number formatting and integer precision byte-for-byte", func() {
			collector := createCollector("meta-cluster-attrs")
			upsertInstance("inst-attrs", collector.ID, "node-attrs", "v1.0.0", "linux",
				`{"big":9007199254740993,"small":1.500000}`)

			resp := getRequest(server, fmt.Sprintf("/orgs/%s/collectors/%s", orgID, collector.ID.String()), adminCookie)
			defer resp.Body.Close() //nolint:errcheck // test cleanup
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())

			// A Struct round-trip would print "small":1.5 (trailing zeros
			// lost) and corrupt "big" to 9007199254740992 (float64 can't
			// exactly represent it) — checked as raw text, not a decoded
			// comparison, since decoding either side back through
			// encoding/json would itself launder the exact precision loss
			// this test exists to catch.
			var result struct {
				LocalAttributes json.RawMessage `json:"local_attributes"`
				Instances       []struct {
					LocalAttributes json.RawMessage `json:"local_attributes"`
				} `json:"instances"`
			}
			Expect(json.Unmarshal(body, &result)).To(Succeed())
			Expect(string(result.LocalAttributes)).To(ContainSubstring("9007199254740993"), "integers beyond 2^53 must not lose precision")
			Expect(string(result.LocalAttributes)).To(ContainSubstring("1.500000"), "trailing zeros must survive, not collapse to 1.5")
			Expect(result.Instances).To(HaveLen(1))
			Expect(string(result.Instances[0].LocalAttributes)).To(ContainSubstring("9007199254740993"))
			Expect(string(result.Instances[0].LocalAttributes)).To(ContainSubstring("1.500000"))
		})
	})

	Describe("GET /orgs/{org}/collectors", func() {
		It("includes each item's latest last_seen and alloy_version", func() {
			collector := createCollector("meta-cluster-list")
			upsertInstance("inst-list-1", collector.ID, "node-list", "v2.0.0", "linux", `{}`)

			resp := getRequest(server, fmt.Sprintf("/orgs/%s/collectors", orgID), adminCookie)
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
			Expect(found["alloy_version"]).To(Equal("v2.0.0"))
			Expect(found["last_seen"]).NotTo(BeEmpty())
		})
	})
})
