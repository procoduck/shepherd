package k8s_test

// No build tag on purpose: the helpers here pick the chart version the
// upgrade spec (helm_upgrade_test.go) starts from, and that choice is pure
// logic worth checking on every `go test ./...` without a cluster.
// TestPreviousStableChart runs there; inside the kind suite it runs too, which
// costs nothing.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// chartSemver is a chart version: MAJOR.MINOR.PATCH with an optional
// prerelease (Chart.yaml carries -rc.N between releases). Build metadata is
// accepted and ignored, as semver orders it.
type chartSemver struct {
	major, minor, patch int
	pre                 []string
	raw                 string
}

var chartSemverRE = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)

func parseChartSemver(s string) (chartSemver, error) {
	m := chartSemverRE.FindStringSubmatch(s)
	if m == nil {
		return chartSemver{}, fmt.Errorf("%q is not a semantic version", s)
	}
	v := chartSemver{raw: s}
	for i, p := range []*int{&v.major, &v.minor, &v.patch} {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return chartSemver{}, fmt.Errorf("%q: %w", s, err)
		}
		*p = n
	}
	if m[4] != "" {
		v.pre = strings.Split(m[4], ".")
	}
	return v, nil
}

// compare orders a and b by semver 2.0 precedence: a prerelease sorts before
// its release, and prerelease identifiers compare numerically when both are
// numbers, lexically otherwise, a numeric one before an alphanumeric one.
func (a chartSemver) compare(b chartSemver) int {
	for _, d := range []int{a.major - b.major, a.minor - b.minor, a.patch - b.patch} {
		if d != 0 {
			return sign(d)
		}
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		x, xErr := strconv.Atoi(a.pre[i])
		y, yErr := strconv.Atoi(b.pre[i])
		switch {
		case xErr == nil && yErr == nil:
			if x != y {
				return sign(x - y)
			}
		case xErr == nil:
			return -1
		case yErr == nil:
			return 1
		default:
			if c := strings.Compare(a.pre[i], b.pre[i]); c != 0 {
				return c
			}
		}
	}
	return sign(len(a.pre) - len(b.pre))
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// previousStableChart is the version the upgrade spec installs first: the
// highest published release (no prerelease) that sorts below current. For a
// current of 0.25.0-rc.1 that is 0.24.x, not 0.25.0-rc.0 and not 0.25.0;
// tags that are not versions (an OCI registry may hold others) are ignored.
func previousStableChart(tags []string, current string) (string, error) {
	cur, err := parseChartSemver(current)
	if err != nil {
		return "", fmt.Errorf("the tree's chart version: %w", err)
	}
	var best *chartSemver
	for _, tag := range tags {
		v, err := parseChartSemver(tag)
		if err != nil || len(v.pre) > 0 || v.compare(cur) >= 0 {
			continue
		}
		if best == nil || v.compare(*best) > 0 {
			best = &v
		}
	}
	if best == nil {
		return "", fmt.Errorf("no published release sorts below %s among %d tags", current, len(tags))
	}
	return best.raw, nil
}

// treeChartVersion reads deploy/helm/shepherd/Chart.yaml's version.
func treeChartVersion(chartDir string) (string, error) {
	body, err := os.ReadFile(filepath.Join(chartDir, "Chart.yaml"))
	if err != nil {
		return "", fmt.Errorf("reading Chart.yaml: %w", err)
	}
	var chart struct {
		Version string `yaml:"version"`
	}
	if err := yaml.Unmarshal(body, &chart); err != nil {
		return "", fmt.Errorf("parsing Chart.yaml: %w", err)
	}
	if chart.Version == "" {
		return "", errors.New("Chart.yaml has no version")
	}
	return chart.Version, nil
}

var upMigrationRE = regexp.MustCompile(`^(\d+)_.*\.up\.sql$`)

// latestTreeMigration is the highest migration number in the tree, the
// version schema_migrations must hold once the tree's chart has run its
// migrate hook (the same derivation /readyz uses from the embedded files).
func latestTreeMigration(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", dir, err)
	}
	latest := 0
	for _, e := range entries {
		m := upMigrationRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		latest = max(latest, n)
	}
	if latest == 0 {
		return 0, fmt.Errorf("no up migrations in %s", dir)
	}
	return latest, nil
}

func TestPreviousStableChart(t *testing.T) {
	published := []string{
		"0.22.0", "0.23.0-rc.1", "0.23.0", "0.23.1", "0.24.0", "0.25.0-rc.1", "sha256-abc.sig", "latest",
	}
	for _, tc := range []struct {
		current, want string
	}{
		{"0.25.0-rc.1", "0.24.0"}, // a prerelease in the tree upgrades from the last release
		{"0.25.0-rc.2", "0.24.0"}, // never from an earlier rc of itself
		{"0.25.0", "0.24.0"},      // the release itself, not its own rc
		{"0.24.0", "0.23.1"},      // a re-run of a released tree: the one before it
		{"0.23.1", "0.23.0"},      // patch below patch
		{"0.30.0", "0.24.0"},      // a gap: still the highest below
		{"1.0.0-rc.1", "0.24.0"},
	} {
		got, err := previousStableChart(published, tc.current)
		if err != nil || got != tc.want {
			t.Errorf("previousStableChart(%q) = %q, %v; want %q", tc.current, got, err, tc.want)
		}
	}
	if got, err := previousStableChart(published, "0.22.0"); err == nil {
		t.Errorf("below the first release: got %q, want an error", got)
	}
	if _, err := previousStableChart(published, "not-a-version"); err == nil {
		t.Error("an unparseable current version must be an error")
	}
}

func TestChartSemverOrdering(t *testing.T) {
	ordered := []string{"0.9.0", "0.10.0-alpha", "0.10.0-alpha.1", "0.10.0-alpha.beta", "0.10.0-beta.2", "0.10.0-beta.11", "0.10.0-rc.1", "0.10.0", "0.10.1", "1.0.0"}
	for i := 0; i+1 < len(ordered); i++ {
		a, errA := parseChartSemver(ordered[i])
		b, errB := parseChartSemver(ordered[i+1])
		if errA != nil || errB != nil {
			t.Fatalf("parsing %q/%q: %v %v", ordered[i], ordered[i+1], errA, errB)
		}
		if a.compare(b) >= 0 || b.compare(a) <= 0 {
			t.Errorf("%s must sort before %s", ordered[i], ordered[i+1])
		}
	}
}

func TestTreeVersionsAreReadable(t *testing.T) {
	v, err := treeChartVersion("../../deploy/helm/shepherd")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseChartSemver(v); err != nil {
		t.Fatalf("Chart.yaml version: %v", err)
	}
	if n, err := latestTreeMigration("../../internal/migrations/sql"); err != nil || n < 1 {
		t.Fatalf("latest migration: %d, %v", n, err)
	}
}
