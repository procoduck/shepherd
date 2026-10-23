package agentapi_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"

	"github.com/golang/snappy"
	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/prometheus/prompb"

	"shepherd/internal/agentapi"
	"shepherd/internal/auth"
	"shepherd/internal/beacon"
	"shepherd/internal/config"
	"shepherd/internal/metrics"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// This is the G5 gate (docs/gateway-tier-plan.md §6): "Beacon ingest rejects
// unauthenticated writes, enforces the rate/size caps, and stores no raw
// samples" — as a Go integration suite against a real database, matching
// this repo's established convention (internal/store, internal/mgmtapi,
// this package's own CollectorService suite above) rather than a mock.
var _ = Describe("BeaconHandler", Label("integration"), func() {
	var (
		ctx        context.Context
		cancel     context.CancelFunc
		st         *store.Store
		server     *httptest.Server
		authHeader string
		tokenID    string
	)

	encodeWrite := func(labelSets [][]prompb.Label, value float64) []byte {
		wr := &prompb.WriteRequest{}
		for _, labels := range labelSets {
			wr.Timeseries = append(wr.Timeseries, prompb.TimeSeries{
				Labels:  labels,
				Samples: []prompb.Sample{{Value: value, Timestamp: 1000}},
			})
		}
		raw, err := wr.Marshal()
		Expect(err).NotTo(HaveOccurred())
		return snappy.Encode(nil, raw)
	}

	// controllerSeries is the label shape the pinned Alloy really emits (see
	// internal/beacon/project_test.go's ctrlSeries): controller_path is the
	// PARENT loader's path, controller_id the loader's own id.
	controllerSeries := func(instance, controllerPath, controllerID, healthType string) []prompb.Label {
		return []prompb.Label{
			{Name: "__name__", Value: "alloy_component_controller_running_components"},
			{Name: "controller_id", Value: controllerID},
			{Name: "controller_path", Value: controllerPath},
			{Name: "health_type", Value: healthType},
			{Name: "instance", Value: instance},
		}
	}
	// componentSeries is a series from the loader inside declare-wrapped
	// pipeline pipe under remotecfg.
	componentSeries := func(instance, pipe, healthType string) []prompb.Label {
		return controllerSeries(instance, "/remotecfg", pipe+".default", healthType)
	}
	healthySeries := func(instance, pipe string) []prompb.Label {
		return componentSeries(instance, pipe, "healthy")
	}

	startServer := func(limits beacon.Limits) {
		h := agentapi.NewBeaconHandler(st, slog.Default(), limits, nil)
		mux := http.NewServeMux()
		mux.Handle(agentapi.BeaconWritePath, h)
		server = httptest.NewServer(mux)
	}

	startServerWithVerifier := func(limits beacon.Limits, verify agentapi.AgentVerifier) {
		server.Close()
		h := agentapi.NewBeaconHandler(st, slog.Default(), limits, verify)
		mux := http.NewServeMux()
		mux.Handle(agentapi.BeaconWritePath, h)
		server = httptest.NewServer(mux)
	}

	postRaw := func(body []byte, header string) *http.Response {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+agentapi.BeaconWritePath, bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		resp, err := server.Client().Do(req)
		Expect(err).NotTo(HaveOccurred())
		return resp
	}

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())

		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		secret := base64.URLEncoding.EncodeToString(raw)
		hash := sha256.Sum256([]byte(secret))
		tok, err := st.Queries.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{
			Name: "beacon-test-token", TokenHash: hash[:], CreatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())
		tokenID = tok.ID.String()
		authHeader = "Basic " + base64.StdEncoding.EncodeToString([]byte(tokenID+":"+secret))

		startServer(beacon.DefaultLimits)
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	// Red run, executed: comment out the verifyBasicAuth failure branch in
	// BeaconHandler.ServeHTTP (internal/agentapi/beacon_handler.go) so the
	// request continues past it. Observed failure: this spec fails with 500
	// rather than the expected 401 — 500 and not 204 because the empty token
	// id then fails a downstream pgtype.UUID parse, an independent second
	// guard. That backstop is real but it is not the control: it refuses the
	// write by accident of the id being unparseable, not because the caller
	// was unauthenticated, so the verifyBasicAuth check is still the thing
	// G5 names.
	It("rejects an unauthenticated write", func() {
		body := encodeWrite([][]prompb.Label{healthySeries("10.0.0.1:12345", "pipe_x")}, 1)
		resp := postRaw(body, "")
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))

		rows, err := st.Queries.ListBeaconInventoryByPrincipal(ctx, tokenID)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(BeEmpty(), "an unauthenticated write must not reach storage")
	})

	It("rejects a write with a wrong secret", func() {
		badAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(tokenID+":wrong-secret"))
		body := encodeWrite([][]prompb.Label{healthySeries("10.0.0.1:12345", "pipe_x")}, 1)
		resp := postRaw(body, badAuth)
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
	})

	It("authenticates an OIDC Bearer write and keys inventory by the oidc principal", func() {
		verify := func(_ context.Context, raw string) (auth.AgentClaims, error) {
			if raw == "good-agent-token" {
				return auth.AgentClaims{Issuer: "https://idp/", Subject: "alloy-prod"}, nil
			}
			return auth.AgentClaims{}, errors.New("bad token")
		}
		startServerWithVerifier(beacon.DefaultLimits, verify)

		body := encodeWrite([][]prompb.Label{healthySeries("10.0.0.1:12345", "pipe_x")}, 1)
		resp := postRaw(body, "Bearer good-agent-token")
		Expect(resp.StatusCode).To(Equal(http.StatusNoContent))

		// Stored under the OIDC principal, not the seeded agent token.
		rows, err := st.Queries.ListBeaconInventoryByPrincipal(ctx, "oidc:https://idp/|alloy-prod")
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].ComponentName).To(Equal("pipe_x"))

		byToken, err := st.Queries.ListBeaconInventoryByPrincipal(ctx, tokenID)
		Expect(err).NotTo(HaveOccurred())
		Expect(byToken).To(BeEmpty(), "an OIDC write must not land under the agent-token principal")
	})

	It("rejects a Bearer write the verifier refuses, and one when OIDC is off", func() {
		verify := func(_ context.Context, raw string) (auth.AgentClaims, error) {
			return auth.AgentClaims{}, errors.New("bad token")
		}
		startServerWithVerifier(beacon.DefaultLimits, verify)
		body := encodeWrite([][]prompb.Label{healthySeries("10.0.0.1:12345", "pipe_x")}, 1)
		Expect(postRaw(body, "Bearer nope").StatusCode).To(Equal(http.StatusUnauthorized))

		// nil verifier = OIDC off: a Bearer header is unauthenticated.
		startServerWithVerifier(beacon.DefaultLimits, nil)
		Expect(postRaw(body, "Bearer good-agent-token").StatusCode).To(Equal(http.StatusUnauthorized))
	})

	// Red run: in internal/agentapi/beacon_handler.go, delete the
	// `r.Body = http.MaxBytesReader(...)` line AND change
	// `beacon.DecodeWriteRequest(r.Header, r.Body, h.limits.MaxBodyBytes)`
	// to pass a very large bound instead of h.limits.MaxBodyBytes (i.e.
	// remove both enforcement points doc.go describes). Observed failure:
	// this spec fails because a body far over DefaultLimits.MaxBodyBytes
	// gets a 204 instead of 413.
	It("enforces the body-size cap", func() {
		startServer(beacon.Limits{MaxBodyBytes: 256, RatePerSecond: 100, Burst: 100})
		// One series with a large padding label comfortably exceeds a 256-byte cap.
		big := healthySeries("10.0.0.1:12345", "pipe_x")
		big = append(big, prompb.Label{Name: "padding", Value: string(bytes.Repeat([]byte("x"), 4096))})
		body := encodeWrite([][]prompb.Label{big}, 1)
		resp := postRaw(body, authHeader)
		Expect(resp.StatusCode).To(Equal(http.StatusRequestEntityTooLarge))
	})

	// Red run: change RateLimiter.Allow (internal/beacon/limits.go) to
	// `return true` unconditionally. Observed failure: this spec fails
	// because every one of the burst-exceeding requests returns 204 instead
	// of at least one 429.
	It("enforces the per-instance rate limit", func() {
		startServer(beacon.Limits{MaxBodyBytes: 1 << 20, RatePerSecond: 1, Burst: 2})
		body := encodeWrite([][]prompb.Label{healthySeries("10.0.0.1:12345", "pipe_x")}, 1)

		var sawThrottled bool
		for i := 0; i < 6; i++ {
			resp := postRaw(body, authHeader)
			if resp.StatusCode == http.StatusTooManyRequests {
				sawThrottled = true
				break
			}
			Expect(resp.StatusCode).To(Equal(http.StatusNoContent))
		}
		Expect(sawThrottled).To(BeTrue(), "expected at least one request beyond Burst=2 to be rate-limited")
	})

	It("stores a projected component/health row and returns 204 — and stores nothing else", func() {
		body := encodeWrite([][]prompb.Label{
			healthySeries("10.0.0.9:12345", "pipe_minimal_scrape"),
			{
				// A decoy series carrying a real sample value under a metric
				// name Project does not recognize. If this ever leaked
				// through, it would prove the "discard" half of "parse,
				// project, discard" had failed.
				{Name: "__name__", Value: "alloy_build_info"},
				{Name: "instance", Value: "10.0.0.9:12345"},
			},
		}, 42)

		resp := postRaw(body, authHeader)
		Expect(resp.StatusCode).To(Equal(http.StatusNoContent))

		rows, err := st.Queries.ListBeaconInventoryByPrincipal(ctx, tokenID)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1), "the decoy alloy_build_info series must not create a second row")
		Expect(rows[0].InstanceLabel).To(Equal("10.0.0.9:12345"))
		Expect(rows[0].ComponentName).To(Equal("pipe_minimal_scrape"))
		Expect(rows[0].Healthy).To(BeTrue())
		// rows[0] is a sqlc.BeaconInventory — its field set is exactly
		// {ID, TokenID, InstanceLabel, ComponentName, Healthy, LastSeen,
		// CreatedAt} (internal/store/sqlc/models.go, generated from
		// 0010_beacon_inventory.up.sql). There is no column here a sample
		// value of 42 could have landed in.
	})

	// Agent tokens are org-bound (migration 0038). The beacon authenticates
	// the writer and keys its inventory on the token id; binding a token in
	// place must not change either, or every bound collector's beacon would
	// go dark after the bind.
	It("accepts a write from an org-bound token, keyed by the same token id", func() {
		org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "beacon-org", DisplayName: "Beacon org", AdminGroupID: "g"})
		Expect(err).NotTo(HaveOccurred())
		var id pgtype.UUID
		Expect(id.Scan(tokenID)).To(Succeed())
		_, err = st.Queries.BindAgentToken(ctx, sqlc.BindAgentTokenParams{
			ID: id, OrgID: org.ID, Clusters: json.RawMessage(`["prod-eu-1"]`), Roles: json.RawMessage(`["metrics"]`),
		})
		Expect(err).NotTo(HaveOccurred())

		resp := postRaw(encodeWrite([][]prompb.Label{healthySeries("10.0.0.7:12345", "pipe_bound")}, 1), authHeader)
		Expect(resp.StatusCode).To(Equal(http.StatusNoContent))

		rows, err := st.Queries.ListBeaconInventoryByPrincipal(ctx, tokenID)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].ComponentName).To(Equal("pipe_bound"))
	})

	// The collector id is a writer-chosen label. An org-bound token may only
	// report a collector its org claimed: naming another org's collector is
	// dropped and counted (beacon_denied), answered with the same 204.
	It("drops a bound token's write naming another org's collector, and stores its own", func() {
		orgA, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "beacon-a", DisplayName: "A", AdminGroupID: "ga"})
		Expect(err).NotTo(HaveOccurred())
		orgB, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "beacon-b", DisplayName: "B", AdminGroupID: "gb"})
		Expect(err).NotTo(HaveOccurred())
		collectorIn := func(cluster string, org sqlc.Org) string {
			cl, err := st.Queries.UpsertCluster(ctx, cluster)
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cl.ID, OrgID: org.ID})).To(Succeed())
			c, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cl.ID, Role: "metrics"})
			Expect(err).NotTo(HaveOccurred())
			return c.ID.String()
		}
		mine, theirs := collectorIn("a-prod", orgA), collectorIn("b-prod", orgB)
		var id pgtype.UUID
		Expect(id.Scan(tokenID)).To(Succeed())
		_, err = st.Queries.BindAgentToken(ctx, sqlc.BindAgentTokenParams{ID: id, OrgID: orgA.ID, Clusters: json.RawMessage(`[]`), Roles: json.RawMessage(`[]`)})
		Expect(err).NotTo(HaveOccurred())
		series := func(instance, path, collectorID string) []prompb.Label {
			return append(healthySeries(instance, path), prompb.Label{Name: beacon.CollectorIDLabel, Value: collectorID})
		}
		denied := func() float64 {
			m := &dto.Metric{}
			Expect(metrics.AgentRefusedTotal.WithLabelValues(metrics.AgentRefusedBeaconDenied).Write(m)).To(Succeed())
			return m.Counter.GetValue()
		}
		before := denied()

		resp := postRaw(encodeWrite([][]prompb.Label{series("10.0.0.8:1", "pipe_theirs", theirs)}, 1), authHeader)
		Expect(resp.StatusCode).To(Equal(http.StatusNoContent))
		rows, err := st.Queries.ListBeaconInventoryByPrincipal(ctx, tokenID)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(BeEmpty(), "org B's collector must get no inventory row from an org-A token")
		Expect(denied() - before).To(Equal(1.0))

		resp = postRaw(encodeWrite([][]prompb.Label{series("10.0.0.8:1", "pipe_mine", mine)}, 1), authHeader)
		Expect(resp.StatusCode).To(Equal(http.StatusNoContent))
		rows, err = st.Queries.ListBeaconInventoryByPrincipal(ctx, tokenID)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].ComponentName).To(Equal("pipe_mine"))
	})

	// #378: before the fix every pipeline collapsed onto one "/remotecfg" row
	// (Project read controller_path alone, which all pipelines share).
	It("stores one row per pipeline, rolls nested modules up, and drops the remotecfg root", func() {
		const inst = "10.0.0.6:12345"
		body := encodeWrite([][]prompb.Label{
			controllerSeries(inst, "/", "remotecfg", "healthy"),
			controllerSeries(inst, "/", "", "healthy"),
			healthySeries(inst, "pipe_app_logs"),
			healthySeries(inst, "pipe_k8s"),
			controllerSeries(inst, "/remotecfg/pipe_k8s.default", "feat.pods", "unhealthy"),
		}, 1)

		resp := postRaw(body, authHeader)
		Expect(resp.StatusCode).To(Equal(http.StatusNoContent))

		rows, err := st.Queries.ListBeaconInventoryByPrincipal(ctx, tokenID)
		Expect(err).NotTo(HaveOccurred())
		got := map[string]bool{}
		for _, r := range rows {
			got[r.ComponentName] = r.Healthy
		}
		Expect(got).To(Equal(map[string]bool{"pipe_app_logs": true, "pipe_k8s": false}))
	})

	It("marks a component unhealthy when its unhealthy count is nonzero", func() {
		body := encodeWrite([][]prompb.Label{
			componentSeries("10.0.0.5:12345", "pipe_y", "healthy"),
			componentSeries("10.0.0.5:12345", "pipe_y", "unhealthy"),
		}, 1)

		resp := postRaw(body, authHeader)
		Expect(resp.StatusCode).To(Equal(http.StatusNoContent))

		rows, err := st.Queries.ListBeaconInventoryByPrincipal(ctx, tokenID)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].Healthy).To(BeFalse())
	})
})
