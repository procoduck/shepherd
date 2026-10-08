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
	// Interior spaces are allowed: an Azure DevOps project name may contain
	// one ("https://dev.azure.com/org/My Project/_git/repo"), and net/url
	// escapes it on the wire, so such a URL has always cloned. A space in the
	// host still fails below (url.Parse refuses it; the scp-like form cannot
	// match it). Leading/trailing whitespace and control characters (a pasted
	// newline or tab) are never part of a URL.
	if raw != strings.TrimSpace(raw) || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("clone URL %q is not a git remote URL: it has leading or trailing whitespace or a control character; %s", raw, remoteURLHelp)
	}
	// go-git's scp-like pattern takes the host as everything up to the first
	// ':', so "git@[2001:db8::1]:owner/repo.git" parses with host "[2001" and
	// would dial nonsense. The ssh:// form brackets IPv6 hosts correctly.
	if !strings.Contains(raw, "://") {
		if _, hostPart, _ := strings.Cut(raw, "@"); strings.HasPrefix(raw, "[") || strings.HasPrefix(hostPart, "[") {
			return "", fmt.Errorf("clone URL %q puts an IPv6 address in the git@host:path form, which git "+
				"clients misread: use ssh://git@[address]/owner/repo.git instead", raw)
		}
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
