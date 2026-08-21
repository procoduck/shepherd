package simsvc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// RunState is the lifecycle of one sandbox run.
type RunState string

// The run states. They are the states §6.4 step 4 asks the UI to render, plus
// the two terminal states a poll can discover without the client having asked
// for anything (expired, canceled).
const (
	StateQueued     RunState = "queued"
	StateStarting   RunState = "starting"
	StateRunning    RunState = "running"
	StateCollecting RunState = "collecting"
	StateCompleted  RunState = "completed"
	StateFailed     RunState = "failed"
	StateExpired    RunState = "expired"
	StateCanceled   RunState = "canceled"
)

// RunError is the machine-readable failure attached to a finished run.
type RunError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Run is one sandbox run's public state.
type Run struct {
	ID         string    `json:"run_id"`
	State      RunState  `json:"state"`
	AcceptedAt time.Time `json:"accepted_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	// Duration is what the run will actually use; DurationClamped reports that
	// the request asked for more than SIM_MAX_DURATION. Clamping silently
	// would make a 10-minute request look like it ran for 10 minutes and
	// captured almost nothing.
	DurationSeconds int       `json:"duration_seconds"`
	DurationClamped bool      `json:"duration_clamped"`
	Error           *RunError `json:"error,omitempty"`
	Results         *Results  `json:"results,omitempty"`
}

// StartRequest is the control API's POST /v1/runs body.
type StartRequest struct {
	// Config is rendered Alloy text. It is never logged: it is the closest
	// thing to user data this service ever holds.
	Config          string            `json:"config"`
	DurationSeconds int               `json:"duration_seconds"`
	LogFixtures     []string          `json:"log_fixtures"`
	LogEmitInterval int               `json:"log_emit_interval_ms"`
	ComponentIndex  map[string]string `json:"component_index"`
}

// Queue errors surfaced by the control API as 429 / 404.
var (
	ErrQueueFull    = errors.New("queue_full")
	ErrRunNotFound  = errors.New("run_not_found")
	ErrShuttingDown = errors.New("shutting_down")
)

// runRecord is a run plus the internals the queue needs.
type runRecord struct {
	run    Run
	cancel context.CancelFunc
	// purgeAt is when the sweeper may forget this run: RunTTL from acceptance
	// while it is live, ResultTTL from completion once it has finished.
	purgeAt time.Time
}

// Queue serialises runs onto THE SINGLE sandbox slot and owns run state.
//
// One slot, not a configurable pool: the capture URLs the transform writes
// (internal/simulate/harness_paths.go) carry no run discriminator, and neither
// does the synthetic log emitter's file name. Two concurrent runs would post to
// the same paths and tail the same files, and their captures would merge
// silently. Handing one user another user's series is worse than making them
// queue, so concurrency is the thing that gives — and it gives here, in the
// shape of the type, rather than in a config key someone can raise.
type Queue struct {
	cfg      Config
	harness  *Harness
	exporter *SyntheticExporter
	logger   *slog.Logger
	now      func() time.Time
	// execute is the sandbox execution, injectable so the queue's own
	// behaviour (concurrency, backlog, TTL) is testable without an Alloy
	// binary.
	execute func(context.Context, runnerOptions) runOutcome

	mu      sync.Mutex
	runs    map[string]*runRecord
	pending []string
	active  string

	wg     sync.WaitGroup
	closed bool
}

// NewQueue builds a queue that executes runs with the real Alloy runner.
func NewQueue(cfg Config, h *Harness, e *SyntheticExporter, logger *slog.Logger) *Queue {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	q := &Queue{
		cfg: cfg, harness: h, exporter: e, logger: logger,
		now:  time.Now,
		runs: map[string]*runRecord{},
	}
	q.execute = func(ctx context.Context, opts runnerOptions) runOutcome {
		return runAlloy(ctx, opts, logger)
	}
	return q
}

// Submit accepts a run, or refuses it because the backlog is full. It never
// blocks: the caller gets a run id back and polls.
func (q *Queue) Submit(req StartRequest) (Run, error) {
	duration, clamped := q.clampDuration(req.DurationSeconds)

	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return Run{}, ErrShuttingDown
	}
	if len(q.pending) >= q.cfg.QueueDepth {
		q.mu.Unlock()
		return Run{}, ErrQueueFull
	}
	now := q.now()
	rec := &runRecord{
		run: Run{
			ID:              newRunID(),
			State:           StateQueued,
			AcceptedAt:      now,
			ExpiresAt:       now.Add(q.cfg.RunTTL),
			DurationSeconds: int(duration / time.Second),
			DurationClamped: clamped,
		},
		purgeAt: now.Add(q.cfg.RunTTL),
	}
	q.runs[rec.run.ID] = rec
	q.pending = append(q.pending, rec.run.ID)
	out := rec.run
	q.mu.Unlock()

	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		q.serve(out.ID, req, duration)
	}()
	return out, nil
}

// clampDuration applies the default and the ceiling, reporting the ceiling.
func (q *Queue) clampDuration(seconds int) (time.Duration, bool) {
	if seconds <= 0 {
		return q.cfg.DefaultDuration, false
	}
	d := time.Duration(seconds) * time.Second
	if d > q.cfg.MaxDuration {
		return q.cfg.MaxDuration, true
	}
	return d, false
}

// serve waits for the single slot, then runs. It is one goroutine per accepted
// run; the slot mutex is what makes them serial.
func (q *Queue) serve(id string, req StartRequest, duration time.Duration) {
	if !q.acquireSlot(id) {
		return // expired or cancelled while queued
	}
	defer q.releaseSlot(id)

	ctx, cancel := context.WithCancel(context.Background())
	q.mu.Lock()
	rec, ok := q.runs[id]
	if !ok || rec.run.State == StateCanceled || rec.run.State == StateExpired {
		q.mu.Unlock()
		cancel()
		return
	}
	rec.cancel = cancel
	rec.run.State = StateStarting
	q.mu.Unlock()
	defer cancel()

	emitter, err := NewLogEmitter(q.cfg.LogDir, req.LogFixtures, time.Duration(req.LogEmitInterval)*time.Millisecond)
	if err != nil {
		q.finish(id, StateFailed, &RunError{Code: "invalid_request", Message: err.Error()}, nil)
		return
	}
	if err := emitter.Prepare(); err != nil {
		q.finish(id, StateFailed, &RunError{Code: "harness_error", Message: err.Error()}, nil)
		return
	}

	// otelcol.exporter.file writes to disk rather than to a receiver, so the
	// only evidence it delivered is bytes appearing in the capture directory.
	// Clearing it first is what keeps a previous run's output from being
	// reported as this one's.
	if err := clearDir(q.cfg.CaptureDir); err != nil {
		q.finish(id, StateFailed, &RunError{Code: "harness_error", Message: err.Error()}, nil)
		return
	}

	sink := q.harness.begin()
	defer q.harness.end(sink)

	emitterCtx, stopEmitter := context.WithCancel(ctx)
	defer stopEmitter()
	go emitter.Run(emitterCtx)
	go q.advanceExporter(emitterCtx)

	q.setState(id, StateRunning)
	outcome := q.execute(ctx, runnerOptions{
		Config:         req.Config,
		Duration:       duration,
		ComponentIndex: req.ComponentIndex,
		RunDir:         filepath.Join(q.cfg.RunDir, id),
		StorageDir:     filepath.Join(q.cfg.StorageDir, id),
		AlloyBinary:    q.cfg.AlloyBinary,
		AlloyHTTP:      q.cfg.AlloyHTTPListen,
		StabilityLevel: q.cfg.StabilityLevel,
	})
	stopEmitter()

	q.setState(id, StateCollecting)
	results := sink.snapshot()
	results.Other.FileExportBytes = dirBytes(q.cfg.CaptureDir)
	results.Components = outcome.Components
	results.StderrTail = outcome.StderrTail
	results.StderrTruncated = outcome.StderrCut

	state, runErr := StateCompleted, (*RunError)(nil)
	if outcome.Err != nil {
		state = StateFailed
		code := "run_failed"
		if errors.Is(outcome.Err, errAlloyStartFailed) {
			code = "alloy_start_failed"
		}
		runErr = &RunError{Code: code, Message: outcome.Err.Error()}
	}
	q.finish(id, state, runErr, &results)
}

// advanceExporter moves the synthetic series while a run is in flight, so the
// counters the sandbox scrapes actually change between scrapes.
func (q *Queue) advanceExporter(ctx context.Context) {
	if q.exporter == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			q.exporter.Advance()
		}
	}
}

// acquireSlot blocks until this run is at the head of the queue and no other
// run holds the slot, or until the run is cancelled or its TTL expires.
func (q *Queue) acquireSlot(id string) bool {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		q.mu.Lock()
		rec, ok := q.runs[id]
		switch {
		case !ok || rec.run.State == StateCanceled || q.closed:
			q.mu.Unlock()
			return false
		case q.now().After(rec.run.ExpiresAt):
			rec.run.State = StateExpired
			rec.purgeAt = q.now().Add(q.cfg.ResultTTL)
			q.dropPending(id)
			q.mu.Unlock()
			return false
		case q.active == "" && len(q.pending) > 0 && q.pending[0] == id:
			q.active = id
			q.pending = q.pending[1:]
			q.mu.Unlock()
			return true
		}
		q.mu.Unlock()
		<-ticker.C
	}
}

func (q *Queue) releaseSlot(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.active == id {
		q.active = ""
	}
}

// dropPending removes a run from the backlog. Caller holds q.mu.
func (q *Queue) dropPending(id string) {
	for i, p := range q.pending {
		if p == id {
			q.pending = append(q.pending[:i], q.pending[i+1:]...)
			return
		}
	}
}

func (q *Queue) setState(id string, state RunState) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if rec, ok := q.runs[id]; ok && !terminal(rec.run.State) {
		rec.run.State = state
	}
}

func (q *Queue) finish(id string, state RunState, runErr *RunError, results *Results) {
	q.mu.Lock()
	defer q.mu.Unlock()
	rec, ok := q.runs[id]
	if !ok {
		return
	}
	// A cancelled run keeps its cancelled state; overwriting it with
	// "completed" would tell the client its cancel did nothing.
	if rec.run.State != StateCanceled {
		rec.run.State = state
		rec.run.Error = runErr
	}
	rec.run.Results = results
	rec.purgeAt = q.now().Add(q.cfg.ResultTTL)
}

// Get returns a run's current state, applying TTL expiry lazily so a poll
// never sees a run that outlived its budget.
func (q *Queue) Get(id string) (Run, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	rec, ok := q.runs[id]
	if !ok {
		return Run{}, ErrRunNotFound
	}
	if !terminal(rec.run.State) && q.now().After(rec.run.ExpiresAt) {
		rec.run.State = StateExpired
		rec.purgeAt = q.now().Add(q.cfg.ResultTTL)
		if rec.cancel != nil {
			rec.cancel()
		}
	}
	return rec.run, nil
}

// Cancel stops a run and purges its results.
func (q *Queue) Cancel(id string) error {
	q.mu.Lock()
	rec, ok := q.runs[id]
	if !ok {
		q.mu.Unlock()
		return ErrRunNotFound
	}
	rec.run.State = StateCanceled
	rec.run.Results = nil
	rec.purgeAt = q.now().Add(q.cfg.ResultTTL)
	q.dropPending(id)
	cancel := rec.cancel
	q.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// Sweep purges runs past their retention window. Called on a ticker by the
// service and directly by tests.
func (q *Queue) Sweep() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	purged := 0
	for id, rec := range q.runs {
		if now.After(rec.purgeAt) {
			if rec.cancel != nil {
				rec.cancel()
			}
			delete(q.runs, id)
			q.dropPending(id)
			purged++
		}
	}
	return purged
}

// IDs returns every known run id, sorted. It exists so tests can observe
// queue state; nothing in the serving path calls it.
func (q *Queue) IDs() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]string, 0, len(q.runs))
	for id := range q.runs {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Close cancels every live run and waits for its goroutine.
func (q *Queue) Close() {
	q.mu.Lock()
	q.closed = true
	for _, rec := range q.runs {
		if rec.cancel != nil {
			rec.cancel()
		}
		if !terminal(rec.run.State) {
			rec.run.State = StateCanceled
		}
	}
	q.pending = nil
	q.mu.Unlock()
	q.wg.Wait()
}

func terminal(s RunState) bool {
	switch s {
	case StateCompleted, StateFailed, StateExpired, StateCanceled:
		return true
	case StateQueued, StateStarting, StateRunning, StateCollecting:
		return false
	}
	return false
}

// clearDir empties a directory without removing it, so a run starts against a
// known-empty capture area.
func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return os.MkdirAll(dir, 0o750)
	}
	if err != nil {
		return fmt.Errorf("simsvc: read capture dir: %w", err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("simsvc: clear capture dir: %w", err)
		}
	}
	return nil
}

// dirBytes totals the regular files directly under dir. A zero total with a
// healthy otelcol.exporter.file is the "nothing captured" signal, not an error.
func dirBytes(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	total := 0
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		total += int(info.Size())
	}
	return total
}

func newRunID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand cannot fail on any platform this runs on; if it somehow
		// did, a predictable run id is still safe because run ids are not
		// capabilities — Shepherd binds them to an org on its own side.
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}
