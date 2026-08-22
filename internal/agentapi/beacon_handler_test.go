package agentapi_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"

	"github.com/golang/snappy"
	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/prometheus/prompb"

	"shepherd/internal/agentapi"
	"shepherd/internal/beacon"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// mustUUID parses a UUID string into pgtype.UUID for query params, failing
// the spec immediately on a malformed input rather than silently querying
// with a zero UUID.
func mustUUID(s string) pgtype.UUID {
	var u pgtype.UUID
	Expect(u.Scan(s)).To(Succeed())
	return u
}

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

	componentSeries := func(instance, path, healthType string) []prompb.Label {
		return []prompb.Label{
			{Name: "__name__", Value: "alloy_component_controller_running_components"},
			{Name: "controller_path", Value: path},
			{Name: "health_type", Value: healthType},
			{Name: "instance", Value: instance},
		}
	}
	healthySeries := func(instance, path string) []prompb.Label {
		return componentSeries(instance, path, "healthy")
	}

	startServer := func(limits beacon.Limits) {
		h := agentapi.NewBeaconHandler(st, slog.Default(), limits)
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

		rows, err := st.Queries.ListBeaconInventoryByToken(ctx, mustUUID(tokenID))
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(BeEmpty(), "an unauthenticated write must not reach storage")
	})

	It("rejects a write with a wrong secret", func() {
		badAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(tokenID+":wrong-secret"))
		body := encodeWrite([][]prompb.Label{healthySeries("10.0.0.1:12345", "pipe_x")}, 1)
		resp := postRaw(body, badAuth)
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
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

		rows, err := st.Queries.ListBeaconInventoryByToken(ctx, mustUUID(tokenID))
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

	It("marks a component unhealthy when its unhealthy count is nonzero", func() {
		body := encodeWrite([][]prompb.Label{
			componentSeries("10.0.0.5:12345", "pipe_y", "healthy"),
			componentSeries("10.0.0.5:12345", "pipe_y", "unhealthy"),
		}, 1)

		resp := postRaw(body, authHeader)
		Expect(resp.StatusCode).To(Equal(http.StatusNoContent))

		rows, err := st.Queries.ListBeaconInventoryByToken(ctx, mustUUID(tokenID))
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].Healthy).To(BeFalse())
	})
})
