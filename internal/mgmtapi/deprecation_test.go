package mgmtapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every REST-shim response must carry the deprecation markers so an external
// integration still calling plain JSON is told, in-band, to move to Connect.
func TestDeprecationHeaders(t *testing.T) {
	var innerCalled bool
	h := deprecationHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/orgs/x/pipelines", nil))

	if !innerCalled {
		t.Fatal("middleware did not call the wrapped handler")
	}
	if got := rec.Header().Get("Deprecation"); got != "true" {
		t.Errorf("Deprecation = %q, want %q", got, "true")
	}
	if got := rec.Header().Get("Link"); !strings.Contains(got, deprecationSuccessor) ||
		!strings.Contains(got, `rel="successor-version"`) {
		t.Errorf("Link = %q, want a successor-version link to %q", got, deprecationSuccessor)
	}
	warning := rec.Header().Get("Warning")
	if !strings.HasPrefix(warning, "299 ") {
		t.Errorf("Warning = %q, want it to start with the 299 warn-code", warning)
	}
	if !strings.Contains(warning, "shepherd.mgmt.v1") {
		t.Errorf("Warning = %q, want it to name the Connect successor", warning)
	}
}
