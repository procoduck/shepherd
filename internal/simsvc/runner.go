package simsvc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// killGrace is how long after the run's duration the sandbox Alloy is given to
// exit on SIGTERM before it is killed outright.
const killGrace = 15 * time.Second

// healthPollInterval is how often the sandbox's own components API is polled.
const healthPollInterval = 2 * time.Second

// alloyComponent is one entry from Alloy's /api/v0/web/components.
//
// The field names are load-bearing and were read off real grafana/alloy
// v1.18.1: the identifier is `localID`, NOT `id`, and health is a nested
// object. Decoding into the wrong field names yields a components list of
// empty strings — health badges that say nothing, painted on every node.
type alloyComponent struct {
	LocalID string `json:"localID"`
	Label   string `json:"label"`
	Health  struct {
		State       string `json:"state"`
		Message     string `json:"message"`
		UpdatedTime string `json:"updatedTime"`
	} `json:"health"`
}

// runnerOptions is everything one sandbox execution needs.
type runnerOptions struct {
	Config         string
	Duration       time.Duration
	ComponentIndex map[string]string
	RunDir         string
	StorageDir     string
	AlloyBinary    string
	AlloyHTTP      string
	StabilityLevel string
}

// runOutcome is what one sandbox execution produced.
type runOutcome struct {
	Components []ComponentHealth
	StderrTail []string
	StderrCut  bool
	// Err is non-nil only when the run failed as a RESULT (Alloy could not
	// load the config, or the binary is missing). Per §6.4 a start-time
	// failure is the SIGNAL: it is returned to the caller as a completed poll
	// carrying diagnostics, never as a transport error.
	Err error
}

// errAlloyStartFailed marks the "Alloy exited during initial load" outcome.
var errAlloyStartFailed = errors.New("alloy_start_failed")

// runAlloy writes the config, runs the sandbox for the requested duration,
// polls component health, and returns what it saw. It always tears the run
// directory down.
func runAlloy(ctx context.Context, opts runnerOptions, logger *slog.Logger) runOutcome {
	if err := os.MkdirAll(opts.RunDir, 0o750); err != nil {
		return runOutcome{Err: fmt.Errorf("create run dir: %w", err)}
	}
	defer os.RemoveAll(opts.RunDir) //nolint:errcheck // tmpfs teardown; a leak is bounded by the tmpfs size
	if err := os.MkdirAll(opts.StorageDir, 0o750); err != nil {
		return runOutcome{Err: fmt.Errorf("create storage dir: %w", err)}
	}
	defer os.RemoveAll(opts.StorageDir) //nolint:errcheck // same

	cfgPath := filepath.Join(opts.RunDir, "config.alloy")
	if err := os.WriteFile(cfgPath, []byte(opts.Config), 0o600); err != nil {
		return runOutcome{Err: fmt.Errorf("write config: %w", err)}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(runCtx, opts.AlloyBinary, //nolint:gosec // the binary path is operator config, not user input
		"run", cfgPath,
		"--storage.path="+opts.StorageDir,
		"--server.http.listen-addr="+opts.AlloyHTTP,
		"--disable-reporting",
		"--server.http.enable-pprof=false",
		"--server.http.disable-support-bundle",
		"--stability.level="+opts.StabilityLevel,
	)
	// SIGTERM first, then SIGKILL after the grace period: Alloy flushes its
	// remote_write queue on shutdown, and killing it outright loses the last
	// batch — which the results view would show as a truncated capture.
	cmd.WaitDelay = killGrace
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return runOutcome{Err: fmt.Errorf("stderr pipe: %w", err)}
	}
	// Alloy writes its own logs to stderr; stdout carries nothing we need, and
	// discarding it keeps a chatty component from filling a pipe nobody reads.
	cmd.Stdout = io.Discard

	if err := cmd.Start(); err != nil {
		return runOutcome{Err: fmt.Errorf("%w: %w", errAlloyStartFailed, err)}
	}

	tail := newRingBuffer(MaxStderrTail)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			tail.add(scanner.Text())
		}
	}()

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	health, pollDone := pollHealth(runCtx, opts, logger)

	var runErr error
	select {
	case err := <-exited:
		// Exiting before the duration elapsed means the initial load failed.
		// That is a result, not an API error (§6.4: failures are the SIGNAL).
		runErr = fmt.Errorf("%w: alloy exited during the run: %w", errAlloyStartFailed, err)
	case <-time.After(opts.Duration):
		cancel()
		<-exited
	case <-ctx.Done():
		cancel()
		<-exited
		runErr = ctx.Err()
	}
	// Cancel before snapshotting: the poller writes to the tracker until its
	// context ends, and reading it while it runs is a race.
	cancel()
	<-pollDone
	<-stderrDone

	lines, cut := tail.lines()
	return runOutcome{
		Components: health.snapshot(opts.ComponentIndex),
		StderrTail: lines,
		StderrCut:  cut,
		Err:        runErr,
	}
}

// healthTracker keeps the latest health seen for each component. Latest rather
// than final: a component that goes unhealthy and is then torn down at
// shutdown would otherwise report "exited" and hide the failure the run
// existed to surface.
type healthTracker struct {
	mu     sync.Mutex
	latest map[string]alloyComponent
	order  []string
}

func newHealthTracker() *healthTracker {
	return &healthTracker{latest: map[string]alloyComponent{}}
}

func (t *healthTracker) observe(components []alloyComponent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range components {
		if _, seen := t.latest[c.LocalID]; !seen {
			t.order = append(t.order, c.LocalID)
		}
		// An unhealthy observation is sticky: shutdown flips every component
		// to a benign state, and a run whose destination was unhealthy for 25
		// of its 30 seconds must not report "healthy".
		if prev, ok := t.latest[c.LocalID]; ok && prev.Health.State == "unhealthy" && c.Health.State != "unhealthy" {
			continue
		}
		t.latest[c.LocalID] = c
	}
}

func (t *healthTracker) snapshot(index map[string]string) []ComponentHealth {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]ComponentHealth, 0, len(t.order))
	for _, id := range t.order {
		c := t.latest[id]
		out = append(out, ComponentHealth{
			LocalID:   c.LocalID,
			NodeID:    index[c.LocalID],
			Health:    c.Health.State,
			Message:   c.Health.Message,
			UpdatedAt: c.Health.UpdatedTime,
		})
	}
	return out
}

// pollHealth reads the sandbox's components API until the run context ends.
// The returned channel closes once the poller has stopped writing, which is
// what makes the tracker safe to snapshot.
func pollHealth(ctx context.Context, opts runnerOptions, logger *slog.Logger) (*healthTracker, <-chan struct{}) {
	tracker := newHealthTracker()
	done := make(chan struct{})
	go func() {
		defer close(done)
		client := &http.Client{Timeout: 2 * time.Second}
		url := "http://" + opts.AlloyHTTP + "/api/v0/web/components"
		ticker := time.NewTicker(healthPollInterval)
		defer ticker.Stop()
		for {
			if components, err := fetchComponents(ctx, client, url); err != nil {
				logger.Debug("sandbox: component health poll failed", "error", err)
			} else {
				tracker.observe(components)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return tracker, done
}

func fetchComponents(ctx context.Context, client *http.Client, url string) ([]alloyComponent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only poll; close error is not actionable
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("components api: status %d", resp.StatusCode)
	}
	var components []alloyComponent
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&components); err != nil {
		return nil, fmt.Errorf("components api: decode: %w", err)
	}
	return components, nil
}
