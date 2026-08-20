package simsvc

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/golang/snappy"
	"github.com/prometheus/prometheus/prompb"
)

// maxCaptureBody bounds one capture request. Alloy's own remote_write batches
// are far below this; the cap exists so a hand-crafted body cannot exhaust the
// simulator's 512 MiB.
const maxCaptureBody = 32 << 20

// errRemoteWriteV2 is returned for a Remote-Write 2.0 body. The 2.0 wire format
// interns label strings in a symbol table, so decoding it as a 1.0
// WriteRequest yields empty labels rather than an error — a silent wrong
// answer, which is why the version header is checked instead of guessed.
var errRemoteWriteV2 = errors.New("remote write 2.0 not supported by the sandbox capture receiver")

// decodeRemoteWrite turns a prometheus.remote_write request body into the
// series it carries. Verified against real Alloy v1.18.1: snappy block format
// wrapping a prompb.WriteRequest.
func decodeRemoteWrite(header http.Header, body io.Reader) (*prompb.WriteRequest, error) {
	if v := header.Get("X-Prometheus-Remote-Write-Version"); v == "2.0.0" {
		return nil, errRemoteWriteV2
	}
	raw, err := io.ReadAll(io.LimitReader(body, maxCaptureBody))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	decoded, err := snappy.Decode(nil, raw)
	if err != nil {
		return nil, fmt.Errorf("snappy decode: %w", err)
	}
	var req prompb.WriteRequest
	if err := req.Unmarshal(decoded); err != nil {
		return nil, fmt.Errorf("unmarshal write request: %w", err)
	}
	return &req, nil
}

// handlePrometheusWrite serves simulate.CapturePathPrometheus.
func (h *Harness) handlePrometheusWrite(w http.ResponseWriter, r *http.Request) {
	req, err := decodeRemoteWrite(r.Header, r.Body)
	if err != nil {
		status := http.StatusBadRequest
		h.logger.Warn("capture: remote_write decode failed", "error", err)
		http.Error(w, err.Error(), status)
		return
	}
	s := h.activeSink()
	if s == nil {
		// No run owns this traffic: a straggler from a run that already
		// finished. Answering 204 keeps Alloy from retry-storming the
		// harness while the next run starts.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Indexed rather than ranged by value: a prompb.TimeSeries is 128 bytes and
	// a single batch carries hundreds of them.
	for i := range req.Timeseries {
		ts := &req.Timeseries[i]
		labels := make(map[string]string, len(ts.Labels))
		for _, l := range ts.Labels {
			labels[l.Name] = l.Value
		}
		for _, sample := range ts.Samples {
			s.addSample(labels, sample.Value, sample.Timestamp)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
