package simsvc

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/golang/snappy"
	"github.com/grafana/loki/pkg/push"
)

// decodeLokiPush turns a loki.write request body into the streams it carries.
// Verified against real Alloy v1.18.1: snappy block format wrapping a
// push.PushRequest. The JSON branch exists for hand-testing the harness with
// curl; Alloy never uses it.
func decodeLokiPush(header http.Header, body io.Reader) (*push.PushRequest, error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxCaptureBody))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if strings.HasPrefix(header.Get("Content-Type"), "application/json") {
		return decodeLokiJSON(raw)
	}
	decoded, err := snappy.Decode(nil, raw)
	if err != nil {
		return nil, fmt.Errorf("snappy decode: %w", err)
	}
	var req push.PushRequest
	if err := req.Unmarshal(decoded); err != nil {
		return nil, fmt.Errorf("unmarshal push request: %w", err)
	}
	return &req, nil
}

// decodeLokiJSON parses the /loki/api/v1/push JSON body shape, whose values
// are [unix_nanos_as_string, line] pairs.
func decodeLokiJSON(raw []byte) (*push.PushRequest, error) {
	var doc struct {
		Streams []struct {
			Stream map[string]string `json:"stream"`
			Values [][]string        `json:"values"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("unmarshal json push request: %w", err)
	}
	req := &push.PushRequest{}
	for _, s := range doc.Streams {
		stream := push.Stream{Labels: labelsToString(s.Stream)}
		for _, v := range s.Values {
			if len(v) < 2 {
				return nil, fmt.Errorf("json push request: stream value has %d elements, want 2", len(v))
			}
			nanos, err := strconv.ParseInt(v[0], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("json push request: timestamp %q: %w", v[0], err)
			}
			stream.Entries = append(stream.Entries, push.Entry{
				Timestamp: time.Unix(0, nanos), Line: v[1],
			})
		}
		req.Streams = append(req.Streams, stream)
	}
	return req, nil
}

// handleLokiPush serves simulate.CapturePathLoki.
func (h *Harness) handleLokiPush(w http.ResponseWriter, r *http.Request) {
	req, err := decodeLokiPush(r.Header, r.Body)
	if err != nil {
		h.logger.Warn("capture: loki push decode failed", "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s := h.activeSink()
	if s == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	for _, stream := range req.Streams {
		labels := parseStreamLabels(stream.Labels)
		for _, e := range stream.Entries {
			s.addLogLine(LogLine{
				Labels: labels, Line: e.Line,
				TimestampMS: e.Timestamp.UnixMilli(),
			})
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseStreamLabels turns Loki's `{a="b", c="d"}` stream label string back into
// a map. Loki ships the label set as one pre-rendered string on the wire, so
// there is no structured form to read instead.
func parseStreamLabels(s string) map[string]string {
	out := map[string]string{}
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	for _, part := range splitTopLevel(s) {
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
		if name != "" {
			out[name] = value
		}
	}
	return out
}

// splitTopLevel splits on commas that are not inside a quoted value; a log
// path label such as {filename="/var/log/a,b.log"} would otherwise split in
// the middle of its value.
func splitTopLevel(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote, escaped := false, false
	for _, r := range s {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && inQuote:
			escaped = true
		case r == '"':
			inQuote = !inQuote
		case r == ',' && !inQuote:
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, cur.String())
	}
	return out
}

func labelsToString(labels map[string]string) string {
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, k+"="+strconv.Quote(v))
	}
	sort.Strings(parts)
	return "{" + strings.Join(parts, ", ") + "}"
}
