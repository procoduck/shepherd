package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"shepherd/internal/version"
)

// The build-info series must be present on the first scrape (published in
// init) with a constant value of 1 and the version/commit carried as labels —
// the shape a dashboard joins the running version onto other series by.
func TestBuildInfoPublished(t *testing.T) {
	got := testutil.ToFloat64(BuildInfo.WithLabelValues(version.Version, version.Commit))
	if got != 1 {
		t.Fatalf("shepherd_build_info{version=%q,commit=%q} = %v, want 1", version.Version, version.Commit, got)
	}

	expected := `
# HELP shepherd_build_info Build information; constant 1, the version and commit labels carry the values.
# TYPE shepherd_build_info gauge
shepherd_build_info{commit="` + version.Commit + `",version="` + version.Version + `"} 1
`
	if err := testutil.CollectAndCompare(BuildInfo, strings.NewReader(expected), "shepherd_build_info"); err != nil {
		t.Fatalf("unexpected build_info exposition: %v", err)
	}
}
