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
		// startServer (re)builds the RPC server with agent.inactive_after
		// set to inactiveAfter; BeforeEach starts it at 5m, the chart's value.
		startServer func(inactiveAfter time.Duration)
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

		startServer = func(inactiveAfter time.Duration) {
			if server != nil {
				server.Close()
			}
			cfg := &config.Config{
				Auth: config.AuthConfig{InsecureCookies: true},
				Validate: config.ValidateConfig{
					AlloyBinary: "", StabilityLevel: "experimental", Timeout: 10e9,
				},
				Agent: config.AgentConfig{InactiveAfter: inactiveAfter},
			}
			authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
			server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
		}
		server = nil
		startServer(5 * time.Minute)
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

	// #237: "inactive" is derived from last_seen at read time, independent of
	// the agent's stored outcome. On Kubernetes every Alloy restart registers a
	// NEW instance id, so a replaced pod's FAILED row is never reconnected —
	// it must stop reading FAILED once it is stale, without the outcome being
	// overwritten (which would let a reconnect promote it to APPLIED).
	setStatus := func(id, status, errMsg string) {
		Expect(st.Queries.UpdateInstanceStatus(ctx, sqlc.UpdateInstanceStatusParams{
			ID:                 id,
			RemoteConfigStatus: pgtype.Text{String: status, Valid: true},
			RemoteConfigError:  pgtype.Text{String: errMsg, Valid: errMsg != ""},
		})).To(Succeed())
	}
	// RAW-SQL-OK: backdating last_seen to make an instance stale — no sqlc
	// query takes an explicit last_seen (UpsertCollectorInstance writes
	// now()), and this is a _test.go file.
	backdate := func(id string, ago time.Duration) {
		_, err := st.Pool().Exec(ctx,
			`UPDATE collector_instances SET last_seen = now() - make_interval(secs => $2) WHERE id = $1`,
			id, ago.Seconds())
		Expect(err).NotTo(HaveOccurred())
	}
	listItem := func(collectorID pgtype.UUID) map[string]any {
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.FleetService/ListCollectors", adminCookie,
			map[string]any{"orgId": orgID})
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var result struct {
			Items []map[string]any `json:"items"`
		}
		Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
		for _, item := range result.Items {
			if item["id"] == collectorID.String() {
				return item
			}
		}
		Fail("collector missing from the list response")
		return nil
	}
	instanceByName := func(result map[string]any, name string) map[string]any {
		instances, ok := result["instances"].([]any)
		Expect(ok).To(BeTrue(), "expected an instances array")
		for _, raw := range instances {
			inst, ok := raw.(map[string]any)
			Expect(ok).To(BeTrue())
			if inst["name"] == name {
				return inst
			}
		}
		Fail("instance " + name + " missing")
		return nil
	}

	Describe("inactive at read time (#237)", func() {
		It("presents a replaced pod's stale FAILED instance as inactive while the collector reads its live instance", func() {
			collector := createCollector("inactive-replaced-pod")
			upsertInstance("pod-a", collector.ID, "alloy-0-a", "v1.20.1", "linux", `{}`)
			setStatus("pod-a", "FAILED", "40:3: Failed to build component")
			backdate("pod-a", 10*time.Minute)
			upsertInstance("pod-b", collector.ID, "alloy-0-b", "v1.20.1", "linux", `{}`)
			setStatus("pod-b", "APPLIED", "")

			result := getCollector(collector.ID)
			a := instanceByName(result, "alloy-0-a")
			Expect(a["remoteConfigStatus"]).To(Equal("inactive"),
				"a pod that stopped checking in must not keep reading FAILED until delete_after")
			Expect(a).NotTo(HaveKey("remoteConfigError"), "the stale outcome's error is not the presented state")
			Expect(instanceByName(result, "alloy-0-b")["remoteConfigStatus"]).To(Equal("APPLIED"))
			Expect(result["remoteConfigStatus"]).To(Equal("APPLIED"), "the collector reads its live instance")
			Expect(listItem(collector.ID)["remoteConfigStatus"]).To(Equal("APPLIED"))

			// The outcome is presented, never overwritten.
			stored, err := st.Queries.GetCollectorInstanceByID(ctx, "pod-a")
			Expect(err).NotTo(HaveOccurred())
			Expect(stored.RemoteConfigStatus.String).To(Equal("FAILED"))
			Expect(stored.RemoteConfigError.String).To(Equal("40:3: Failed to build component"))
		})

		It("presents a collector whose every instance is stale as inactive", func() {
			collector := createCollector("inactive-all-stale")
			upsertInstance("gone-1", collector.ID, "gone-1", "v1.20.1", "linux", `{}`)
			setStatus("gone-1", "APPLIED", "")
			backdate("gone-1", time.Hour)

			Expect(getCollector(collector.ID)["remoteConfigStatus"]).To(Equal("inactive"))
			Expect(listItem(collector.ID)["remoteConfigStatus"]).To(Equal("inactive"))
		})

		It("shows the stored FAILED again, not APPLIED, once a stale instance checks back in", func() {
			collector := createCollector("inactive-reconnect")
			upsertInstance("flaky", collector.ID, "flaky", "v1.20.1", "linux", `{}`)
			setStatus("flaky", "FAILED", "dial tcp: no such host")
			backdate("flaky", time.Hour)
			Expect(instanceByName(getCollector(collector.ID), "flaky")["remoteConfigStatus"]).To(Equal("inactive"))

			upsertInstance("flaky", collector.ID, "flaky", "v1.20.1", "linux", `{}`)

			inst := instanceByName(getCollector(collector.ID), "flaky")
			Expect(inst["remoteConfigStatus"]).To(Equal("FAILED"))
			Expect(inst["remoteConfigError"]).To(Equal("dial tcp: no such host"))
		})

		It("never presents an instance as inactive when agent.inactive_after is unset", func() {
			startServer(0)
			collector := createCollector("inactive-unset")
			upsertInstance("old", collector.ID, "old", "v1.20.1", "linux", `{}`)
			setStatus("old", "FAILED", "boom")
			backdate("old", 30*24*time.Hour)

			result := getCollector(collector.ID)
			Expect(instanceByName(result, "old")["remoteConfigStatus"]).To(Equal("FAILED"))
			Expect(result["remoteConfigStatus"]).To(Equal("FAILED"))
			Expect(listItem(collector.ID)["remoteConfigStatus"]).To(Equal("FAILED"))
		})
	})

	// An APPLIED is a claim about the config it was reported with
	// (remote_config_status_hash). docs/proofs/applied-status.md (d)/(e): for
	// a poll interval after Shepherd serves a new config the stored row still
	// says APPLIED about the previous one, including while Alloy refuses the
	// new config. The agentapi suite replays the captured sequences end to
	// end; these pin the presentation rule itself.
	Describe("APPLIED about an earlier config than the one served", func() {
		setStatusFor := func(id, status, errMsg, hash string) {
			Expect(st.Queries.UpdateInstanceStatus(ctx, sqlc.UpdateInstanceStatusParams{
				ID:                 id,
				RemoteConfigStatus: pgtype.Text{String: status, Valid: true},
				RemoteConfigError:  pgtype.Text{String: errMsg, Valid: errMsg != ""},
				StatusHash:         pgtype.Text{String: hash, Valid: hash != ""},
			})).To(Succeed())
		}
		// serve makes hash the collector's served config the way a
		// recompute does: mark dirty, then the compare-and-swap write.
		serve := func(collectorID pgtype.UUID, hash string) {
			Expect(st.Queries.MarkServeCacheDirty(ctx, collectorID)).To(Succeed())
			cache, err := st.Queries.GetServeCache(ctx, collectorID)
			Expect(err).NotTo(HaveOccurred())
			_, err = st.Queries.UpsertServeCacheConditional(ctx, sqlc.UpsertServeCacheConditionalParams{
				CollectorID: collectorID, Content: "// " + hash, Hash: hash, DirtySeq: cache.DirtySeq,
			})
			Expect(err).NotTo(HaveOccurred())
		}
		presented := func(collectorID pgtype.UUID, instance string) (string, any) {
			result := getCollector(collectorID)
			inst := instanceByName(result, instance)
			Expect(result["remoteConfigStatus"]).To(Equal(inst["remoteConfigStatus"]))
			Expect(listItem(collectorID)["remoteConfigStatus"]).To(Equal(inst["remoteConfigStatus"]))
			status, ok := inst["remoteConfigStatus"].(string)
			Expect(ok).To(BeTrue(), "expected a remoteConfigStatus string")
			return status, inst["remoteConfigError"]
		}

		It("presents APPLIED as APPLYING until the agent reports on the newly served config", func() {
			collector := createCollector("applied-earlier")
			upsertInstance("agent", collector.ID, "agent", "v1.20.1", "linux", `{}`)
			serve(collector.ID, "hash-a")
			setStatusFor("agent", "APPLIED", "", "hash-a")
			status, _ := presented(collector.ID, "agent")
			Expect(status).To(Equal("APPLIED"))

			serve(collector.ID, "hash-b")
			status, _ = presented(collector.ID, "agent")
			Expect(status).To(Equal("APPLYING"), "the APPLIED is about hash-a; hash-b is served")

			setStatusFor("agent", "FAILED", "1:1: invalid stage config logfmt mapping or regex is required", "hash-b")
			status, errMsg := presented(collector.ID, "agent")
			Expect(status).To(Equal("FAILED"))
			Expect(errMsg).To(Equal("1:1: invalid stage config logfmt mapping or regex is required"))

			serve(collector.ID, "hash-c")
			setStatusFor("agent", "APPLIED", "", "hash-c")
			status, _ = presented(collector.ID, "agent")
			Expect(status).To(Equal("APPLIED"))

			// Presented, never rewritten: the stored row still says APPLIED.
			serve(collector.ID, "hash-d")
			status, _ = presented(collector.ID, "agent")
			Expect(status).To(Equal("APPLYING"))
			stored, err := st.Queries.GetCollectorInstanceByID(ctx, "agent")
			Expect(err).NotTo(HaveOccurred())
			Expect(stored.RemoteConfigStatus.String).To(Equal("APPLIED"))
		})

		It("keeps a FAILED about an earlier config FAILED (F1)", func() {
			collector := createCollector("failed-earlier")
			upsertInstance("agent", collector.ID, "agent", "v1.20.1", "linux", `{}`)
			serve(collector.ID, "hash-a")
			setStatusFor("agent", "FAILED", "40:3: Failed to build component", "hash-a")
			serve(collector.ID, "hash-b")

			status, errMsg := presented(collector.ID, "agent")
			Expect(status).To(Equal("FAILED"))
			Expect(errMsg).To(Equal("40:3: Failed to build component"))
		})

		It("shows APPLIED as stored when it cannot tell which config it was about", func() {
			collector := createCollector("applied-unknown")
			upsertInstance("no-hash", collector.ID, "no-hash", "v1.20.1", "linux", `{}`)
			serve(collector.ID, "hash-a")
			setStatusFor("no-hash", "APPLIED", "", "") // a row from before 0028
			status, _ := presented(collector.ID, "no-hash")
			Expect(status).To(Equal("APPLIED"))

			other := createCollector("applied-nothing-served")
			upsertInstance("unserved", other.ID, "unserved", "v1.20.1", "linux", `{}`)
			setStatusFor("unserved", "APPLIED", "", "hash-a")
			status, _ = presented(other.ID, "unserved")
			Expect(status).To(Equal("APPLIED"), "no serve_cache row: nothing to compare against")

			Expect(st.Queries.MarkServeCacheDirty(ctx, other.ID)).To(Succeed()) // a '' placeholder row
			status, _ = presented(other.ID, "unserved")
			Expect(status).To(Equal("APPLIED"), "a dirty placeholder's empty hash is not a served config")
		})
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
