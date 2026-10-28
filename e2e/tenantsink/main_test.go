package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang/snappy"
	"github.com/grafana/loki/pkg/push"
	"github.com/prometheus/prometheus/prompb"
)

// The sink decodes by hand what loki.write and prometheus.remote_write send;
// these check that against the real encoders.
func TestRecordsLokiPushTenantAndLabels(t *testing.T) {
	req := push.PushRequest{Streams: []push.Stream{
		{Labels: `{case="a", filename="/etc/alloy/strict.alloy"}`, Entries: []push.Entry{{Timestamp: time.Unix(1, 0), Line: strings.Repeat("x", 300)}}},
		{Labels: `{case="b \"q\"", __tenant_id__="mine"}`, Entries: []push.Entry{{Timestamp: time.Unix(2, 0), Line: "y"}}},
	}}
	raw, err := req.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got := post(t, "/loki/api/v1/push", "mine", snappy.Encode(nil, raw))
	want := []map[string]string{
		{"case": "a", "filename": "/etc/alloy/strict.alloy"},
		{"case": `b "q"`, "__tenant_id__": "mine"},
	}
	if got.Tenant != "mine" || got.Error != "" || !reflect.DeepEqual(got.Labels, want) {
		t.Errorf("got %+v", got)
	}
}

func TestRecordsRemoteWriteLabels(t *testing.T) {
	req := prompb.WriteRequest{Timeseries: []prompb.TimeSeries{{
		Labels:  []prompb.Label{{Name: "__name__", Value: "up"}, {Name: "case", Value: "m"}},
		Samples: []prompb.Sample{{Value: 1, Timestamp: 1}},
	}}}
	raw, err := req.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got := post(t, "/api/v1/push", "", snappy.Encode(nil, raw))
	if got.Tenant != "" || got.Error != "" || !reflect.DeepEqual(got.Labels, []map[string]string{{"__name__": "up", "case": "m"}}) {
		t.Errorf("got %+v", got)
	}
}

func post(t *testing.T, path, tenant string, body []byte) Record {
	t.Helper()
	s := &sink{}
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	if tenant != "" {
		r.Header.Set("X-Scope-OrgID", tenant)
	}
	w := httptest.NewRecorder()
	s.push(w, r)
	if w.Code != http.StatusNoContent || len(s.records) != 1 {
		t.Fatalf("status %d, %d records", w.Code, len(s.records))
	}
	return s.records[0]
}

// The sink also plays the tenant-route gateway (internal/gateway
// RenderHTTPRoute): a push under /{kind}/{segment} of a route registered
// with it is recorded under the route's tenant, whatever header it carried,
// with the prefix stripped before decoding; one under an unknown route is
// not found.
func TestPlaysTheGatewayForRegisteredRoutes(t *testing.T) {
	s := &sink{}
	reg := httptest.NewRecorder()
	s.addRoute(reg, httptest.NewRequest(http.MethodPost, "/routes",
		strings.NewReader(`{"kind":"otlp","segment":"theirs-1a2b3c4d","tenant":"theirs"}`)))
	if reg.Code != http.StatusNoContent {
		t.Fatalf("registering a route: %d %s", reg.Code, reg.Body)
	}
	req := push.PushRequest{Streams: []push.Stream{
		{Labels: `{case="g"}`, Entries: []push.Entry{{Timestamp: time.Unix(1, 0), Line: "x"}}},
	}}
	raw, err := req.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/otlp/theirs-1a2b3c4d/loki/api/v1/push", bytes.NewReader(snappy.Encode(nil, raw)))
	r.Header.Set("X-Scope-OrgID", "mine")
	w := httptest.NewRecorder()
	s.push(w, r)
	if w.Code != http.StatusNoContent || len(s.records) != 1 {
		t.Fatalf("status %d, %d records", w.Code, len(s.records))
	}
	got := s.records[0]
	if got.Tenant != "theirs" || got.Path != "/otlp/theirs-1a2b3c4d/loki/api/v1/push" || got.Error != "" ||
		!reflect.DeepEqual(got.Labels, []map[string]string{{"case": "g"}}) {
		t.Errorf("got %+v", got)
	}
	w = httptest.NewRecorder()
	s.push(w, httptest.NewRequest(http.MethodPost, "/otlp/unknown-0000/loki/api/v1/push", bytes.NewReader(nil)))
	if w.Code != http.StatusNotFound || len(s.records) != 1 {
		t.Errorf("an unknown route: status %d, %d records", w.Code, len(s.records))
	}
}
