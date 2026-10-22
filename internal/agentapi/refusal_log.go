package agentapi

import (
	"log/slog"
	"sync"
	"time"

	"shepherd/internal/metrics"
)

const (
	// refusalLogWindow is how long after a Warn for one (instance id, reason)
	// further refusals for it are only counted.
	refusalLogWindow = 5 * time.Minute
	// refusalLogMax bounds the remembered keys. A refused collector retries
	// every poll interval, so without the window one misconfigured fleet would
	// log a Warn per instance per poll; without the bound, a caller cycling
	// instance ids would grow the map without limit.
	refusalLogMax = 4096
)

type refusalKey struct{ instanceID, reason string }

// refusalLog counts every identity refusal in shepherd_agent_refused_total and
// logs the first per (instance id, reason) per window at Warn; the rest are
// counted only. In-process: each replica keeps its own window.
type refusalLog struct {
	mu     sync.Mutex
	window time.Duration
	max    int
	last   map[refusalKey]time.Time
	now    func() time.Time
}

func newRefusalLog(window time.Duration, maxKeys int) *refusalLog {
	return &refusalLog{window: window, max: maxKeys, last: make(map[refusalKey]time.Time), now: time.Now}
}

// record counts one refusal and reports whether it was logged. attrs are
// extra ids for the log line — never credentials or claim contents.
func (l *refusalLog) record(logger *slog.Logger, rpc, reason, instanceID string, attrs ...any) bool {
	metrics.AgentRefusedTotal.WithLabelValues(reason).Inc()
	if !l.allow(refusalKey{instanceID: instanceID, reason: reason}) {
		return false
	}
	logger.Warn("collector request refused",
		append([]any{"rpc", rpc, "reason", reason, "instance_id", instanceID}, attrs...)...)
	return true
}

func (l *refusalLog) allow(k refusalKey) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if t, ok := l.last[k]; ok && now.Sub(t) < l.window {
		return false
	}
	if _, ok := l.last[k]; !ok && len(l.last) >= l.max {
		for key, t := range l.last {
			if now.Sub(t) >= l.window {
				delete(l.last, key)
			}
		}
		if len(l.last) >= l.max {
			// Every remembered key is inside its window: start over rather
			// than grow. The cost is at most one extra Warn per key.
			clear(l.last)
		}
	}
	l.last[k] = now
	return true
}
