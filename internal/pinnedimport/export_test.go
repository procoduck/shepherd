package pinnedimport

import (
	"context"
	"time"

	"shepherd/internal/gitrepo"
)

// Fetcher is the git side a test replaces.
type Fetcher interface {
	FileAtCommit(ctx context.Context, repo gitrepo.Repo, commit, path string) ([]byte, error)
}

// WithFetcher returns r using f for git, and budget for a save's fetches.
func (r *Resolver) WithFetcher(f Fetcher, budget time.Duration) *Resolver {
	c := *r
	c.git = f
	if budget > 0 {
		c.budget = budget
	}
	return &c
}

// SetEvictionGrace replaces how recently used a module may be and still not
// be evicted.
func SetEvictionGrace(d time.Duration) { evictionGrace = d }
