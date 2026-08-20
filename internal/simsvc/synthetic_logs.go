package simsvc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"shepherd/internal/simulate"
)

// DefaultLogEmitInterval is how often the emitter appends one round of fixture
// lines. Fast enough that a 15-second e2e run captures several rounds, slow
// enough that a 120-second run does not fill the 64 MiB tmpfs.
const DefaultLogEmitInterval = 500 * time.Millisecond

// LogEmitter writes the fixture lines a loki_file stub tails. The file name is
// built by simulate.StubLogFileName, the same function the transform uses to
// write __path__, so a rename cannot leave the tailer pointed at a file
// nobody writes.
type LogEmitter struct {
	dir      string
	fixtures []string
	interval time.Duration
}

// NewLogEmitter validates the requested fixture names against the shared
// library. An unknown name is an error rather than a skipped file: silently
// writing nothing produces a run that captures no logs, which reads as a
// broken loki.process chain.
func NewLogEmitter(dir string, fixtures []string, interval time.Duration) (*LogEmitter, error) {
	if interval <= 0 {
		interval = DefaultLogEmitInterval
	}
	for _, f := range fixtures {
		if _, ok := simulate.StubLogLines(f); !ok {
			return nil, fmt.Errorf("simsvc: unknown log fixture %q", f)
		}
	}
	return &LogEmitter{dir: dir, fixtures: fixtures, interval: interval}, nil
}

// Prepare truncates one file per fixture so a run never tails the previous
// run's lines. loki.source.file reads from the start of a file it has not seen
// before, so leftover content would appear as this run's capture.
func (e *LogEmitter) Prepare() error {
	if len(e.fixtures) == 0 {
		return nil
	}
	if err := os.MkdirAll(e.dir, 0o750); err != nil {
		return fmt.Errorf("simsvc: create log dir: %w", err)
	}
	for _, f := range e.fixtures {
		path := filepath.Join(e.dir, simulate.StubLogFileName(f))
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			return fmt.Errorf("simsvc: truncate %s: %w", path, err)
		}
	}
	return nil
}

// Run appends one round of every fixture's lines per interval until ctx ends.
func (e *LogEmitter) Run(ctx context.Context) {
	if len(e.fixtures) == 0 {
		return
	}
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	// Emit immediately so a short run is not entirely start-up latency.
	e.emit()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.emit()
		}
	}
}

func (e *LogEmitter) emit() {
	for _, fixture := range e.fixtures {
		lines, ok := simulate.StubLogLines(fixture)
		if !ok {
			continue
		}
		path := filepath.Join(e.dir, simulate.StubLogFileName(fixture))
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // path is built from a validated fixture name under the run's own dir
		if err != nil {
			return
		}
		var b strings.Builder
		for _, line := range lines {
			// A fixture line may itself be multiline (the stacktrace one is);
			// it goes in verbatim so a stage.multiline downstream has
			// something real to reassemble.
			b.WriteString(line)
			b.WriteString("\n")
		}
		_, _ = f.WriteString(b.String()) //nolint:errcheck // a failed append shows up as missing captures, which is the signal
		_ = f.Close()                    //nolint:errcheck // best effort; the next tick reopens
	}
}
