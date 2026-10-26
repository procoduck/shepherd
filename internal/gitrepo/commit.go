package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/memory"
)

// Errors of FileAtCommit and CheckPinned.
var (
	// ErrNotACommit is a revision that is not a full, lowercase, 40-hex
	// commit SHA.
	ErrNotACommit = errors.New("gitrepo: revision is not a full lowercase 40-character commit SHA")
	// ErrCommitNotFound is a commit the remote does not have (or no branch
	// or tag reaches).
	ErrCommitNotFound = errors.New("gitrepo: commit not found")
	// ErrPathNotFound is a path that does not exist at the commit.
	ErrPathNotFound = errors.New("gitrepo: path not found at the commit")
	// ErrNotAFile is a path that names a directory, a symlink or a
	// submodule rather than a regular file.
	ErrNotAFile = errors.New("gitrepo: path is not a regular file")
	// ErrFileTooLarge is a file larger than Limits.MaxFileBytes.
	ErrFileTooLarge = errors.New("gitrepo: file exceeds max_file_bytes")
	// ErrRevisionShadowed is a commit SHA that a branch or tag of the same
	// name shadows: Alloy's import.git checks a revision out as a tag, then a
	// branch, then a commit, so it would follow that ref instead.
	ErrRevisionShadowed = errors.New("gitrepo: a branch or tag is named like the commit SHA")
)

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// IsFullCommitSHA reports whether rev is a full, lowercase, 40-hex SHA-1
// commit id: the only revision form FileAtCommit accepts.
func IsFullCommitSHA(rev string) bool { return fullSHA.MatchString(rev) }

// CheckPinned lists the remote's refs (one cheap ls-remote, no objects) and
// refuses (ErrRevisionShadowed) when a branch or tag is named commit. Any
// other error means the remote could not be listed: unreachable, refused
// credentials, a redirect refused, over the byte limit or the timeout.
func (r Repo) CheckPinned(ctx context.Context, commit string) error {
	if !IsFullCommitSHA(commit) {
		return ErrNotACommit
	}
	limits := r.Limits.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, limits.FetchTimeout)
	defer cancel()
	return r.listRefs(ctx, limits, commit)
}

// listRefs is the ls-remote of CheckPinned.
func (r Repo) listRefs(ctx context.Context, limits Limits, commit string) error {
	counter := newByteCounter(limits.MaxRepoBytes)
	opts, err := r.clientOptions(ctx, counter)
	if err != nil {
		return err
	}
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: "origin", URLs: []string{r.URL}})
	refs, err := remote.ListContext(ctx, &git.ListOptions{ClientOptions: opts})
	if err != nil {
		return r.wrapFetchErr("ls-remote", err, counter, limits)
	}
	if counter.exceeded() {
		return fmt.Errorf("%w: ls-remote of %s", ErrRepoTooLarge, r.URL)
	}
	for _, ref := range refs {
		n := ref.Name()
		if (n.IsBranch() || n.IsTag()) && n.Short() == commit {
			return fmt.Errorf("%w: %s on %s", ErrRevisionShadowed, n, r.URL)
		}
	}
	return nil
}

