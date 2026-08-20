package simsvc

import (
	"sort"
	"strings"
	"sync"
)

// Result caps. Every cap has a companion *_truncated flag on Results because
// "nothing captured" is the signal §6.4 exists to surface: a silently trimmed
// result set and an empty one must never look alike to the UI.
const (
	MaxSeries     = 2000
	MaxLogLines   = 2000
	MaxStderrTail = 200
	// MaxLogLineBytes bounds a single captured line. A run that ships one
	// 50 MB line would otherwise be answered back to the browser verbatim.
	MaxLogLineBytes = 8192
)

// Series is one captured time series. First/Last values and timestamps make a
// counter's advance visible without shipping every sample.
type Series struct {
	Name             string            `json:"name"`
	Labels           map[string]string `json:"labels"`
	SampleCount      int               `json:"sample_count"`
	FirstValue       float64           `json:"first_value"`
	LastValue        float64           `json:"last_value"`
	FirstTimestampMS int64             `json:"first_timestamp_ms"`
	LastTimestampMS  int64             `json:"last_timestamp_ms"`
}

// LogLine is one captured log entry at the Loki push receiver.
type LogLine struct {
	Labels      map[string]string `json:"labels"`
	Line        string            `json:"line"`
	TimestampMS int64             `json:"timestamp_ms"`
}

// OTLPCounts summarises what arrived at the OTLP receivers. Counts rather than
// payloads: the OTLP destinations exist in the graph to prove the wire works,
// and a full decode dump would dwarf every other result.
type OTLPCounts struct {
	MetricPoints       int      `json:"metric_points"`
	LogRecords         int      `json:"log_records"`
	Spans              int      `json:"spans"`
	ResourceAttributes []string `json:"resource_attributes"`
}

// OtherCounts covers the destinations whose bodies the harness accepts and
// counts but does not decode: a non-zero count is the evidence the pipeline
// delivered, which is all the results view claims for them.
type OtherCounts struct {
	PyroscopeRequests int `json:"pyroscope_requests"`
	FaroRequests      int `json:"faro_requests"`
	SplunkHECRequests int `json:"splunkhec_requests"`
	SyslogMessages    int `json:"syslog_messages"`
	FileExportBytes   int `json:"file_export_bytes"`
}

// ComponentHealth is one entry from the sandbox Alloy's own components API,
// re-addressed to the graph node it came from via the run's component index.
//
// Health is NOT evidence of delivery: measured against real Alloy v1.18.1, a
// prometheus.remote_write whose endpoint does not resolve stays "healthy"
// while it retries. Callers must key "the pipeline worked" on captured
// content, never on this field.
type ComponentHealth struct {
	LocalID   string `json:"local_id"`
	NodeID    string `json:"node_id"`
	Health    string `json:"health"`
	Message   string `json:"message"`
	UpdatedAt string `json:"updated_at"`
}

// Results is everything one sandbox run observed.
type Results struct {
	Series            []Series          `json:"series"`
	SeriesTruncated   bool              `json:"series_truncated"`
	LogLines          []LogLine         `json:"log_lines"`
	LogLinesTruncated bool              `json:"log_lines_truncated"`
	OTLP              OTLPCounts        `json:"otlp"`
	Other             OtherCounts       `json:"other"`
	Components        []ComponentHealth `json:"components"`
	StderrTail        []string          `json:"stderr_tail"`
	StderrTruncated   bool              `json:"stderr_truncated"`
}

// sink accumulates captures for the single active run. It is shared by every
// receiver and is the only mutable state the harness owns.
type sink struct {
	mu       sync.Mutex
	series   map[string]*Series
	seriesOF bool
	logs     []LogLine
	logsOF   bool
	otlp     OTLPCounts
	otlpAttr map[string]struct{}
	other    OtherCounts
}

func newSink() *sink {
	return &sink{series: map[string]*Series{}, otlpAttr: map[string]struct{}{}}
}

// addSample records one sample of one series, keyed by its full label set so a
// relabel-produced label makes a distinct entry — which is exactly what the
// e2e assertion inspects.
func (s *sink) addSample(labels map[string]string, value float64, timestampMS int64) {
	key := seriesKey(labels)
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.series[key]
	if !ok {
		if len(s.series) >= MaxSeries {
			s.seriesOF = true
			return
		}
		existing = &Series{
			Name: labels["__name__"], Labels: labels,
			FirstValue: value, FirstTimestampMS: timestampMS,
		}
		s.series[key] = existing
	}
	existing.SampleCount++
	existing.LastValue = value
	existing.LastTimestampMS = timestampMS
}

func (s *sink) addLogLine(l LogLine) {
	if len(l.Line) > MaxLogLineBytes {
		l.Line = l.Line[:MaxLogLineBytes]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.logs) >= MaxLogLines {
		s.logsOF = true
		return
	}
	s.logs = append(s.logs, l)
}

func (s *sink) addOTLP(metricPoints, logRecords, spans int, attrs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.otlp.MetricPoints += metricPoints
	s.otlp.LogRecords += logRecords
	s.otlp.Spans += spans
	for _, a := range attrs {
		s.otlpAttr[a] = struct{}{}
	}
}

func (s *sink) addOther(mutate func(*OtherCounts)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mutate(&s.other)
}

// snapshot renders the accumulated captures in a deterministic order, so two
// polls of the same finished run answer byte-identically and an e2e assertion
// can compare whole slices.
func (s *sink) snapshot() Results {
	s.mu.Lock()
	defer s.mu.Unlock()

	keys := make([]string, 0, len(s.series))
	for k := range s.series {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	series := make([]Series, 0, len(keys))
	for _, k := range keys {
		series = append(series, *s.series[k])
	}

	logs := make([]LogLine, len(s.logs))
	copy(logs, s.logs)

	attrs := make([]string, 0, len(s.otlpAttr))
	for a := range s.otlpAttr {
		attrs = append(attrs, a)
	}
	sort.Strings(attrs)

	otlp := s.otlp
	otlp.ResourceAttributes = attrs
	return Results{
		Series: series, SeriesTruncated: s.seriesOF,
		LogLines: logs, LogLinesTruncated: s.logsOF,
		OTLP: otlp, Other: s.other,
	}
}

// seriesKey is a collision-free rendering of a label set: sorted name=value
// pairs joined by a byte that cannot appear in a Prometheus label name.
func seriesKey(labels map[string]string) string {
	names := make([]string, 0, len(labels))
	for n := range labels {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte('\x00')
		b.WriteString(labels[n])
		b.WriteByte('\x01')
	}
	return b.String()
}

// ringBuffer keeps the last n lines of the sandbox's stderr. Alloy's start-up
// diagnostics are the most useful thing a failed run returns, and they arrive
// before anything else, so a plain "last n" would lose them — hence the
// separate head retention.
type ringBuffer struct {
	mu       sync.Mutex
	head     []string
	tail     []string
	n        int
	dropped  bool
	headKeep int
}

func newRingBuffer(n int) *ringBuffer {
	return &ringBuffer{n: n, headKeep: n / 2}
}

func (r *ringBuffer) add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.head) < r.headKeep {
		r.head = append(r.head, line)
		return
	}
	r.tail = append(r.tail, line)
	if len(r.tail) > r.n-r.headKeep {
		r.tail = r.tail[1:]
		r.dropped = true
	}
}

func (r *ringBuffer) lines() ([]string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.head)+len(r.tail))
	out = append(out, r.head...)
	out = append(out, r.tail...)
	return out, r.dropped
}
