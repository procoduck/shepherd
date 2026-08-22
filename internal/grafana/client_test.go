package grafana

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNewClient_RequiresToken(t *testing.T) {
	if _, err := NewClient("https://grafana.example.com", "", time.Second); err == nil {
		t.Fatal("NewClient with empty token succeeded; a Client that would send unauthenticated requests must be refused at construction")
	}
}

func TestNewClient_RequiresBaseURL(t *testing.T) {
	if _, err := NewClient("", "tok", time.Second); err == nil {
		t.Fatal("NewClient with empty baseURL succeeded")
	}
}

func TestNewClient_RejectsNonHTTPScheme(t *testing.T) {
	if _, err := NewClient("ftp://grafana.example.com", "tok", time.Second); err == nil {
		t.Fatal("NewClient accepted a non-http(s) scheme")
	}
}

func TestNewClient_DefaultsTimeout(t *testing.T) {
	c, err := NewClient("https://grafana.example.com", "tok", 0)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.timeout != DefaultTimeout {
		t.Fatalf("timeout = %v, want DefaultTimeout (%v) when caller passes 0", c.timeout, DefaultTimeout)
	}
}

// TestClientStringRedactsToken pins that Client's own Stringer, not the
// default reflection-based fmt formatting, is what a caller gets.
//
// Red run: delete Client's String and GoString methods. fmt then falls
// back to its default struct formatting for %v/%+v, which — contrary to
// the common assumption that unexported fields are "private" — DOES print
// an unexported field's actual value. Observed failure after deleting
// both methods: fmt.Sprintf("%+v", client) contains
// "token:super-secret-token", failing this test's Contains(...,
// "[REDACTED]") assertion (the literal token substring appears instead).
func TestClientStringRedactsToken(t *testing.T) {
	c, err := NewClient("https://grafana.example.com", "super-secret-token", time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	// BOTH the pointer and the dereferenced value. The receiver was a pointer
	// until review pointed out the seam: with a pointer receiver, fmt selects
	// the method only for *Client, and fmt.Sprintf("%+v", *client) — easy to
	// write by accident — printed the token via default struct formatting.
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		for name, val := range map[string]any{"pointer": c, "value": *c} {
			formatted := fmt.Sprintf(verb, val)
			if strings.Contains(formatted, "super-secret-token") {
				t.Fatalf("formatting a Client %s with %s contains the raw token: %q", name, verb, formatted)
			}
			if !strings.Contains(formatted, "REDACTED") {
				t.Fatalf("formatting a Client %s with %s does not mention REDACTED: %q", name, verb, formatted)
			}
		}
	}
}

func TestQueryDatasource_RequiresDatasourceUID(t *testing.T) {
	c, err := NewClient("https://grafana.example.com", "tok", time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.QueryDatasource(context.Background(), QueryDatasourceRequest{}); err == nil {
		t.Fatal("QueryDatasource with no DatasourceUID succeeded")
	}
}

func TestQueryDatasource_SendsAuthorizationBearerHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		writeDSQueryResponse(w, map[string][][]any{})
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "my-token", time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.QueryDatasource(context.Background(), QueryDatasourceRequest{DatasourceUID: "ds1", From: "now-5m", To: "now"}); err != nil {
		t.Fatalf("QueryDatasource: %v", err)
	}
	if gotAuth != "Bearer my-token" {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, "Bearer my-token")
	}
}

// writeDSQueryResponse writes a POST /api/ds/query response for refId "A"
// whose single frame's data.values is the given field arrays.
func writeDSQueryResponse(w http.ResponseWriter, values map[string][][]any) {
	fields, ok := values["A"]
	if !ok {
		fields = [][]any{}
	}
	resp := dsQueryResponse{Results: map[string]dsQueryResult{
		"A": {Status: 200, Frames: []dsQueryFrame{{Data: struct {
			Values []json.RawMessage `json:"values"`
		}{Values: rawValues(fields)}}}},
	}}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp) //nolint:errcheck // test helper writing to an httptest ResponseRecorder; encode failure would already fail the enclosing test via a bad response body
}

func rawValues(fields [][]any) []json.RawMessage {
	out := make([]json.RawMessage, len(fields))
	for i, f := range fields {
		b, err := json.Marshal(f)
		if err != nil {
			panic(err) // test fixture data is always marshalable; a failure here is a bug in the test itself
		}
		out[i] = b
	}
	return out
}

func TestQueryDatasource_HasDataTrueWhenPointsPresent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeDSQueryResponse(w, map[string][][]any{"A": {{1000, 2000}, {1.0, 2.0}}})
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "tok", time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	hasData, err := c.QueryDatasource(context.Background(), QueryDatasourceRequest{DatasourceUID: "ds1", From: "now-5m", To: "now"})
	if err != nil {
		t.Fatalf("QueryDatasource: %v", err)
	}
	if !hasData {
		t.Fatal("hasData = false, want true: the response frame carried two data points")
	}
}

func TestQueryDatasource_HasDataFalseWhenEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeDSQueryResponse(w, map[string][][]any{"A": {{}, {}}})
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "tok", time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	hasData, err := c.QueryDatasource(context.Background(), QueryDatasourceRequest{DatasourceUID: "ds1", From: "now-5m", To: "now"})
	if err != nil {
		t.Fatalf("QueryDatasource: %v", err)
	}
	if hasData {
		t.Fatal("hasData = true, want false: the response frame carried zero data points in every field")
	}
}