// FileAtCommit fetches commit — a full lowercase SHA (IsFullCommitSHA) — in
// memory and returns the regular file at file (a clean path relative to the
// repository root). Repo.Branch and Repo.Path are not used. It refuses a
// commit a branch or tag of the same name shadows (CheckPinned).
//
// The commit is fetched alone (depth 1) when the server allows a want of a
// reachable SHA; otherwise every branch and tag is fetched and the commit
// looked up among them, which is what Alloy itself does. Either way the
// whole call is bounded by Limits: MaxRepoBytes over everything
// transferred, FetchTimeout over its wall-clock time, MaxFileBytes for the
// file.
func (r Repo) FileAtCommit(ctx context.Context, commit, file string) ([]byte, error) {
	if !IsFullCommitSHA(commit) {
		return nil, ErrNotACommit
	}
	if file == "" || path.IsAbs(file) || path.Clean(file) != file || strings.HasPrefix(file, "../") || file == ".." {
		return nil, fmt.Errorf("%w: %q is not a clean relative path", ErrPathNotFound, file)
	}
	limits := r.Limits.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, limits.FetchTimeout)
	defer cancel()

	if err := r.listRefs(ctx, limits, commit); err != nil {
		return nil, err
	}

	counter := newByteCounter(limits.MaxRepoBytes)
	opts, err := r.clientOptions(ctx, counter)
	if err != nil {
		return nil, err
	}
	repo, err := git.Init(memory.NewStorage())
	if err != nil {
		return nil, fmt.Errorf("gitrepo: initialising an in-memory repository: %w", err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{r.URL}}); err != nil {
		return nil, fmt.Errorf("gitrepo: adding the remote: %w", err)
	}
	err = repo.FetchContext(ctx, &git.FetchOptions{
		RefSpecs:      []config.RefSpec{config.RefSpec(commit + ":refs/pinned/" + commit)},
		Depth:         1,
		Tags:          plumbing.NoTags,
		ClientOptions: opts,
	})
	if errors.Is(err, git.ErrExactSHA1NotSupported) {
		// The server does not let a client want a commit by id. Fetch every
		// branch and tag instead, as Alloy does, and look the commit up.
		err = repo.FetchContext(ctx, &git.FetchOptions{
			RefSpecs: []config.RefSpec{
				"+refs/heads/*:refs/remotes/origin/*",
				"+refs/tags/*:refs/tags/*",
			},
			Tags:          plumbing.NoTags,
			ClientOptions: opts,
		})
	}
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		if counter.exceeded() || errors.Is(err, ErrRepoTooLarge) || errors.Is(err, context.DeadlineExceeded) {
			return nil, r.wrapFetchErr("fetch", err, counter, limits)
		}
		if isMissingObject(err) {
			return nil, fmt.Errorf("%w: %s on %s", ErrCommitNotFound, commit, r.URL)
		}
		return nil, r.wrapFetchErr("fetch", err, counter, limits)
	}
	if counter.exceeded() {
		return nil, fmt.Errorf("%w: fetch of %s", ErrRepoTooLarge, r.URL)
	}
	c, err := repo.CommitObject(plumbing.NewHash(commit))
	if err != nil {
		return nil, fmt.Errorf("%w: %s on %s", ErrCommitNotFound, commit, r.URL)
	}
	return readCommitFile(c, file, limits.MaxFileBytes)
}

// isMissingObject reports whether a fetch failed because the server has no
// such object (it answers a want of an unknown id with an error).
func isMissingObject(err error) bool {
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not our ref") || strings.Contains(msg, "not found") ||
		strings.Contains(msg, "unadvertised object") || strings.Contains(msg, "no such")
}

// readCommitFile returns the regular file at file in c's tree.
func readCommitFile(c *object.Commit, file string, maxBytes int64) ([]byte, error) {
	tree, err := c.Tree()
	if err != nil {
		return nil, fmt.Errorf("gitrepo: reading the commit's tree: %w", err)
	}
	entry, err := tree.FindEntry(file)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrPathNotFound, file)
	}
	if entry.Mode != filemode.Regular && entry.Mode != filemode.Executable {
		return nil, fmt.Errorf("%w: %q", ErrNotAFile, file)
	}
	f, err := tree.TreeEntryFile(entry)
	if err != nil {
		return nil, fmt.Errorf("gitrepo: reading %q: %w", file, err)
	}
	if f.Size > maxBytes {
		return nil, fmt.Errorf("%w: %q is %d bytes, the limit is %d", ErrFileTooLarge, file, f.Size, maxBytes)
	}
	rd, err := f.Reader()
	if err != nil {
		return nil, fmt.Errorf("gitrepo: reading %q: %w", file, err)
	}
	defer func() { _ = rd.Close() }() //nolint:errcheck // in-memory reader
	data, err := io.ReadAll(io.LimitReader(rd, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("gitrepo: reading %q: %w", file, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: %q", ErrFileTooLarge, file)
	}
	return data, nil
}
