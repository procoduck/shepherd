package grafana

import (
	"encoding/json"
	"net/url"
	"testing"
)

// TestExploreURLSignatureTakesNoToken is a compile-time property, not a
// runtime assertion: ExploreURL's signature (baseURL, datasourceUID,
// query, from, to) has no token parameter at all, which is D7's "deep
// links into Explore which need no token at all" made structural — there
// is no argument position a token could even be passed into. This test
// exists so that claim has a named, discoverable anchor; if a future
// change adds a token parameter, this test's call site becomes a compile
// error, not a silently-passing runtime check.
func TestExploreURLSignatureTakesNoToken(t *testing.T) {
	if _, err := ExploreURL("https://grafana.example.com", "ds1", map[string]any{"expr": "up"}, "now-5m", "now"); err != nil {
		t.Fatalf("ExploreURL: %v", err)
	}
}

func TestExploreURL_RequiresBaseURL(t *testing.T) {
	if _, err := ExploreURL("", "ds1", nil, "now-5m", "now"); err == nil {
		t.Fatal("ExploreURL with empty baseURL succeeded")
	}
}

func TestExploreURL_RejectsNonHTTPScheme(t *testing.T) {
	if _, err := ExploreURL("ftp://grafana.example.com", "ds1", nil, "now-5m", "now"); err == nil {
		t.Fatal("ExploreURL accepted a non-http(s) scheme")
	}
}

func TestExploreURL_RequiresDatasourceUID(t *testing.T) {
	if _, err := ExploreURL("https://grafana.example.com", "", nil, "now-5m", "now"); err == nil {
		t.Fatal("ExploreURL with empty datasourceUID succeeded")
	}
}

func TestExploreURL_RequiresFromAndTo(t *testing.T) {
	if _, err := ExploreURL("https://grafana.example.com", "ds1", nil, "", "now"); err == nil {
		t.Fatal("ExploreURL with empty from succeeded")
	}
	if _, err := ExploreURL("https://grafana.example.com", "ds1", nil, "now-5m", ""); err == nil {
		t.Fatal("ExploreURL with empty to succeeded")
	}
}

func TestExploreURL_EncodesDatasourceAndQueryAndRange(t *testing.T) {
	raw, err := ExploreURL("https://grafana.example.com/", "P1", map[string]any{"expr": "up{job=\"myapp\"}"}, "now-5m", "now")
	if err != nil {
		t.Fatalf("ExploreURL: %v", err)
	}

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("ExploreURL returned an unparseable URL: %v", err)
	}
	if u.Path != "/explore" {
		t.Fatalf("path = %q, want /explore", u.Path)
	}
	if got := u.Query().Get("schemaVersion"); got != "1" {
		t.Fatalf("schemaVersion = %q, want \"1\"", got)
	}

	var panes map[string]explorePane
	if err := json.Unmarshal([]byte(u.Query().Get("panes")), &panes); err != nil {
		t.Fatalf("panes did not decode as the pane schema: %v", err)
	}
	if len(panes) != 1 {
		t.Fatalf("panes has %d entries, want exactly 1", len(panes))
	}
	pane, ok := panes[explorePaneID]
	if !ok {
		t.Fatalf("panes has no entry for key %q; got keys %v", explorePaneID, keysOf(panes))
	}
	if pane.Datasource != "P1" {
		t.Errorf("pane.Datasource = %q, want %q", pane.Datasource, "P1")
	}
	if pane.Range.From != "now-5m" || pane.Range.To != "now" {
		t.Errorf("pane.Range = %+v, want {now-5m now}", pane.Range)
	}
	if len(pane.Queries) != 1 {
		t.Fatalf("pane.Queries has %d entries, want exactly 1", len(pane.Queries))
	}
	if pane.Queries[0].Datasource.UID != "P1" {
		t.Errorf("pane.Queries[0].Datasource.UID = %q, want %q", pane.Queries[0].Datasource.UID, "P1")
	}
}

func keysOf(m map[string]explorePane) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestExploreURL_TrimsTrailingSlashFromBaseURL guards against a doubled
// slash ("https://host//explore") that some Grafana ingress/reverse-proxy
// setups treat as a distinct, non-matching path.
func TestExploreURL_TrimsTrailingSlashFromBaseURL(t *testing.T) {
	raw, err := ExploreURL("https://grafana.example.com/", "P1", nil, "now-5m", "now")
	if err != nil {
		t.Fatalf("ExploreURL: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("ExploreURL returned an unparseable URL: %v", err)
	}
	if u.Path != "/explore" {
		t.Fatalf("path = %q, want /explore (no doubled slash)", u.Path)
	}
}
