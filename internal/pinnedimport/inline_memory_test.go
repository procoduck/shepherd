package pinnedimport

import (
	"context"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/alloysyntax"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
)

// cloningModules hands out a fresh copy of a module's text on every read,
// as the database does, so a text the walker keeps shows in the heap.
type cloningModules struct{ fakeModules }

func (c cloningModules) GetPinnedImportModule(ctx context.Context, a sqlc.GetPinnedImportModuleParams) (sqlc.PinnedImportModule, error) {
	r, err := c.fakeModules.GetPinnedImportModule(ctx, a)
	r.Content = strings.Clone(r.Content)
	return r, err
}

// peakHeap is how far the live heap rose above where it was while fn ran,
// sampled every millisecond.
func peakHeap(fn func()) uint64 {
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak atomic.Uint64
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > base.HeapAlloc && m.HeapAlloc-base.HeapAlloc > peak.Load() {
				peak.Store(m.HeapAlloc - base.HeapAlloc)
			}
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	fn()
	close(stop)
	<-done
	return peak.Load()
}

// One module reached at many depths is read once per place, and held only
// while its level is walked or built (#450 review): 16 cached 1 MiB modules,
// each imported at every level of a 7-deep import.string chain — 136
// instances in a 62 KiB pipeline — once held a copy per place (about
// 200 MiB) before the build refused it. Also with the modules' bulk inside
// their own import.git blocks, which the served form drops, so no lower
// bound on the served form stops the walk early.
func TestInlineHoldsEachModuleBriefly(t *testing.T) {
	repos := budgetRepos(t)
	for _, tc := range []struct {
		name   string
		chain  int
		module func(i int) string
		serve  bool
	}{
		{"1 MiB modules", 7, func(i int) string {
			return "// " + strconv.Itoa(i) + strings.Repeat("a", 1<<20-20) + "\n"
		}, false},
		{"1 MiB inside each module's import.git", 6, func(i int) string {
			return strings.Replace(importGit("", "leaf", "leaf.alloy"), "}\n",
				"  // "+strconv.Itoa(i)+strings.Repeat("a", 1<<20-300)+"\n}\n", 1)
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mods := fakeModules{"leaf.alloy": "x = 1\n"}
			var level strings.Builder
			for i := range 16 {
				p := "m" + strconv.Itoa(i) + ".alloy"
				mods[p] = tc.module(i)
				level.WriteString(importGit("", "g"+strconv.Itoa(i), p))
			}
			text := level.String()
			for range tc.chain {
				text = level.String() + "import.string \"s\" {\n  content = " + alloysyntax.QuoteString(text) + "\n}\n"
			}
			var err error
			peak := peakHeap(func() {
				_, err = inline(context.Background(), cloningModules{mods}, pgtype.UUID{}, repos, text)
			})
			t.Logf("%d-byte pipeline: peak heap +%.1f MiB, %v", len(text), mib(peak), err)
			if (err == nil) != tc.serve {
				t.Fatalf("served: %v, want %v", err == nil, tc.serve)
			}
			if peak > 32*validate.MaxContentBytes {
				t.Fatalf("peak heap +%.1f MiB, more than 32 times the cap", mib(peak))
			}
		})
	}
}
