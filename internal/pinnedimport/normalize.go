package pinnedimport

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode"

	"shepherd/internal/gitrepo"
)

// NormalizeRepository is the form an import.git repository is compared to an
// org's allowlist in, so a spelling cannot dodge it. Two URLs normalise
// alike only when every git client reaches the same repository through both:
//
//   - the scheme and host are lower-cased (both are case-insensitive), and an
//     IPv6 host keeps its brackets;
//   - a default port is dropped (443 for https, 80 for http, 22 for ssh);
//   - the scp-like [user@]host:path form becomes ssh://[user@]host/path;
//   - one trailing "/" and then one trailing ".git" are dropped.
//
// Everything else is kept as written and is significant: the path's case
// (most git servers are case-sensitive), the scheme (http and https are
// different endpoints), a non-default port and the ssh user.
//
// Refused, as ambiguous or as something no allowlist entry should be: what
// gitrepo.RemoteTransport refuses (local paths, file://, git://, any other
// scheme, no host, no repository path), whitespace or control characters,
// percent-encoding, a backslash, a query or fragment, user info on http(s)
// (credentials belong in a stored git credential), a password on ssh, an
// empty, "." or ".." path segment, and an scp-like path starting with "/"
// or "~".
func NormalizeRepository(raw string) (string, error) {
	if _, err := gitrepo.RemoteTransport(raw); err != nil {
		return "", err
	}
	if strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("repository %q contains whitespace", raw)
	}
	for _, bad := range []string{"%", "\\", "?", "#"} {
		if strings.Contains(raw, bad) {
			return "", fmt.Errorf("repository %q contains %q, which makes it ambiguous; write the URL without "+
				"percent-encoding, a query or a fragment", raw, bad)
		}
	}
	u, err := parseRemote(raw)
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(u.Scheme)
	var user string
	if u.User != nil {
		if scheme != "ssh" {
			return "", fmt.Errorf("repository %q carries user information; give a %s repository's credentials "+
				"as a stored git credential instead", raw, scheme)
		}
		if _, hasPassword := u.User.Password(); hasPassword {
			return "", fmt.Errorf("repository %q carries a password; use a stored git credential", raw)
		}
		user = u.User.Username() + "@"
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("repository %q has no host", raw)
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" && port != defaultPort[scheme] {
		host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	p, err := normalPath(raw, u.Path)
	if err != nil {
		return "", err
	}
	return scheme + "://" + user + host + p, nil
}

var defaultPort = map[string]string{"https": "443", "http": "80", "ssh": "22"}

// parseRemote parses raw as a URL, turning the scp-like form into ssh://.
func parseRemote(raw string) (*url.URL, error) {
	if !strings.Contains(raw, "://") {
		userHost, p, ok := strings.Cut(raw, ":")
		if !ok || p == "" {
			return nil, fmt.Errorf("repository %q is not a git remote URL", raw)
		}
		if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
			return nil, fmt.Errorf("repository %q names a path from the root or a home directory, which "+
				"servers resolve differently; write it as ssh://host/owner/repo", raw)
		}
		raw = "ssh://" + userHost + "/" + p
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("repository %q is not a URL: %w", raw, err)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, fmt.Errorf("repository %q is ambiguous", raw)
	}
	return u, nil
}

// normalPath checks a repository path's segments and drops one trailing "/"
// and then one trailing ".git".
func normalPath(raw, p string) (string, error) {
	p = strings.TrimSuffix(p, "/")
	p = strings.TrimSuffix(p, ".git")
	if !strings.HasPrefix(p, "/") || p == "/" {
		return "", fmt.Errorf("repository %q names no repository", raw)
	}
	for _, seg := range strings.Split(p[1:], "/") {
		switch seg {
		case "", ".", "..":
			return "", fmt.Errorf("repository %q has an empty, \".\" or \"..\" path segment, which makes it "+
				"ambiguous", raw)
		}
	}
	return p, nil
}

// errNotAllowed is the refusal of a repository not on the org's allowlist.
var errNotAllowed = errors.New("is not on the organisation's allowlist of import repositories (an application " +
	"administrator adds it under Admin → Organisations)")