func TestQueryDatasource_ReturnsErrorWhenQueryResultHasError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := dsQueryResponse{Results: map[string]dsQueryResult{
			"A": {Status: 400, Error: "parse error: unexpected token"},
		}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp) //nolint:errcheck // test helper writing to an httptest ResponseRecorder; encode failure would already fail the enclosing test via a bad response body
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "tok", time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.QueryDatasource(context.Background(), QueryDatasourceRequest{DatasourceUID: "ds1", From: "now-5m", To: "now"}); err == nil {
		t.Fatal("QueryDatasource succeeded despite results[refId].error being set")
	} else if !strings.Contains(err.Error(), "parse error") {
		t.Fatalf("error %q does not surface the datasource's own error message", err.Error())
	}
}

func TestQueryDatasource_ReturnsErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("invalid API token")) //nolint:errcheck // test helper writing to an httptest ResponseRecorder
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "bad-tok", time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.QueryDatasource(context.Background(), QueryDatasourceRequest{DatasourceUID: "ds1", From: "now-5m", To: "now"}); err == nil {
		t.Fatal("QueryDatasource succeeded against a 401 response")
	} else if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error %q does not name the observed status code", err.Error())
	}
}

// TestClientBoundsSlowServer_EvenWithNoCallerDeadline is D7's "a slow or
// broken Grafana must degrade to unknown, never hang a request" property,
// exercised directly at the Client level with a context that has NO
// deadline of its own — the exact case a caller forgetting to set one
// produces, and the case Client must not rely on the caller to avoid.
//
// Red run: in do(), replace
//
//	ctx, cancel := context.WithTimeout(ctx, c.timeout)
//	defer cancel()
//
// with nothing (use the incoming ctx directly). Server sleeps 300ms;
// client timeout is 40ms. Observed failure after the revert: elapsed ==
// ~300ms and err == nil (the request succeeds, just late), failing this
// test's `elapsed > 200*time.Millisecond` assertion — proving the 40ms
// bound was doing nothing and the call was actually gated only by the
// server's own behavior, i.e. unbounded from Client's side.
func TestClientBoundsSlowServer_EvenWithNoCallerDeadline(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-time.After(2 * time.Second):
		}
		writeDSQueryResponse(w, map[string][][]any{})
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	c, err := NewClient(srv.URL, "tok", 40*time.Millisecond)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	start := time.Now()
	// Deliberately context.Background(): no deadline of the caller's own,
	// which is exactly the case Client must bound on its own.
	_, err = c.QueryDatasource(context.Background(), QueryDatasourceRequest{DatasourceUID: "ds1", From: "now-5m", To: "now"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("QueryDatasource against a server that never responds within the client timeout returned no error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v does not wrap context.DeadlineExceeded", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("QueryDatasource took %v, want well under the server's 2s hang — the client timeout (40ms) did not bound the call", elapsed)
	}
}

// TestListDatasources_NeverDecodesIntoAFieldASecretCouldOccupy is the
// structural half of "destination import ... Grafana will not hand back
// datasource secrets": Datasource has exactly UID/Name/Type/URL/IsDefault,
// verified by reflection so a future change that adds e.g. a Password
// field to Datasource fails this test immediately, the same discipline
// internal/beacon.ComponentObservation's structural test applies to raw
// samples.
func TestListDatasources_NeverDecodesIntoAFieldASecretCouldOccupy(t *testing.T) {
	typ := reflect.TypeOf(Datasource{})
	want := map[string]bool{"UID": true, "Name": true, "Type": true, "URL": true, "IsDefault": true}
	if typ.NumField() != len(want) {
		t.Fatalf("Datasource has %d fields, want exactly %d (%v) — a field was added or removed", typ.NumField(), len(want), want)
	}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if !want[name] {
			t.Fatalf("Datasource has unexpected field %q — datasource secrets must never have a field to decode into", name)
		}
	}
}

func TestListDatasources_DecodesUIDNameTypeURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/datasources" {
			t.Errorf("request path = %q, want /api/datasources", r.URL.Path)
		}
		body := []byte(`[
			{"id":1,"uid":"P1","name":"Prometheus","type":"prometheus","url":"http://prom:9090","isDefault":true,"password":"should-not-decode","basicAuthPassword":"also-should-not-decode"},
			{"id":2,"uid":"L1","name":"Loki","type":"loki","url":"http://loki:3100","isDefault":false}
		]`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body) //nolint:errcheck // test helper writing to an httptest ResponseRecorder
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "tok", time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	got, err := c.ListDatasources(context.Background())
	if err != nil {
		t.Fatalf("ListDatasources: %v", err)
	}
	want := []Datasource{
		{UID: "P1", Name: "Prometheus", Type: "prometheus", URL: "http://prom:9090", IsDefault: true},
		{UID: "L1", Name: "Loki", Type: "loki", URL: "http://loki:3100", IsDefault: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListDatasources = %+v, want %+v", got, want)
	}
}
