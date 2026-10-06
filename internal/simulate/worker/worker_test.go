package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/schema"
	"shepherd/internal/simulate"
	"shepherd/internal/simulate/worker"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
	"shepherd/internal/version"
	"shepherd/internal/visual"
)

// fakeSimulator is a control-API test double. Its POST /v1/runs handler
// blocks on release until the test lets it through, tracking the number of
// concurrently in-flight starts — the counter the concurrency kill-probe
// below asserts never exceeds 1.
type fakeSimulator struct {
	mu          sync.Mutex
	runs        map[string]string // run id -> state
	results     map[string]*simulate.ClientResults
	current     int32
	maxSeen     int32
	release     chan struct{}
	releaseOnce sync.Once
}

func newFakeSimulator() *fakeSimulator {
	return &fakeSimulator{runs: map[string]string{}, results: map[string]*simulate.ClientResults{}, release: make(chan struct{})}
}

// unblock releases every start blocked on f.release, exactly once. Safe to
// call from the test body (to let seeded runs proceed) AND unconditionally
// from AfterEach: without this, a test that fails before its own unblock
// call (e.g. a red-proof run with the advisory lock disabled) would leave a
// handler goroutine parked forever, and httptest.Server.Close() blocks until
// every in-flight handler returns — hanging the whole suite instead of
// failing it.
func (f *fakeSimulator) unblock() {
	f.releaseOnce.Do(func() { close(f.release) })
}

func (f *fakeSimulator) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/runs", func(w http.ResponseWriter, r *http.Request) {
		var req simulate.ClientStartRequest
		_ = json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck // test double; malformed body just yields zero values

		cur := atomic.AddInt32(&f.current, 1)
		for {
			old := atomic.LoadInt32(&f.maxSeen)
			if cur <= old || atomic.CompareAndSwapInt32(&f.maxSeen, old, cur) {
				break
			}
		}

		<-f.release // held until the test releases every blocked start at once

		atomic.AddInt32(&f.current, -1)
		id := fmt.Sprintf("fake-run-%p-%d", r, time.Now().UnixNano())
		// Every rendered component reports healthy, the way a real sandbox
		// reports a stub that ran fine: the stand-in for a stubbed discovery
		// node is a working discovery.relabel, so Alloy says "healthy".
		results := &simulate.ClientResults{}
		for local, node := range req.ComponentIndex {
			results.Components = append(results.Components, simulate.ClientComponentHealth{
				LocalID: local, NodeID: node, Health: "healthy", Message: "started component",
			})
		}
		f.mu.Lock()
		f.runs[id] = "completed"
		f.results[id] = results
		f.mu.Unlock()

		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(simulate.ClientRun{ID: id, State: "completed", Results: results}) //nolint:errcheck // test double
	})
	mux.HandleFunc("GET /v1/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		f.mu.Lock()
		state, ok := f.runs[id]
		results := f.results[id]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(simulate.ClientAPIError{Code: "run_not_found", Message: "no such run"}) //nolint:errcheck // test double
			return
		}
		_ = json.NewEncoder(w).Encode(simulate.ClientRun{ID: id, State: state, Results: results}) //nolint:errcheck // test double
	})
	return mux
}

func testStore(ctx context.Context) *store.Store {
	dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
	st, err := store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 10}, slog.Default())
	Expect(err).NotTo(HaveOccurred())
	return st
}

// singleNodeGraph builds the smallest graph containing one component.
// Duplicated verbatim from internal/simulate's own transform_test.go fixture
// of the same name: this package cannot import that _test.go file (it lives
// in a different package's test binary), and worker_test.go needs the exact
// same shape so the claim/execute lifecycle sees what the transform tests do.
func singleNodeGraph(component string) visual.GraphDocument {
	return visual.GraphDocument{
		Kind:          "alloy-graph/v1",
		SchemaVersion: "alloy-v1.18.1",
		Nodes: []visual.GraphNode{{
			ID: "d1", Component: component, Label: "sink", Props: map[string]interface{}{},
		}},
	}
}

