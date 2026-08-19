// Package gitrepo speaks the git wire protocol over HTTPS (SSH lands in a
// later step) to read *.alloy files out of one branch of a remote
// repository. It never shells out to a git binary — transport, protocol,
// and object decoding are all pure Go via github.com/go-git/go-git/v6, so
// the distroless production image needs no git binary at all.
//
// Change detection is a ls-remote (LatestCommit): a single cheap round
// trip that resolves a branch tip without fetching any objects. Reading
// config is a shallow, single-branch, in-memory clone (Files): repos this
// package targets are small, so nothing ever touches disk.
//
// Every operation builds its own credentials and TLS transport — see
// clientOptions in transport.go — so multiple Repo values with different
// auth or trust settings never interfere with each other.
package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"

	billy "github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/util"
	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/storage/memory"
)

// Repo describes one git repository to read *.alloy files out of.
type Repo struct {
	// URL is the clone URL, e.g. https://gitea.example.com/team/configs.git.
	URL string
	// Branch is the branch to track. Required.
	Branch string
	// Path is the subdirectory (relative to the repo root) that Files
	// searches for *.alloy files. Empty (or ".") means the whole repo.
	Path string
	// Auth supplies credentials for URL. Nil is equivalent to NoneAuth{}
	// (anonymous).
	Auth Auth
	// TLS configures certificate verification for an https:// URL. The
	// zero value verifies against the system trust store only.
	TLS TLSOptions
	// Limits bounds the resources one call may consume. The zero value
	// is filled in with DefaultLimits().
	Limits Limits
}

// File is one *.alloy file read out of a repository.
type File struct {
	// Path is the file's path relative to the repository root (not
	// relative to Repo.Path).
	Path string
	// Content is the raw file content.
	Content []byte
}

// Sentinel errors returned by Repo methods. Authentication and
// authorization failures are not wrapped in a package sentinel here —
// they already satisfy errors.Is against
// github.com/go-git/go-git/v6/plumbing/transport.ErrAuthenticationRequired
// and .ErrAuthorizationFailed, which is a clearer signal than anything
// this package could add.
var (
	// ErrBranchNotFound is returned by LatestCommit when Repo.Branch does
	// not exist on the remote.
	ErrBranchNotFound = errors.New("gitrepo: branch not found")
	// ErrTooManyFiles is returned by Files when more than
	// Limits.MaxFiles matching files are found under Repo.Path.
	ErrTooManyFiles = errors.New("gitrepo: too many .alloy files")
	// ErrFetchTimeout is returned when an operation exceeds
	// Limits.FetchTimeout.
	ErrFetchTimeout = errors.New("gitrepo: fetch timed out")
)

func (r Repo) branchRef() plumbing.ReferenceName {
	return plumbing.NewBranchReferenceName(r.Branch)
}

// LatestCommit resolves the tip of Repo.Branch without fetching any
// objects (git ls-remote), so it is cheap enough to call on every poll.
func (r Repo) LatestCommit(ctx context.Context) (string, error) {
	limits := r.Limits.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, limits.FetchTimeout)
	defer cancel()

	counter := newByteCounter(limits.MaxRepoBytes)
	opts, err := r.clientOptions(ctx, counter)
	if err != nil {
		return "", err
	}

	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: "origin",
		URLs: []string{r.URL},
	})

	refs, err := remote.ListContext(ctx, &git.ListOptions{ClientOptions: opts})
	if err != nil {
		return "", r.wrapFetchErr("ls-remote", err, counter, limits)
	}
	// The byte limit is checked here even though ListContext reported no
	// error: a response small enough to arrive in one Read can carry a
	// complete, self-delimited ref advertisement, so go-git's parser may
	// never look at the trailing error the counting reader attaches to
	// that Read once the limit trips (see the countingTransport doc
	// comment in limits.go). Checking the counter directly is the only
	// enforcement that can't be silently bypassed this way.
	if counter.exceeded() {
		return "", fmt.Errorf("%w: ls-remote of %s", ErrRepoTooLarge, r.URL)
	}

	want := r.branchRef()
	for _, ref := range refs {
		if ref.Name() == want {
			return ref.Hash().String(), nil
		}
	}
	return "", fmt.Errorf("%w: %q on %s", ErrBranchNotFound, r.Branch, r.URL)
}

