package pipelinesource_test

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"shepherd/internal/pipelinesource"
)

func TestParseAcceptsEveryValueAndNothingElse(t *testing.T) {
	for _, s := range pipelinesource.All() {
		got, err := pipelinesource.Parse(string(s))
		if err != nil || got != s {
			t.Fatalf("Parse(%q) = %q, %v; want %q, nil", s, got, err, s)
		}
	}
	for _, bad := range []string{"", "UI", "api", "gitops", " git"} {
		if _, err := pipelinesource.Parse(bad); !errors.Is(err, pipelinesource.ErrUnknown) {
			t.Fatalf("Parse(%q) error = %v; want ErrUnknown", bad, err)
		}
	}
}

func TestStringIsTheStoredValue(t *testing.T) {
	want := map[pipelinesource.Source]string{
		pipelinesource.UI: "ui", pipelinesource.Wizard: "wizard",
		pipelinesource.Git: "git", pipelinesource.Visual: "visual",
	}
	if len(want) != len(pipelinesource.All()) {
		t.Fatalf("All() has %d values, the table %d", len(pipelinesource.All()), len(want))
	}
	for s, v := range want {
		if s.String() != v {
			t.Fatalf("%v.String() = %q, want %q", s, s.String(), v)
		}
	}
}

// The constants and the column's CHECK constraint are two lists of the same
// thing. The newest migration that defines the constraint is the one in force
// (0001 declares it inline, later ones replace pipelines_source_check); its IN
// list must equal All().
func TestAllMatchesTheCheckConstraint(t *testing.T) {
	files, err := filepath.Glob("../migrations/sql/*.up.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("listing migrations: %v (%d files)", err, len(files))
	}
	sort.Strings(files)
	check := regexp.MustCompile(`(?s)CHECK\s*\(\s*source\s+IN\s*\(([^)]*)\)\s*\)`)
	var list, from string
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.Contains(text, "pipelines_source_check") && !strings.Contains(text, "CREATE TABLE pipelines") {
			continue
		}
		if m := check.FindStringSubmatch(text); m != nil {
			list, from = m[1], filepath.Base(f)
		}
	}
	if list == "" {
		t.Fatal("no migration defines the pipelines.source CHECK constraint")
	}
	var inDB []string
	for _, v := range strings.Split(list, ",") {
		inDB = append(inDB, strings.Trim(strings.TrimSpace(v), "'"))
	}
	var inGo []string
	for _, s := range pipelinesource.All() {
		inGo = append(inGo, string(s))
	}
	sort.Strings(inDB)
	sort.Strings(inGo)
	if !slices.Equal(inDB, inGo) {
		t.Fatalf("pipelines_source_check (%s) allows %v; pipelinesource.All() is %v", from, inDB, inGo)
	}
}
