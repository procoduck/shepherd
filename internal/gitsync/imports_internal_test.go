package gitsync

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"shepherd/internal/config"
	"shepherd/internal/pinnedimport"
)

// One git sync pass shares one pinned-module fetch budget across every file
// of the commit (pinnedimport.SaveBudget once, not once per file): planFile
// reads the pass's Source from the context syncLink set it in.
func TestSyncPassSharesOneImportSource(t *testing.T) {
	r := &Reconciler{imports: pinnedimport.New(&config.Config{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))}
	ctx := r.withPassImports(context.Background())
	a, b := r.passImports(ctx), r.passImports(ctx)
	if a != b {
		t.Fatal("two files of one pass got different import sources, so each would get its own budget")
	}
	if r.passImports(r.withPassImports(context.Background())) == a {
		t.Fatal("a new pass must get a new budget")
	}
}
