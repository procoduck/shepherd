package grafana

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestOutcomeZeroValueIsUnknown pins the structural half of the "three
// outcomes, not two" requirement: a zero-valued Outcome (an
// uninitialized field, a zero-valued VerificationResult{} literal, any
// code path that returns before assigning one) must read as "could not
// determine", never as a silent false-shaped OutcomeNotArrived.
//
// Red run: reorder the const block so OutcomeNotArrived is declared before
// OutcomeUnknown (giving OutcomeNotArrived the zero value). This test then
// fails: var o Outcome; o == OutcomeUnknown is false.
func TestOutcomeZeroValueIsUnknown(t *testing.T) {
	var o Outcome
	if o != OutcomeUnknown {
		t.Fatalf("zero-valued Outcome = %v, want OutcomeUnknown", o)
	}
	var r VerificationResult
	if r.Outcome != OutcomeUnknown {
		t.Fatalf("zero-valued VerificationResult.Outcome = %v, want OutcomeUnknown", r.Outcome)
	}
}

func TestOutcomeString(t *testing.T) {
	cases := map[Outcome]string{
		OutcomeUnknown:    "unknown",
		OutcomeArrived:    "arrived",
		OutcomeNotArrived: "not_arrived",
	}
	for o, want := range cases {
		if got := o.String(); got != want {
			t.Errorf("Outcome(%d).String() = %q, want %q", int(o), got, want)
		}
	}
}

func TestVerify_NilClientReturnsUnknown(t *testing.T) {
	result := Verify(context.Background(), nil, QueryDatasourceRequest{DatasourceUID: "ds1", From: "now-5m", To: "now"})
	if result.Outcome != OutcomeUnknown {
		t.Fatalf("Outcome = %v, want OutcomeUnknown for a nil client", result.Outcome)
	}
	if result.Reason == "" {
		t.Fatal("Reason is empty; a caller displaying OutcomeUnknown needs to know why")
	}
	if result.CheckedAt.IsZero() {
		t.Fatal("CheckedAt is zero; every result must record when it was produced")
	}
}

func TestVerify_ArrivedWhenDataPresent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeDSQueryResponse(w, map[string][][]any{"A": {{1000}, {1.0}}})
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "tok", time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result := Verify(context.Background(), c, QueryDatasourceRequest{DatasourceUID: "ds1", From: "now-5m", To: "now"})
	if result.Outcome != OutcomeArrived {
		t.Fatalf("Outcome = %v, want OutcomeArrived", result.Outcome)
	}
}

func TestVerify_NotArrivedWhenQuerySucceedsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeDSQueryResponse(w, map[string][][]any{"A": {{}}})
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "tok", time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result := Verify(context.Background(), c, QueryDatasourceRequest{DatasourceUID: "ds1", From: "now-5m", To: "now"})
	if result.Outcome != OutcomeNotArrived {
		t.Fatalf("Outcome = %v, want OutcomeNotArrived", result.Outcome)
	}
}

// TestVerify_UnknownWhenQueryFails is the load-bearing case the build
// brief names explicitly: "Collapsing the third into 'did not arrive'
// would report a broken Grafana as a broken pipeline." A 500 from Grafana
// itself (not "zero points", an actual failure to answer) must land on
// OutcomeUnknown, never OutcomeNotArrived.
//
// Red run: in Verify, change the `if err != nil { return unknown(...) }`
// branch to instead `return VerificationResult{Outcome: OutcomeNotArrived,
// ...}`. Observed failure: this test's Outcome check fails with "Outcome =
// not_arrived, want OutcomeUnknown" — exactly the misreport the brief
// warns against, now caught by a test instead of by a confused on-call
// engineer.
func TestVerify_UnknownWhenQueryFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "tok", time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result := Verify(context.Background(), c, QueryDatasourceRequest{DatasourceUID: "ds1", From: "now-5m", To: "now"})
	if result.Outcome != OutcomeUnknown {
		t.Fatalf("Outcome = %v, want OutcomeUnknown — a broken Grafana must never be reported as a broken pipeline", result.Outcome)
	}
}

func TestVerify_UnknownWhenGrafanaUnreachable(t *testing.T) {
	// A server that closes the listener immediately: connection refused,
	// not a timeout — the other network-failure shape Verify must also
	// fold into OutcomeUnknown.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // closed before any request is made

	c, err := NewClient(url, "tok", time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result := Verify(context.Background(), c, QueryDatasourceRequest{DatasourceUID: "ds1", From: "now-5m", To: "now"})
	if result.Outcome != OutcomeUnknown {
		t.Fatalf("Outcome = %v, want OutcomeUnknown for an unreachable Grafana", result.Outcome)
	}
}
