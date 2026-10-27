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