func seedQueuedRun(ctx context.Context, st *store.Store, orgID pgtype.UUID, component string) sqlc.SimulateRun {
	graphJSON, err := json.Marshal(singleNodeGraph(component))
	Expect(err).NotTo(HaveOccurred())
	run, err := st.Queries.CreateSimulateRun(ctx, sqlc.CreateSimulateRunParams{
		OrgID: orgID, Graph: graphJSON, RequestedDurationSeconds: 15, CreatedBy: "worker-test",
	})
	Expect(err).NotTo(HaveOccurred())
	return run
}

var _ = Describe("RunWorker", Label("integration"), func() {
	var (
		ctx     context.Context
		cancel  context.CancelFunc
		st      *store.Store
		orgID   pgtype.UUID
		reg     *schema.Registry
		v       *validate.Validator
		fakeSim *fakeSimulator
		sim     *httptest.Server
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		st = testStore(ctx)

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "worker-test-org", DisplayName: "Worker Test Org", AdminGroupID: "worker-admin",
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID

		reg, err = schema.New(schema.Embedded, version.AlloySchemaVersion)
		Expect(err).NotTo(HaveOccurred())
		v = validate.New(&config.ValidateConfig{AlloyBinary: "", Timeout: 5 * time.Second})

		fakeSim = newFakeSimulator()
		sim = httptest.NewServer(fakeSim.handler())
	})

	AfterEach(func() {
		fakeSim.unblock() // see unblock's doc comment: prevents sim.Close() from hanging on a failed spec
		sim.Close()
		st.Close()
		cancel()
	})

	workerCfg := func() config.SimulatorConfig {
		return config.SimulatorConfig{
			Enabled: true, ControlURL: sim.URL,
			MaxConcurrentRuns: 1, PollInterval: 30 * time.Millisecond, JanitorInterval: time.Hour,
			RunTTL: 10 * time.Second, RetentionTTL: time.Hour,
		}
	}

	It("never lets two RunWorker instances run the sandbox concurrently, even with MaxConcurrentRuns=1 each sharing one DB", func() {
		for i := 0; i < 3; i++ {
			seedQueuedRun(ctx, st, orgID, "prometheus.relabel")
		}

		cfg := workerCfg()
		workerA := worker.New(st, reg, v, cfg, slog.Default().With("worker", "A"))
		workerB := worker.New(st, reg, v, cfg, slog.Default().With("worker", "B"))
		workerA.Start(ctx)
		workerB.Start(ctx)

		// Give both workers a real window to race for the same slot before
		// releasing anything. If the advisory lock were skipped (claim
		// unconditionally), two workers would each claim a distinct row and
		// call the fake simulator at the same time, and maxSeen would
		// observe 2 here.
		Consistently(func() int32 {
			return atomic.LoadInt32(&fakeSim.maxSeen)
		}, 400*time.Millisecond, 20*time.Millisecond).Should(BeNumerically("<=", 1))
		Expect(atomic.LoadInt32(&fakeSim.current)).To(Equal(int32(1)),
			"exactly one worker should be holding the slot (blocked in the fake simulator) by now")

		fakeSim.unblock()

		Eventually(func() bool {
			ids := mustListRuns(ctx, st, orgID)
			if len(ids) != 3 {
				return false
			}
			for _, id := range ids {
				row, err := st.Queries.GetSimulateRunByID(ctx, id)
				Expect(err).NotTo(HaveOccurred())
				if row.Status != simulate.RunStatusCompleted {
					return false
				}
			}
			return true
		}, 5*time.Second, 50*time.Millisecond).Should(BeTrue(), "all 3 rows must eventually reach a terminal state")
	})

	It("takes a cannot_stub graph from queued to failed with error_code=cannot_stub end to end", func() {
		run := seedQueuedRun(ctx, st, orgID, "discovery.process") // Sources category, no discovery_stub in the shipped overlay

		cfg := workerCfg()
		w := worker.New(st, reg, v, cfg, slog.Default().With("worker", "solo"))
		w.Start(ctx)

		Eventually(func() string {
			row, err := st.Queries.GetSimulateRunByID(ctx, run.ID)
			Expect(err).NotTo(HaveOccurred())
			return row.Status
		}, 5*time.Second, 50*time.Millisecond).Should(Equal(simulate.RunStatusFailed))

		row, err := st.Queries.GetSimulateRunByID(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(row.ErrorCode).To(Equal(simulate.RunErrorCannotStub))
		Expect(row.ErrorMessage).To(ContainSubstring("cannot stub discovery.process"))
	})

	// #253: a stubbed discovery node used to come back "healthy" — the health
	// of the discovery.relabel stand-in, not of the component the user drew,
	// which never ran. The stored health now says "stubbed" and names the
	// stand-in; a node that really ran keeps its sandbox-reported state.
	It("reports a stubbed discovery node as stubbed, not healthy, while a real node keeps its health", func() {
		graph := visual.GraphDocument{
			Kind:          "alloy-graph/v1",
			SchemaVersion: "alloy-v1.18.1",
			Nodes: []visual.GraphNode{
				{ID: "k8s", Component: "discovery.kubernetes", Label: "pods", Props: map[string]interface{}{"role": "pod"}},
				{ID: "rl", Component: "prometheus.relabel", Label: "sink", Props: map[string]interface{}{}},
			},
		}
		graphJSON, err := json.Marshal(graph)
		Expect(err).NotTo(HaveOccurred())
		run, err := st.Queries.CreateSimulateRun(ctx, sqlc.CreateSimulateRunParams{
			OrgID: orgID, Graph: graphJSON, RequestedDurationSeconds: 15, CreatedBy: "worker-test",
		})
		Expect(err).NotTo(HaveOccurred())
		fakeSim.unblock()

		cfg := workerCfg()
		cfg.TargetAddress = "shepherd-simulator:9111" // the config default; stubs need it
		w := worker.New(st, reg, v, cfg, slog.Default().With("worker", "solo"))
		w.Start(ctx)

		Eventually(func() string {
			row, err := st.Queries.GetSimulateRunByID(ctx, run.ID)
			Expect(err).NotTo(HaveOccurred())
			return row.Status
		}, 5*time.Second, 50*time.Millisecond).Should(BeElementOf(simulate.RunStatusCompleted, simulate.RunStatusFailed))

		row, err := st.Queries.GetSimulateRunByID(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(row.Status).To(Equal(simulate.RunStatusCompleted), "%s: %s", row.ErrorCode, row.ErrorMessage)
		var health []simulate.RunComponentHealth
		Expect(json.Unmarshal(row.ComponentHealth, &health)).To(Succeed())
		byNode := map[string]simulate.RunComponentHealth{}
		for _, h := range health {
			byNode[h.NodeID] = h
		}
		Expect(byNode).To(HaveKey("k8s"))
		Expect(byNode["k8s"].HealthState).To(Equal(simulate.HealthStateStubbed))
		Expect(byNode["k8s"].Component).To(Equal("discovery.kubernetes"))
		Expect(byNode["k8s"].Message).To(ContainSubstring("did not run"))
		Expect(byNode["k8s"].Message).To(ContainSubstring("k8s-pod-targets"))
		Expect(byNode).To(HaveKey("rl"))
		Expect(byNode["rl"].HealthState).To(Equal("healthy"))
	})
})

// mustListRuns returns every simulate_runs id for orgID, seeded earlier in
// the same test via seedQueuedRun. It re-derives ids from the store rather
// than threading return values through the seeding loop, so the concurrency
// spec above can seed rows in a plain loop.
func mustListRuns(ctx context.Context, st *store.Store, orgID pgtype.UUID) []pgtype.UUID {
	rows, err := st.Pool().Query(ctx, `SELECT id FROM simulate_runs WHERE org_id = $1`, orgID)
	Expect(err).NotTo(HaveOccurred())
	defer rows.Close()
	var ids []pgtype.UUID
	for rows.Next() {
		var id pgtype.UUID
		Expect(rows.Scan(&id)).To(Succeed())
		ids = append(ids, id)
	}
	Expect(rows.Err()).NotTo(HaveOccurred())
	return ids
}
