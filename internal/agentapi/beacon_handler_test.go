package agentapi_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/golang/snappy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/prometheus/model/value"
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
		dbURL      string
		// homeOrg is the seeded token's org; anyCollector is a collector in a
		// cluster homeOrg claimed, so the seeded token may report it.
		homeOrg      sqlc.Org
		anyCollector string
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
	// collectorSeries also carries the shepherd_collector_id label the
	// rendered baseline stamps on every series; "" leaves it off.
	collectorSeries := func(collectorID, instance, controllerPath, controllerID, healthType string) []prompb.Label {
		labels := []prompb.Label{
			{Name: "__name__", Value: "alloy_component_controller_running_components"},
			{Name: "controller_id", Value: controllerID},
			{Name: "controller_path", Value: controllerPath},
			{Name: "health_type", Value: healthType},
			{Name: "instance", Value: instance},
		}
		if collectorID != "" {
			labels = append(labels, prompb.Label{Name: beacon.CollectorIDLabel, Value: collectorID})
		}
		return labels
	}
	controllerSeries := func(instance, controllerPath, controllerID, healthType string) []prompb.Label {
		return collectorSeries(anyCollector, instance, controllerPath, controllerID, healthType)
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

	counter := func(c *prometheus.CounterVec, label string) float64 {
		m := &dto.Metric{}
		Expect(c.WithLabelValues(label).Write(m)).To(Succeed())
		return m.Counter.GetValue()
	}
	denied := func() float64 { return counter(metrics.AgentRefusedTotal, metrics.AgentRefusedBeaconDenied) }

	// collectorIn seeds a collector in cluster, claimed by org (nil leaves the
	// cluster unclaimed), and returns its id.
	collectorIn := func(cluster string, org *sqlc.Org) string {
		cl, err := st.Queries.UpsertCluster(ctx, cluster)
		Expect(err).NotTo(HaveOccurred())
		if org != nil {
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cl.ID, OrgID: org.ID})).To(Succeed())
		}
		c, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cl.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())
		return c.ID.String()
	}

	bindToken := func(org sqlc.Org) {
		var id pgtype.UUID
		Expect(id.Scan(tokenID)).To(Succeed())
		_, err := st.Queries.BindAgentToken(ctx, sqlc.BindAgentTokenParams{ID: id, OrgID: org.ID, Clusters: json.RawMessage(`[]`), Roles: json.RawMessage(`[]`)})
		Expect(err).NotTo(HaveOccurred())
	}

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		dbURL = sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())

		homeOrg, err = st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "beacon-home", DisplayName: "Home", AdminGroupID: "ghome"})
		Expect(err).NotTo(HaveOccurred())
		anyCollector = collectorIn("home-prod", &homeOrg)

		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		secret := base64.URLEncoding.EncodeToString(raw)
		hash := sha256.Sum256([]byte(secret))
		tok, err := st.Queries.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{
			Name: "beacon-test-token", TokenHash: hash[:], CreatedBy: "test", OrgID: homeOrg.ID,
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
		collector := collectorIn("prod-eu-1", &org)

		resp := postRaw(encodeWrite([][]prompb.Label{collectorSeries(collector, "10.0.0.7:12345", "/remotecfg", "pipe_bound.default", "healthy")}, 1), authHeader)
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
		mine, theirs := collectorIn("a-prod", &orgA), collectorIn("b-prod", &orgB)
		bindToken(orgA)
		series := func(instance, path, collectorID string) []prompb.Label {
			return collectorSeries(collectorID, instance, "/remotecfg", path+".default", "healthy")
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

	// #389: a bound token naming a collector whose cluster no org has claimed
	// is outside its org too — dropped and counted like another org's.
	It("drops a bound token's write naming a collector in an unclaimed cluster", func() {
		org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "beacon-u", DisplayName: "U", AdminGroupID: "gu"})
		Expect(err).NotTo(HaveOccurred())
		unclaimed := collectorIn("nobody-prod", nil)
		bindToken(org)
		before := denied()

		body := encodeWrite([][]prompb.Label{collectorSeries(unclaimed, "10.0.0.4:1", "/remotecfg", "pipe_u.default", "healthy")}, 1)
		Expect(postRaw(body, authHeader).StatusCode).To(Equal(http.StatusNoContent))
		rows, err := st.Queries.ListBeaconInventoryByPrincipal(ctx, tokenID)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(BeEmpty(), "an unclaimed cluster's collector gets no inventory row from a bound token")
		Expect(denied() - before).To(Equal(1.0))
	})

	// #389: the collector id is how a write is attributed and how a bound
	// writer's write is checked against its org. A write without one, or with
	// one that is not a UUID, is dropped and counted by reason — never stored
	// under the writer's own principal.
	It("drops a write whose collector id is missing or unparseable, counted by reason", func() {
		for _, tc := range []struct{ collectorID, reason string }{
			{"", metrics.BeaconDroppedCollectorIDMissing},
			{"not-a-uuid", metrics.BeaconDroppedCollectorIDInvalid},
		} {
			before := counter(metrics.BeaconDroppedTotal, tc.reason)
			body := encodeWrite([][]prompb.Label{collectorSeries(tc.collectorID, "10.0.0.3:1", "/remotecfg", "pipe_z.default", "healthy")}, 1)
			Expect(postRaw(body, authHeader).StatusCode).To(Equal(http.StatusNoContent))
			Expect(counter(metrics.BeaconDroppedTotal, tc.reason)-before).To(Equal(1.0), tc.reason)
		}
		rows, err := st.Queries.ListBeaconInventoryByPrincipal(ctx, tokenID)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(BeEmpty())
	})

	// #389: an OIDC caller with an agent_identities binding acts for that
	// binding's org, so its beacon gets the same collector-in-org check a
	// bound token's does.
	It("drops a bound OIDC identity's write naming another org's collector, and stores its own", func() {
		orgA, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "beacon-oa", DisplayName: "OA", AdminGroupID: "goa"})
		Expect(err).NotTo(HaveOccurred())
		orgB, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "beacon-ob", DisplayName: "OB", AdminGroupID: "gob"})
		Expect(err).NotTo(HaveOccurred())
		mine, theirs := collectorIn("oa-prod", &orgA), collectorIn("ob-prod", &orgB)
		_, err = st.Queries.CreateAgentIdentity(ctx, sqlc.CreateAgentIdentityParams{
			Issuer: "https://idp/", AppID: "alloy-app", OrgID: orgA.ID,
			Clusters: json.RawMessage(`[]`), Roles: json.RawMessage(`[]`), CreatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())
		startServerWithVerifier(beacon.DefaultLimits, func(context.Context, string) (auth.AgentClaims, error) {
			return auth.AgentClaims{Issuer: "https://idp/", Subject: "alloy-sub", AppID: "alloy-app"}, nil
		})
		const principal = "oidc:https://idp/|alloy-sub"
		before := denied()

		body := encodeWrite([][]prompb.Label{collectorSeries(theirs, "10.0.0.2:1", "/remotecfg", "pipe_theirs.default", "healthy")}, 1)
		Expect(postRaw(body, "Bearer t").StatusCode).To(Equal(http.StatusNoContent))
		rows, err := st.Queries.ListBeaconInventoryByPrincipal(ctx, principal)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(BeEmpty(), "org B's collector must get no inventory row from an org-A identity")
		Expect(denied() - before).To(Equal(1.0))

		body = encodeWrite([][]prompb.Label{collectorSeries(mine, "10.0.0.2:1", "/remotecfg", "pipe_mine.default", "healthy")}, 1)
		Expect(postRaw(body, "Bearer t").StatusCode).To(Equal(http.StatusNoContent))
		rows, err = st.Queries.ListBeaconInventoryByPrincipal(ctx, principal)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].ComponentName).To(Equal("pipe_mine"))
	})

	// #389: a mode-2 OIDC caller (no binding; the IdP asserts the org by name)
	// acts for the asserted org, so the beacon checks its collectors against
	// that org — and an asserted org that does not exist covers no collector.
	It("drops a mode-2 OIDC caller's write naming a collector outside its asserted org", func() {
		orgA, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "beacon-m2a", DisplayName: "M2A", AdminGroupID: "gm2a"})
		Expect(err).NotTo(HaveOccurred())
		orgB, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "beacon-m2b", DisplayName: "M2B", AdminGroupID: "gm2b"})
		Expect(err).NotTo(HaveOccurred())
		mine, theirs := collectorIn("m2a-prod", &orgA), collectorIn("m2b-prod", &orgB)
		asserted := "beacon-m2a"
		startServerWithVerifier(beacon.DefaultLimits, func(context.Context, string) (auth.AgentClaims, error) {
			return auth.AgentClaims{Issuer: "https://idp/", Subject: "m2-sub", AppID: "m2-app", Org: asserted}, nil
		})
		const principal = "oidc:https://idp/|m2-sub"
		write := func(collectorID, pipe string) {
			body := encodeWrite([][]prompb.Label{collectorSeries(collectorID, "10.0.0.11:1", "/remotecfg", pipe+".default", "healthy")}, 1)
			Expect(postRaw(body, "Bearer t").StatusCode).To(Equal(http.StatusNoContent))
		}
		before := denied()

		write(theirs, "pipe_theirs")
		rows, err := st.Queries.ListBeaconInventoryByPrincipal(ctx, principal)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(BeEmpty(), "org B's collector must get no inventory row from a caller asserting org A")
		Expect(denied() - before).To(Equal(1.0))

		write(mine, "pipe_mine")
		rows, err = st.Queries.ListBeaconInventoryByPrincipal(ctx, principal)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].ComponentName).To(Equal("pipe_mine"))

		asserted = "no-such-org"
		write(mine, "pipe_ghost")
		rows, err = st.Queries.ListBeaconInventoryByPrincipal(ctx, principal)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1), "an asserted org that does not exist reports no collector")
		Expect(denied() - before).To(Equal(2.0))
	})

	// #389: an unbound token (one from before org binding, or whose org was
	// deleted) acts for no org, so it may report no collector — every write
	// is dropped and counted as token_unbound, still 204.
	It("drops every write from an unbound token", func() {
		hash := sha256.Sum256([]byte("unbound-secret"))
		tok, err := st.Queries.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{Name: "unbound", TokenHash: hash[:], CreatedBy: "test"})
		Expect(err).NotTo(HaveOccurred())
		header := "Basic " + base64.StdEncoding.EncodeToString([]byte(tok.ID.String()+":unbound-secret"))
		before := counter(metrics.AgentRefusedTotal, metrics.AgentRefusedTokenUnbound)

		Expect(postRaw(encodeWrite([][]prompb.Label{healthySeries("10.0.0.12:1", "pipe_u")}, 1), header).StatusCode).To(Equal(http.StatusNoContent))
		rows, err := st.Queries.ListBeaconInventoryByPrincipal(ctx, tok.ID.String())
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(BeEmpty())
		Expect(counter(metrics.AgentRefusedTotal, metrics.AgentRefusedTokenUnbound) - before).To(Equal(1.0))
	})

	// #389: a cluster allowlist narrows what the caller may report exactly as
	// it narrows what the agent API serves it: a token's (or binding's)
	// clusters list, and a mode-2 caller's clusters claim. Empty means any.
	It("drops a write naming a collector in a cluster outside the caller's allowlist", func() {
		allowed, other := collectorIn("home-allowed", &homeOrg), collectorIn("home-other", &homeOrg)
		var id pgtype.UUID
		Expect(id.Scan(tokenID)).To(Succeed())
		_, err := st.Queries.BindAgentToken(ctx, sqlc.BindAgentTokenParams{ID: id, OrgID: homeOrg.ID, Clusters: json.RawMessage(`["home-allowed"]`), Roles: json.RawMessage(`[]`)})
		Expect(err).NotTo(HaveOccurred())
		write := func(header, collectorID, pipe string) {
			body := encodeWrite([][]prompb.Label{collectorSeries(collectorID, "10.0.0.13:1", "/remotecfg", pipe+".default", "healthy")}, 1)
			Expect(postRaw(body, header).StatusCode).To(Equal(http.StatusNoContent))
		}
		components := func(principal string) []string {
			rows, err := st.Queries.ListBeaconInventoryByPrincipal(ctx, principal)
			Expect(err).NotTo(HaveOccurred())
			var names []string
			for _, r := range rows {
				names = append(names, r.ComponentName)
			}
			return names
		}
		before := denied()
		write(authHeader, other, "pipe_other")
		write(authHeader, allowed, "pipe_allowed")
		Expect(components(tokenID)).To(ConsistOf("pipe_allowed"))
		Expect(denied() - before).To(Equal(1.0))

		startServerWithVerifier(beacon.DefaultLimits, func(context.Context, string) (auth.AgentClaims, error) {
			return auth.AgentClaims{Issuer: "https://idp/", Subject: "m2c", AppID: "m2c", Org: "beacon-home", Clusters: []string{"home-allowed"}}, nil
		})
		before = denied()
		write("Bearer t", other, "pipe_other")
		write("Bearer t", allowed, "pipe_allowed")
		Expect(components("oidc:https://idp/|m2c")).To(ConsistOf("pipe_allowed"))
		Expect(denied() - before).To(Equal(1.0))
	})

	// #389: a lookup of the named collector's owner that the database cannot
	// answer is an outage, not a denial: 503, which remote_write retries.
	It("answers 503 when the collector's owner cannot be looked up", func() {
		conn, err := pgx.Connect(ctx, dbURL)
		Expect(err).NotTo(HaveOccurred())
		defer conn.Close(ctx) //nolint:errcheck // test cleanup
		// Token auth reads only agent_tokens, so this breaks the ownership
		// lookup alone.
		_, err = conn.Exec(ctx, `ALTER TABLE clusters RENAME TO clusters_gone`) // RAW-SQL-OK: fault injection
		Expect(err).NotTo(HaveOccurred())
		before := denied()

		resp := postRaw(encodeWrite([][]prompb.Label{healthySeries("10.0.0.14:1", "pipe_x")}, 1), authHeader)
		Expect(resp.StatusCode).To(Equal(http.StatusServiceUnavailable))
		Expect(denied()-before).To(BeZero(), "an outage is not counted as a denial")
	})

	// #389: one batch comes from one Alloy, so every series carries the same
	// collector id. A batch naming more than one is dropped whole and counted.
	It("drops a batch whose series carry more than one collector id", func() {
		second := collectorIn("home-second", &homeOrg)
		before := counter(metrics.BeaconDroppedTotal, metrics.BeaconDroppedCollectorIDMixed)
		body := encodeWrite([][]prompb.Label{
			collectorSeries(anyCollector, "10.0.0.15:1", "/remotecfg", "pipe_a.default", "healthy"),
			collectorSeries(second, "10.0.0.15:1", "/remotecfg", "pipe_b.default", "healthy"),
		}, 1)
		Expect(postRaw(body, authHeader).StatusCode).To(Equal(http.StatusNoContent))
		rows, err := st.Queries.ListBeaconInventoryByPrincipal(ctx, tokenID)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(BeEmpty())
		Expect(counter(metrics.BeaconDroppedTotal, metrics.BeaconDroppedCollectorIDMixed) - before).To(Equal(1.0))
	})

	// #389: as in the agent API, a stored binding wins over an org the IdP
	// asserts.
	It("checks an OIDC caller against its binding's org, not a different asserted one", func() {
		orgA, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "beacon-pa", DisplayName: "PA", AdminGroupID: "gpa"})
		Expect(err).NotTo(HaveOccurred())
		orgB, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "beacon-pb", DisplayName: "PB", AdminGroupID: "gpb"})
		Expect(err).NotTo(HaveOccurred())
		inA, inB := collectorIn("pa-prod", &orgA), collectorIn("pb-prod", &orgB)
		_, err = st.Queries.CreateAgentIdentity(ctx, sqlc.CreateAgentIdentityParams{
			Issuer: "https://idp/", AppID: "prec-app", OrgID: orgA.ID,
			Clusters: json.RawMessage(`[]`), Roles: json.RawMessage(`[]`), CreatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())
		startServerWithVerifier(beacon.DefaultLimits, func(context.Context, string) (auth.AgentClaims, error) {
			return auth.AgentClaims{Issuer: "https://idp/", Subject: "prec", AppID: "prec-app", Org: "beacon-pb"}, nil
		})
		before := denied()
		for collectorID, pipe := range map[string]string{inA: "pipe_a", inB: "pipe_b"} {
			body := encodeWrite([][]prompb.Label{collectorSeries(collectorID, "10.0.0.16:1", "/remotecfg", pipe+".default", "healthy")}, 1)
			Expect(postRaw(body, "Bearer t").StatusCode).To(Equal(http.StatusNoContent))
		}
		rows, err := st.Queries.ListBeaconInventoryByPrincipal(ctx, "oidc:https://idp/|prec")
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].ComponentName).To(Equal("pipe_a"), "the binding's org (A) wins over the asserted org (B)")
		Expect(denied() - before).To(Equal(1.0))
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
	// 0.21 walkthrough: a disabled pipeline's row was refreshed (healthy) by
	// the first beacon write after Alloy unloaded it, because that write
	// carries a staleness marker for each vanished series and the marker's
	// NaN was projected as a live, healthy count. The marker must leave the
	// row as the last real report left it, so reconciliation sees it fall
	// behind on the very next report.
	It("does not refresh a pipeline's row from a staleness marker", func() {
		const inst = "10.0.0.4:12345"
		Expect(postRaw(encodeWrite([][]prompb.Label{
			healthySeries(inst, "pipe_keep"), healthySeries(inst, "pipe_gone"),
		}, 1), authHeader).StatusCode).To(Equal(http.StatusNoContent))
		lastSeen := func() map[string]time.Time {
			rows, err := st.Queries.ListBeaconInventoryByPrincipal(ctx, tokenID)
			Expect(err).NotTo(HaveOccurred())
			out := map[string]time.Time{}
			for _, r := range rows {
				out[r.ComponentName] = r.LastSeen.Time
			}
			return out
		}
		before := lastSeen()
		Expect(before).To(HaveKey("pipe_gone"))

		wr := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
			{Labels: healthySeries(inst, "pipe_keep"), Samples: []prompb.Sample{{Value: 1, Timestamp: 2000}}},
			{Labels: healthySeries(inst, "pipe_gone"), Samples: []prompb.Sample{{Value: math.Float64frombits(value.StaleNaN), Timestamp: 2000}}},
		}}
		raw, err := wr.Marshal()
		Expect(err).NotTo(HaveOccurred())
		time.Sleep(10 * time.Millisecond)
		Expect(postRaw(snappy.Encode(nil, raw), authHeader).StatusCode).To(Equal(http.StatusNoContent))

		after := lastSeen()
		Expect(after["pipe_keep"]).To(BeTemporally(">", before["pipe_keep"]))
		Expect(after["pipe_gone"]).To(Equal(before["pipe_gone"]), "a staleness marker is not a report")
	})

	// 0.21 re-walk: once the only pipeline on a collector is disabled, the
	// beacon writes carry no pipeline series, so no row is refreshed and the
	// dropped row stays the instance's newest. Every accepted write stamps
	// the instance's report time (instance_reported_at, 0046) on its rows, so
	// reconciliation can see the row fall behind the report.
	Describe("the instance's report stamp", func() {
		const inst = "10.0.0.15:12345"
		type stamp struct{ lastSeen, reportedAt time.Time }
		stamps := func() map[string]stamp {
			rows, err := st.Queries.ListBeaconInventoryByPrincipal(ctx, tokenID)
			Expect(err).NotTo(HaveOccurred())
			out := map[string]stamp{}
			for _, r := range rows {
				Expect(r.InstanceReportedAt.Valid).To(BeTrue(), r.ComponentName+" has no report stamp")
				out[r.ComponentName] = stamp{r.LastSeen.Time, r.InstanceReportedAt.Time}
			}
			return out
		}
		// previousScrape backdates every row to a scrape 70s ago, past the
		// handler's 5s stamp throttle.
		previousScrape := func() {
			// RAW-SQL-OK: backdating last_seen/instance_reported_at to the previous scrape — no sqlc query sets them
			_, err := st.Pool().Exec(ctx, `UPDATE beacon_inventory SET last_seen = now() - interval '70 seconds', instance_reported_at = now() - interval '70 seconds'`)
			Expect(err).NotTo(HaveOccurred())
			// RAW-SQL-OK: backdating the instance's last-report record (0048) with the rows
			_, err = st.Pool().Exec(ctx, `UPDATE beacon_instance_reports SET reported_at = now() - interval '70 seconds'`)
			Expect(err).NotTo(HaveOccurred())
		}
		// lastReports is the collector's last-report records (0048), by instance.
		lastReports := func() map[string]time.Time {
			var coll pgtype.UUID
			Expect(coll.Scan(anyCollector)).To(Succeed())
			recs, err := st.Queries.ListBeaconInstanceReportsByCollector(ctx, coll)
			Expect(err).NotTo(HaveOccurred())
			out := map[string]time.Time{}
			for _, r := range recs {
				out[r.InstanceLabel] = r.ReportedAt.Time
			}
			return out
		}
		rootOnly := func() []byte {
			return encodeWrite([][]prompb.Label{controllerSeries(inst, "/", "remotecfg", "healthy")}, 1)
		}

		BeforeEach(func() {
			Expect(postRaw(encodeWrite([][]prompb.Label{healthySeries(inst, "pipe_sole")}, 1), authHeader).StatusCode).To(Equal(http.StatusNoContent))
			Expect(stamps()).To(HaveKey("pipe_sole"))
			previousScrape()
		})

		It("is stamped by a write that carries no pipeline series", func() {
			Expect(postRaw(rootOnly(), authHeader).StatusCode).To(Equal(http.StatusNoContent))
			s := stamps()["pipe_sole"]
			Expect(s.reportedAt).To(BeTemporally(">", s.lastSeen.Add(time.Minute)),
				"the report moved on while the dropped pipeline's row did not")
		})

		It("is stamped alongside a sibling the write still carries", func() {
			Expect(postRaw(encodeWrite([][]prompb.Label{healthySeries(inst, "pipe_sibling")}, 1), authHeader).StatusCode).To(Equal(http.StatusNoContent))
			got := stamps()
			Expect(got["pipe_sibling"].reportedAt).To(BeTemporally("~", got["pipe_sibling"].lastSeen, 5*time.Second))
			Expect(got["pipe_sole"].reportedAt).To(BeTemporally("~", got["pipe_sibling"].reportedAt, 5*time.Second))
			Expect(got["pipe_sole"].reportedAt).To(BeTemporally(">", got["pipe_sole"].lastSeen.Add(time.Minute)))
		})

		It("is not stamped by a refused write", func() {
			before := stamps()["pipe_sole"]
			reportsBefore := lastReports()
			Expect(reportsBefore).To(HaveKey(inst))

			// Unauthenticated.
			Expect(postRaw(rootOnly(), "").StatusCode).To(Equal(http.StatusUnauthorized))
			// An unbound token.
			hash := sha256.Sum256([]byte("unbound-stamp"))
			unbound, err := st.Queries.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{Name: "unbound-stamp", TokenHash: hash[:], CreatedBy: "test"})
			Expect(err).NotTo(HaveOccurred())
			Expect(postRaw(rootOnly(), "Basic "+base64.StdEncoding.EncodeToString([]byte(unbound.ID.String()+":unbound-stamp"))).StatusCode).To(Equal(http.StatusNoContent))
			// A token bound to another org, naming this org's collector.
			other, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "beacon-stamp-other", DisplayName: "Other", AdminGroupID: "gso"})
			Expect(err).NotTo(HaveOccurred())
			hash = sha256.Sum256([]byte("other-stamp"))
			foreign, err := st.Queries.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{Name: "other-stamp", TokenHash: hash[:], CreatedBy: "test", OrgID: other.ID})
			Expect(err).NotTo(HaveOccurred())
			deniedBefore := denied()
			Expect(postRaw(rootOnly(), "Basic "+base64.StdEncoding.EncodeToString([]byte(foreign.ID.String()+":other-stamp"))).StatusCode).To(Equal(http.StatusNoContent))
			Expect(denied() - deniedBefore).To(Equal(1.0))

			Expect(stamps()["pipe_sole"]).To(Equal(before), "a refused write is no report")
			Expect(lastReports()).To(Equal(reportsBefore), "a refused write records no last report (0048)")
		})

		// v0.20.0 follow-up: the sweeper deletes rows by last_seen, so the
		// report must also live in a record of its own (0048) that a write
		// carrying no pipeline series keeps current with no row left at all.
		It("records the last report apart from the rows, also with every row deleted", func() {
			Expect(postRaw(rootOnly(), authHeader).StatusCode).To(Equal(http.StatusNoContent))
			first := lastReports()
			Expect(first).To(HaveKey(inst))

			// RAW-SQL-OK: deleting every beacon row as the sweeper would, and backdating the record past the throttle
			_, err := st.Pool().Exec(ctx, `DELETE FROM beacon_inventory; UPDATE beacon_instance_reports SET reported_at = now() - interval '70 seconds'`)
			Expect(err).NotTo(HaveOccurred())
			Expect(postRaw(rootOnly(), authHeader).StatusCode).To(Equal(http.StatusNoContent))
			got := lastReports()
			Expect(got).To(HaveLen(1))
			Expect(got[inst]).To(BeTemporally("~", time.Now(), 5*time.Second))
		})

		It("records nothing, and still answers 204, for an instance label past the table's bound", func() {
			long := strings.Repeat("é", 257) // 257 characters, as Postgres counts them
			Expect(postRaw(encodeWrite([][]prompb.Label{controllerSeries(long, "/", "remotecfg", "healthy")}, 1), authHeader).StatusCode).
				To(Equal(http.StatusNoContent))
			Expect(lastReports()).NotTo(HaveKey(long))
		})

		// PR #428 review: an unscoped writer (OIDC with no binding and no
		// asserted org) skips the collector-owner check. A write carrying no
		// pipeline series and naming another org's collector must not create
		// a record that makes that collector's observed set read current and
		// empty — every served pipeline would show as drift.
		It("records nothing for an unscoped writer's zero-series write naming another org's collector", func() {
			victimOrg, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "beacon-victim", DisplayName: "V", AdminGroupID: "gv"})
			Expect(err).NotTo(HaveOccurred())
			victim := collectorIn("victim-prod", &victimOrg)
			startServerWithVerifier(beacon.DefaultLimits, func(context.Context, string) (auth.AgentClaims, error) {
				return auth.AgentClaims{Issuer: "https://idp/", Subject: "unscoped-sub", AppID: "unscoped-app"}, nil
			})
			reportsOf := func(coll string) int {
				var id pgtype.UUID
				Expect(id.Scan(coll)).To(Succeed())
				recs, err := st.Queries.ListBeaconInstanceReportsByCollector(ctx, id)
				Expect(err).NotTo(HaveOccurred())
				return len(recs)
			}
			zeroSeries := encodeWrite([][]prompb.Label{collectorSeries(victim, "10.9.9.9:1", "/", "remotecfg", "healthy")}, 1)
			Expect(postRaw(zeroSeries, "Bearer t").StatusCode).To(Equal(http.StatusNoContent))
			Expect(reportsOf(victim)).To(BeZero(), "no record without a row this write stored")

			// The same writer with a pipeline series stores a row (as before
			// this PR, unscoped writers are not owner-checked) and the record
			// with it — no more than that row already says.
			withSeries := encodeWrite([][]prompb.Label{collectorSeries(victim, "10.9.9.9:1", "/remotecfg", "pipe_x.default", "healthy")}, 1)
			Expect(postRaw(withSeries, "Bearer t").StatusCode).To(Equal(http.StatusNoContent))
			Expect(reportsOf(victim)).To(Equal(1))
		})

		It("still records a scoped writer's zero-series write (the owner check passed)", func() {
			// RAW-SQL-OK: clearing the record the BeforeEach write created
			_, err := st.Pool().Exec(ctx, `DELETE FROM beacon_instance_reports`)
			Expect(err).NotTo(HaveOccurred())
			Expect(postRaw(rootOnly(), authHeader).StatusCode).To(Equal(http.StatusNoContent))
			Expect(lastReports()).To(HaveKey(inst))
		})

		// PR #428 review: instance labels rotate (pod restarts), and records
		// live for agent.delete_after, so the table is bounded per collector:
		// past maxBeaconInstanceReports the oldest records are evicted.
		It("keeps at most the cap of records per collector, evicting the oldest", func() {
			cap := agentapi.MaxBeaconInstanceReports
			// RAW-SQL-OK: seeding cap records with distinct, ascending ages — no sqlc query sets reported_at
			_, err := st.Pool().Exec(ctx, `
				INSERT INTO beacon_instance_reports (collector_id, instance_label, reported_at)
				SELECT $1::uuid, 'old-' || g, now() - make_interval(secs => 600 + g)
				FROM generate_series(1, $2::int) g
				ON CONFLICT DO NOTHING`, anyCollector, cap)
			Expect(err).NotTo(HaveOccurred())
			Expect(postRaw(encodeWrite([][]prompb.Label{healthySeries("fresh:1", "pipe_fresh")}, 1), authHeader).StatusCode).To(Equal(http.StatusNoContent))
			got := lastReports()
			Expect(got).To(HaveLen(cap))
			Expect(got).To(HaveKey("fresh:1"))
			Expect(got).To(HaveKey(inst), "a recent record is not the one evicted")
			Expect(got).NotTo(HaveKey(fmt.Sprintf("old-%d", cap)), "the oldest is evicted")
			Expect(got).NotTo(HaveKey(fmt.Sprintf("old-%d", cap-1)))
		})

		It("throttles the record like the row stamp", func() {
			Expect(postRaw(rootOnly(), authHeader).StatusCode).To(Equal(http.StatusNoContent))
			first := lastReports()[inst]
			time.Sleep(20 * time.Millisecond)
			Expect(postRaw(rootOnly(), authHeader).StatusCode).To(Equal(http.StatusNoContent))
			Expect(lastReports()[inst]).To(Equal(first), "a second write within 5s leaves the record alone")
		})
	})
})
