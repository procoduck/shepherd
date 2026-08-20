package simsvc

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// testConfig is a Config whose directories are all inside dir, so a spec never
// touches /tmp/shepherd-sim.
func testConfig(dir string) Config {
	return Config{
		Listen:          "127.0.0.1:0",
		CaptureListen:   "127.0.0.1:0",
		SyntheticListen: "127.0.0.1:0",
		OTLPGRPCListen:  "127.0.0.1:0",
		SyslogListen:    "127.0.0.1:0",
		AlloyBinary:     "/nonexistent/alloy",
		AlloyHTTPListen: "127.0.0.1:0",
		StabilityLevel:  DefaultStabilityLevel,
		QueueDepth:      4,
		DefaultDuration: 30 * time.Second,
		MaxDuration:     120 * time.Second,
		RunTTL:          5 * time.Minute,
		ResultTTL:       time.Hour,
		StorageDir:      dir + "/storage",
		RunDir:          dir + "/run",
		CaptureDir:      dir + "/capture",
		LogDir:          dir + "/logs",
		AllowedHosts:    []string{"shepherd-simulator", "127.0.0.1", "localhost"},
		SATokenPath:     dir + "/absent-token",
		MaxConfigBytes:  DefaultMaxConfigBytes,
	}
}

// newTestQueue builds a queue whose sandbox execution is the supplied stub, so
// the queue's own behaviour is provable without an Alloy binary.
func newTestQueue(cfg Config, execute func(context.Context, runnerOptions) runOutcome) *Queue {
	q := NewQueue(cfg, NewHarness(nil), NewSyntheticExporter(), nil)
	q.execute = execute
	return q
}

