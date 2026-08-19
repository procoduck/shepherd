package gitrepo

import (
	"context"
	"fmt"
	"os"

	"github.com/go-git/go-git/v6/plumbing/transport/ssh/knownhosts"
	gossh "golang.org/x/crypto/ssh"
)

// defaultSSHUser is the SSH login user assumed when SSHAuth.User is empty.
// It matches the deploy-key convention used by GitHub, GitLab, Gitea, and
// Bitbucket: the SSH *username* is always "git" (or a fixed literal); the
// caller's identity comes entirely from which private key signs the
// connection, not from the username.
const defaultSSHUser = "git"

// SSHAuth authenticates over SSH using a private key, verifying the
// remote's host key against a per-credential known_hosts. There is
// deliberately no accept-any-host-key mode: an unverified SSH host key is
// an undetectable MITM (docs/git-provider-design.md §7). A wrong or
// missing known_hosts entry must fail the connection, not silently trust
// it.
type SSHAuth struct {
	// User is the SSH login user. Empty defaults to "git" (defaultSSHUser).
	User string
	// PrivateKeyPEM is the PEM-encoded private key (PKCS#1, PKCS#8,
	// EC, or OpenSSH format — anything golang.org/x/crypto/ssh.ParsePrivateKey
	// accepts). Required.
	PrivateKeyPEM []byte
	// Passphrase decrypts PrivateKeyPEM when it is passphrase-protected.
	// Empty for an unencrypted key.
	Passphrase string
	// KnownHosts is OpenSSH known_hosts content (one or more
	// "host key-type base64-key" lines, in the format `ssh-keyscan`
	// produces) used to verify the remote host key. Required: SSH() and
	// sshHostKeyCallback fail if this is empty rather than falling back
	// to any form of unverified connection.
	KnownHosts []byte
}

func (a SSHAuth) user() string {
	if a.User != "" {
		return a.User
	}
	return defaultSSHUser
}

// HTTP implements Auth: the ssh strategy never speaks HTTP.
func (SSHAuth) HTTP(context.Context) (string, string, bool, error) { return "", "", false, nil }

// SSH implements Auth: it parses PrivateKeyPEM into a signer, decrypting
// with Passphrase if the key is encrypted.
func (a SSHAuth) SSH(context.Context) (gossh.Signer, bool, error) {
	if len(a.PrivateKeyPEM) == 0 {
		return nil, false, fmt.Errorf("gitrepo: ssh auth: private key is required")
	}

	signer, err := gossh.ParsePrivateKey(a.PrivateKeyPEM)
	if _, missing := err.(*gossh.PassphraseMissingError); missing { //nolint:errorlint // ParsePrivateKey documents this concrete sentinel type, not a wrapped chain
		signer, err = gossh.ParsePrivateKeyWithPassphrase(a.PrivateKeyPEM, []byte(a.Passphrase))
	}
	if err != nil {
		return nil, false, fmt.Errorf("gitrepo: ssh auth: parsing private key: %w", err)
	}
	return signer, true, nil
}

// sshUsername implements sshAuthDetails.
func (a SSHAuth) sshUsername() string { return a.user() }

// sshHostKeyCallback implements sshAuthDetails. It parses KnownHosts into a
// verifying callback; there is deliberately no fallback that accepts an
// unknown or mismatched host key.
func (a SSHAuth) sshHostKeyCallback() (gossh.HostKeyCallback, error) {
	if len(a.KnownHosts) == 0 {
		return nil, fmt.Errorf("gitrepo: ssh auth: known_hosts is required (no accept-any-host-key mode)")
	}
	return parseKnownHosts(a.KnownHosts)
}

// parseKnownHosts builds a verifying gossh.HostKeyCallback from raw
// known_hosts content. go-git's knownhosts.NewDB only reads from file
// paths, so the content is staged to a private temp file for the duration
// of the call and removed immediately after — NewDB parses eagerly into an
// in-memory callback, so the file need not (and does not) outlive this
// function.
func parseKnownHosts(content []byte) (gossh.HostKeyCallback, error) {
	f, err := os.CreateTemp("", "shepherd-known-hosts-*")
	if err != nil {
		return nil, fmt.Errorf("gitrepo: ssh auth: creating known_hosts temp file: %w", err)
	}
	path := f.Name()
	defer os.Remove(path) //nolint:errcheck // best-effort cleanup of a private temp file holding non-secret known_hosts content

	if _, err := f.Write(content); err != nil {
		_ = f.Close() //nolint:errcheck // already returning the write error below
		return nil, fmt.Errorf("gitrepo: ssh auth: writing known_hosts temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("gitrepo: ssh auth: closing known_hosts temp file: %w", err)
	}

	db, err := knownhosts.NewDB(path)
	if err != nil {
		return nil, fmt.Errorf("gitrepo: ssh auth: parsing known_hosts: %w", err)
	}
	return db.HostKeyCallback(), nil
}