// Files shallow-clones Repo.Branch (depth 1, single branch, in-memory)
// and returns every *.alloy file found under Repo.Path.
//
// A Path that does not exist in the repository is not an error: it is
// treated the same as an existing, empty directory, and Files returns no
// files. This keeps "no config committed yet" indistinguishable from
// "nothing new to sync" from the caller's point of view.
func (r Repo) Files(ctx context.Context) ([]File, error) {
	limits := r.Limits.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, limits.FetchTimeout)
	defer cancel()

	counter := newByteCounter(limits.MaxRepoBytes)
	opts, err := r.clientOptions(ctx, counter)
	if err != nil {
		return nil, err
	}

	wt := memfs.New()
	if _, err := git.CloneContext(ctx, memory.NewStorage(), wt, &git.CloneOptions{
		URL:           r.URL,
		ReferenceName: r.branchRef(),
		SingleBranch:  true,
		Depth:         1,
		ClientOptions: opts,
	}); err != nil {
		return nil, r.wrapFetchErr("clone", err, counter, limits)
	}
	// See the matching check in LatestCommit: the counter is the
	// authoritative signal, independent of whether CloneContext itself
	// reported an error.
	if counter.exceeded() {
		return nil, fmt.Errorf("%w: clone of %s", ErrRepoTooLarge, r.URL)
	}

	root := "."
	if r.Path != "" && r.Path != "." {
		root = filepath.Clean(r.Path)
	}

	var files []File
	walkErr := util.Walk(wt, root, func(walkPath string, info fs.FileInfo, walkErr error) error {
		if walkErr != nil {
			if walkPath == root {
				// Repo.Path doesn't exist in this branch: treat as empty,
				// not an error (see the doc comment above).
				return nil
			}
			return fmt.Errorf("gitrepo: walking %s: %w", walkPath, walkErr)
		}
		if info.IsDir() || !strings.HasSuffix(walkPath, ".alloy") {
			return nil
		}

		if len(files) >= limits.MaxFiles {
			return fmt.Errorf("%w: more than %d files under %q in %s", ErrTooManyFiles, limits.MaxFiles, r.Path, r.URL)
		}

		content, skipped, err := readLimited(wt, walkPath, info.Size(), limits.MaxFileBytes)
		if err != nil {
			return fmt.Errorf("gitrepo: reading %s: %w", walkPath, err)
		}
		if skipped {
			slog.WarnContext(ctx, "gitrepo: skipping oversized file",
				"path", walkPath, "size", info.Size(), "max_file_bytes", limits.MaxFileBytes, "url", r.URL)
			return nil
		}

		files = append(files, File{Path: walkPath, Content: content})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	return files, nil
}

// readLimited reads filename in full unless it is larger than max, in
// which case it reports skipped=true without reading the whole file into
// memory.
func readLimited(wt billy.Filesystem, filename string, size int64, maxBytes int64) (content []byte, skipped bool, err error) {
	if size > maxBytes {
		return nil, true, nil
	}

	f, err := wt.Open(filename)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing %s: %w", filename, cerr)
		}
	}()

	// Read one byte past maxBytes: if info.Size() lied (e.g. a symlink or
	// a filesystem that doesn't report accurate sizes), this still trips
	// the limit instead of buffering an unbounded file.
	limited := io.LimitReader(f, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > maxBytes {
		return nil, true, nil
	}
	return data, false, nil
}

// wrapFetchErr turns a raw go-git/transport error into a clear,
// distinguishable one: a timed-out context becomes ErrFetchTimeout, a
// counter that tripped becomes ErrRepoTooLarge (regardless of how deeply
// go-git wrapped the underlying read error), and everything else is
// wrapped with %w so callers can still errors.Is against go-git's own
// transport sentinels (auth failures, repository-not-found, etc).
func (r Repo) wrapFetchErr(op string, err error, counter *byteCounter, limits Limits) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s of %s exceeded %s", ErrFetchTimeout, op, r.URL, limits.FetchTimeout)
	}
	if counter.exceeded() || errors.Is(err, ErrRepoTooLarge) {
		return fmt.Errorf("%w: %s of %s", ErrRepoTooLarge, op, r.URL)
	}
	return fmt.Errorf("gitrepo: %s %s: %w", op, r.URL, err)
}