var _ = Describe("Run queue", func() {
	var cfg Config

	BeforeEach(func() { cfg = testConfig(GinkgoT().TempDir()) })

	It("runs exactly one sandbox at a time", func() {
		// The capture URLs carry no run discriminator: two concurrent runs
		// would post to the same paths and merge one user's series into
		// another's. This assertion is the enforcement of that constraint.
		var concurrent, peak atomic.Int32
		release := make(chan struct{})
		queue := newTestQueue(cfg, func(ctx context.Context, _ runnerOptions) runOutcome {
			n := concurrent.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			<-release
			concurrent.Add(-1)
			return runOutcome{}
		})
		DeferCleanup(queue.Close)

		var ids []string
		for range 3 {
			run, err := queue.Submit(StartRequest{Config: "// config"})
			Expect(err).NotTo(HaveOccurred())
			ids = append(ids, run.ID)
		}

		Eventually(concurrent.Load).Should(Equal(int32(1)))
		Consistently(peak.Load, 300*time.Millisecond).Should(Equal(int32(1)))

		close(release)
		for _, id := range ids {
			Eventually(func() RunState {
				run, err := queue.Get(id)
				Expect(err).NotTo(HaveOccurred())
				return run.State
			}).Should(Equal(StateCompleted))
		}
	})

	It("refuses a submission once the backlog is full", func() {
		cfg.QueueDepth = 1
		release := make(chan struct{})
		queue := newTestQueue(cfg, func(context.Context, runnerOptions) runOutcome {
			<-release
			return runOutcome{}
		})
		DeferCleanup(func() { close(release); queue.Close() })

		first, err := queue.Submit(StartRequest{Config: "// config"})
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() RunState {
			run, err := queue.Get(first.ID)
			Expect(err).NotTo(HaveOccurred())
			return run.State
		}).Should(Equal(StateRunning))

		_, err = queue.Submit(StartRequest{Config: "// config"})
		Expect(err).NotTo(HaveOccurred(), "the first backlog slot must be free")

		_, err = queue.Submit(StartRequest{Config: "// config"})
		Expect(err).To(MatchError(ErrQueueFull))
	})

	It("clamps a duration above the maximum and says so", func() {
		queue := newTestQueue(cfg, func(context.Context, runnerOptions) runOutcome { return runOutcome{} })
		DeferCleanup(queue.Close)

		run, err := queue.Submit(StartRequest{Config: "// config", DurationSeconds: 600})
		Expect(err).NotTo(HaveOccurred())
		Expect(run.DurationSeconds).To(Equal(120))
		// Clamping silently would make a 10-minute request look like it ran
		// for 10 minutes and captured almost nothing.
		Expect(run.DurationClamped).To(BeTrue())
	})

	It("applies the default duration when none is asked for", func() {
		queue := newTestQueue(cfg, func(context.Context, runnerOptions) runOutcome { return runOutcome{} })
		DeferCleanup(queue.Close)

		run, err := queue.Submit(StartRequest{Config: "// config"})
		Expect(err).NotTo(HaveOccurred())
		Expect(run.DurationSeconds).To(Equal(30))
		Expect(run.DurationClamped).To(BeFalse())
	})

	It("passes the requested duration through to the sandbox", func() {
		var seen time.Duration
		var mu sync.Mutex
		queue := newTestQueue(cfg, func(_ context.Context, opts runnerOptions) runOutcome {
			mu.Lock()
			seen = opts.Duration
			mu.Unlock()
			return runOutcome{}
		})
		DeferCleanup(queue.Close)

		run, err := queue.Submit(StartRequest{Config: "// config", DurationSeconds: 15})
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() RunState {
			r, err := queue.Get(run.ID)
			Expect(err).NotTo(HaveOccurred())
			return r.State
		}).Should(Equal(StateCompleted))

		mu.Lock()
		defer mu.Unlock()
		Expect(seen).To(Equal(15 * time.Second))
	})

	It("reports an Alloy start failure as a failed run carrying the stderr tail, not as an API error", func() {
		// §6.4: a config Alloy cannot load is the SIGNAL. Surfacing it as a
		// transport error would hide it behind a generic toast.
		queue := newTestQueue(cfg, func(context.Context, runnerOptions) runOutcome {
			return runOutcome{
				StderrTail: []string{`scrape_timeout (10s) greater than scrape_interval (2s)`},
				Err:        errAlloyStartFailed,
			}
		})
		DeferCleanup(queue.Close)

		run, err := queue.Submit(StartRequest{Config: "// config"})
		Expect(err).NotTo(HaveOccurred())

		var final Run
		Eventually(func() RunState {
			var err error
			final, err = queue.Get(run.ID)
			Expect(err).NotTo(HaveOccurred())
			return final.State
		}).Should(Equal(StateFailed))
		Expect(final.Error).NotTo(BeNil())
		Expect(final.Error.Code).To(Equal("alloy_start_failed"))
		Expect(final.Results).NotTo(BeNil())
		Expect(final.Results.StderrTail).To(ContainElement(ContainSubstring("scrape_timeout")))
	})

	It("attaches captured results to the finished run", func() {
		harness := NewHarness(nil)
		queue := NewQueue(cfg, harness, NewSyntheticExporter(), nil)
		queue.execute = func(context.Context, runnerOptions) runOutcome {
			// Deliver into whatever sink the queue made active — the same
			// path a real capture takes.
			harness.activeSink().addSample(map[string]string{"__name__": "shepherd_sim_requests_total"}, 4, 1234)
			return runOutcome{Components: []ComponentHealth{{LocalID: "prometheus.remote_write.cap", NodeID: "node-3", Health: "healthy"}}}
		}
		DeferCleanup(queue.Close)

		run, err := queue.Submit(StartRequest{Config: "// config"})
		Expect(err).NotTo(HaveOccurred())

		var final Run
		Eventually(func() RunState {
			var err error
			final, err = queue.Get(run.ID)
			Expect(err).NotTo(HaveOccurred())
			return final.State
		}).Should(Equal(StateCompleted))

		Expect(final.Results.Series).To(HaveLen(1))
		Expect(final.Results.Series[0].Name).To(Equal("shepherd_sim_requests_total"))
		// The component index is what lets the UI paint health onto the graph
		// node rather than onto an Alloy local id nobody authored.
		Expect(final.Results.Components).To(HaveLen(1))
		Expect(final.Results.Components[0].NodeID).To(Equal("node-3"))
	})

	It("reports the bytes otelcol.exporter.file wrote, and clears the directory between runs", func() {
		// A file destination delivers to disk, not to a receiver, so bytes in
		// the capture directory are the only evidence it worked. Stale bytes
		// from a previous run would be reported as this run's delivery.
		Expect(os.MkdirAll(cfg.CaptureDir, 0o750)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(cfg.CaptureDir, "previous-run.json"), []byte("stale"), 0o600)).To(Succeed())

		queue := newTestQueue(cfg, func(context.Context, runnerOptions) runOutcome {
			Expect(os.WriteFile(filepath.Join(cfg.CaptureDir, "sink.json"), []byte("0123456789"), 0o600)).To(Succeed())
			return runOutcome{}
		})
		DeferCleanup(queue.Close)

		run, err := queue.Submit(StartRequest{Config: "// config"})
		Expect(err).NotTo(HaveOccurred())

		var final Run
		Eventually(func() RunState {
			final, err = queue.Get(run.ID)
			Expect(err).NotTo(HaveOccurred())
			return final.State
		}).Should(Equal(StateCompleted))

		Expect(final.Results.Other.FileExportBytes).To(Equal(10),
			"only this run's bytes may count; the 5 stale bytes must have been cleared")
	})

	It("cancels a running sandbox and drops its results", func() {
		started := make(chan struct{})
		queue := newTestQueue(cfg, func(ctx context.Context, _ runnerOptions) runOutcome {
			close(started)
			<-ctx.Done()
			return runOutcome{}
		})
		DeferCleanup(queue.Close)

		run, err := queue.Submit(StartRequest{Config: "// config"})
		Expect(err).NotTo(HaveOccurred())
		Eventually(started).Should(BeClosed())

		Expect(queue.Cancel(run.ID)).To(Succeed())
		Eventually(func() RunState {
			r, err := queue.Get(run.ID)
			Expect(err).NotTo(HaveOccurred())
			return r.State
		}).Should(Equal(StateCanceled))
	})

	It("returns not-found for an unknown run", func() {
		queue := newTestQueue(cfg, func(context.Context, runnerOptions) runOutcome { return runOutcome{} })
		DeferCleanup(queue.Close)
		_, err := queue.Get("no-such-run")
		Expect(err).To(MatchError(ErrRunNotFound))
	})

	It("expires a run that outlives its TTL", func() {
		cfg.RunTTL = time.Minute
		release := make(chan struct{})
		queue := newTestQueue(cfg, func(context.Context, runnerOptions) runOutcome {
			<-release
			return runOutcome{}
		})
		DeferCleanup(func() { close(release); queue.Close() })

		run, err := queue.Submit(StartRequest{Config: "// config"})
		Expect(err).NotTo(HaveOccurred())

		// Move the clock past the TTL rather than sleeping through it.
		queue.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
		expired, err := queue.Get(run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(expired.State).To(Equal(StateExpired))
	})

	It("purges a finished run once its retention window has passed", func() {
		queue := newTestQueue(cfg, func(context.Context, runnerOptions) runOutcome { return runOutcome{} })
		DeferCleanup(queue.Close)

		run, err := queue.Submit(StartRequest{Config: "// config"})
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() RunState {
			r, err := queue.Get(run.ID)
			Expect(err).NotTo(HaveOccurred())
			return r.State
		}).Should(Equal(StateCompleted))

		Expect(queue.Sweep()).To(Equal(0), "a fresh result must not be purged")

		queue.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
		Expect(queue.Sweep()).To(Equal(1))
		_, err = queue.Get(run.ID)
		Expect(err).To(MatchError(ErrRunNotFound))
	})

	It("never starts a run that was cancelled while still queued", func() {
		var started atomic.Int32
		release := make(chan struct{})
		releaseOnce := sync.OnceFunc(func() { close(release) })
		queue := newTestQueue(cfg, func(context.Context, runnerOptions) runOutcome {
			started.Add(1)
			<-release
			return runOutcome{}
		})
		DeferCleanup(func() { releaseOnce(); queue.Close() })

		blocking, err := queue.Submit(StartRequest{Config: "// config"})
		Expect(err).NotTo(HaveOccurred())
		Eventually(started.Load).Should(Equal(int32(1)))

		queued, err := queue.Submit(StartRequest{Config: "// config"})
		Expect(err).NotTo(HaveOccurred())
		Expect(queue.Cancel(queued.ID)).To(Succeed())

		releaseOnce()
		Eventually(func() RunState {
			r, err := queue.Get(blocking.ID)
			Expect(err).NotTo(HaveOccurred())
			return r.State
		}).Should(Equal(StateCompleted))
		Consistently(started.Load, 200*time.Millisecond).Should(Equal(int32(1)))
	})
})
