package gitrepo

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/go-git/go-git/v6/plumbing/transport"
)

// Transport is the wire protocol a clone URL is fetched over. An Auth
// strategy speaks exactly one of them (see Auth's doc comment), so a URL and
// a credential that disagree can never authenticate.
type Transport string

// The two transports this package clones over.
const (
	// TransportHTTP is http:// or https://: NoneAuth, BasicAuth, PATAuth,
	// AdoSPAuth and GitHubAppAuth.
	TransportHTTP Transport = "http"
	// TransportSSH is ssh:// or the scp-like git@host:path form: SSHAuth.
	TransportSSH Transport = "ssh"
)

// remoteURLHelp is the shape advice every refusal from RemoteTransport ends
// with, so a person who typed something else sees what is accepted.
const remoteURLHelp = "use https://host/owner/repo.git, or ssh://git@host/owner/repo.git or " +
	"git@host:owner/repo.git with an SSH credential"

// RemoteTransport reports which transport raw would be cloned over, or why it
// cannot be cloned at all. It parses with go-git's own transport.ParseURL —
// the parser Repo.Files and Repo.LsRemote hand the URL to — so what it
// accepts is what a clone will actually attempt, not a second opinion of it.
//
// Accepted: http(s) URLs and ssh:// URLs with a host and a repository path,
// and scp-like [user@]host:path. Refused: anything go-git would treat as a
// local path ("not a url" parses as one), and the file:// and git://
// schemes, which no Auth strategy here authenticates.
func RemoteTransport(raw string) (Transport, error) {
	if raw == "" {
		return "", fmt.Errorf("clone URL is required: %s", remoteURLHelp)
	}
	if strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", fmt.Errorf("clone URL %q is not a git remote URL: it contains spaces or control characters; %s", raw, remoteURLHelp)
	}
	u, err := transport.ParseURL(raw)
	if err != nil {
		return "", fmt.Errorf("clone URL %q is not a git remote URL: %s", raw, remoteURLHelp)
	}
	var t Transport
	switch u.Scheme {
	case "http", "https":
		t = TransportHTTP
	case "ssh":
		t = TransportSSH
	default:
		return "", fmt.Errorf("clone URL %q is not a git remote URL: %s", raw, remoteURLHelp)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("clone URL %q has no host: %s", raw, remoteURLHelp)
	}
	if strings.Trim(u.Path, "/") == "" {
		return "", fmt.Errorf("clone URL %q names no repository: %s", raw, remoteURLHelp)
	}
	return t, nil
}
