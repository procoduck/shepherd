package gitrepo

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// Default limits, matching docs/git-provider-design.md §3.6.
const (
	DefaultMaxRepoBytes = 50 * 1024 * 1024
	DefaultMaxFileBytes = 1 * 1024 * 1024
	DefaultMaxFiles     = 500
	DefaultFetchTimeout = 60 * time.Second
)

// Limits bounds the resources one Repo operation (LatestCommit or Files)
// may consume. The zero value is filled in with DefaultLimits by
// Limits.withDefaults, so an unset Repo.Limits is safe.
type Limits struct {
	// MaxRepoBytes caps the total bytes read from the remote across an
	// entire fetch (info/refs plus all pack data). Enforced by a counting
	// reader on the transport, so it trips on bytes actually transferred,
	// not on a post-hoc size check.
	MaxRepoBytes int64
	// MaxFileBytes caps the size of any single *.alloy file. A file over
	// the limit is skipped (a diagnostic is logged), not fatal to the
	// call.
	MaxFileBytes int64
	// MaxFiles caps the number of *.alloy files Files may return. Found
	// more than this and the whole call aborts with ErrTooManyFiles.
	MaxFiles int
	// FetchTimeout bounds the wall-clock time of one LatestCommit or
	// Files call.
	FetchTimeout time.Duration
}

// DefaultLimits returns the limits from docs/git-provider-design.md §3.6.
func DefaultLimits() Limits {
	return Limits{
		MaxRepoBytes: DefaultMaxRepoBytes,
		MaxFileBytes: DefaultMaxFileBytes,
		MaxFiles:     DefaultMaxFiles,
		FetchTimeout: DefaultFetchTimeout,
	}
}

// withDefaults returns l with every zero-or-negative field replaced by
// its default.
func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxRepoBytes <= 0 {
		l.MaxRepoBytes = d.MaxRepoBytes
	}
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = d.MaxFileBytes
	}
	if l.MaxFiles <= 0 {
		l.MaxFiles = d.MaxFiles
	}
	if l.FetchTimeout <= 0 {
		l.FetchTimeout = d.FetchTimeout
	}
	return l
}

// ErrRepoTooLarge is returned when a fetch transfers more bytes than
// Limits.MaxRepoBytes allows.
var ErrRepoTooLarge = errors.New("gitrepo: repository exceeds max_repo_bytes limit")

// byteCounter is shared by every HTTP request one operation makes, so the
// limit applies to the whole fetch rather than to each request in
// isolation.
type byteCounter struct {
	n     atomic.Int64
	limit int64
}

func newByteCounter(limit int64) *byteCounter {
	return &byteCounter{limit: limit}
}

func (c *byteCounter) exceeded() bool {
	return c.n.Load() > c.limit
}

// countingTransport wraps an http.RoundTripper so every byte read from a
// response body counts against the shared limit, aborting the read as soon
// as the limit is crossed instead of only noticing after the whole body
// has already been buffered in memory.
//
// This aborts most oversized transfers immediately, mid-stream. It is not
// by itself a guaranteed trip point, though: a caller that reads a small,
// self-delimited response (e.g. go-git's pktline ref advertisement) in one
// Read call gets its data and the wrapped error together per the
// io.Reader contract, and may act on the data without ever inspecting
// that trailing error if it already has everything it needed. Repo.go's
// wrapFetchErr checks byteCounter.exceeded() unconditionally — including
// on an apparently successful clone/list — as the authoritative backstop
// so the limit can never be silently ignored that way.
type countingTransport struct {
	next    http.RoundTripper
	counter *byteCounter
}

func (t *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &countingReadCloser{rc: resp.Body, counter: t.counter}
	return resp, nil
}

type countingReadCloser struct {
	rc      io.ReadCloser
	counter *byteCounter
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if n > 0 {
		// Override even a clean io.EOF: the chunk that crosses the limit
		// is itself the trip condition, not a successful read.
		if total := c.counter.n.Add(int64(n)); total > c.counter.limit {
			err = fmt.Errorf("%w: read %d bytes, limit %d", ErrRepoTooLarge, total, c.counter.limit)
		}
	}
	return n, err
}

func (c *countingReadCloser) Close() error { return c.rc.Close() }
