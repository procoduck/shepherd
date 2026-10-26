// Package gitcred turns a stored git credential (git_credentials, its
// secrets encrypted at rest with internal/crypto) into the gitrepo.Auth
// strategy its kind selects, and scrubs that credential's secret values out
// of an error before it reaches a log line or a stored message. Git sync
// (internal/gitsync) and strict tenant mode's pinned imports
// (internal/pinnedimport) share it.
package gitcred

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"shepherd/internal/crypto"
	"shepherd/internal/gitrepo"
	"shepherd/internal/store/sqlc"
)

// ErrNoEncryptor is a credential with secrets and no encryption key to
// decrypt them with (security.encryption_key unset).
var ErrNoEncryptor = errors.New("the encryption key is not configured, so stored git credentials cannot be read")

// BuildAuth decrypts cred's secret(s) and builds the gitrepo.Auth strategy
// matching cred.Kind (docs/git-provider-design.md §3.2). tokens is shared
// by every ado_sp and github_app credential so their minted tokens are
// cached and refreshed independently, keyed by credential id. enc may be
// nil only for a "none" credential.
func BuildAuth(enc *crypto.Encryptor, tokens *gitrepo.TokenCache, cred sqlc.GitCredential) (gitrepo.Auth, error) {
	if cred.Kind == "none" {
		return gitrepo.NoneAuth{}, nil
	}
	if enc == nil {
		return nil, ErrNoEncryptor
	}
	switch cred.Kind {
	case "basic":
		secret, err := enc.Decrypt(cred.ClientSecretEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypting password: %w", err)
		}
		return gitrepo.BasicAuth{Username: cred.Username.String, Password: string(secret)}, nil

	case "pat":
		secret, err := enc.Decrypt(cred.ClientSecretEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypting token: %w", err)
		}
		return gitrepo.PATAuth{Username: cred.Username.String, Token: string(secret)}, nil

	case "ssh":
		key, err := enc.Decrypt(cred.ClientSecretEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypting private key: %w", err)
		}
		var passphrase string
		if len(cred.Secret2Enc) > 0 {
			pp, err := enc.Decrypt(cred.Secret2Enc)
			if err != nil {
				return nil, fmt.Errorf("decrypting private key passphrase: %w", err)
			}
			passphrase = string(pp)
		}
		return gitrepo.SSHAuth{
			User:          cred.Username.String,
			PrivateKeyPEM: key,
			Passphrase:    passphrase,
			KnownHosts:    []byte(cred.SshKnownHosts.String),
		}, nil

	case "ado_sp":
		secret, err := enc.Decrypt(cred.ClientSecretEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypting client secret: %w", err)
		}
		return gitrepo.AdoSPAuth{
			CredentialID: cred.ID.String(),
			TenantID:     cred.EntraTenantID.String,
			ClientID:     cred.ClientID.String,
			ClientSecret: string(secret),
			Cache:        tokens,
		}, nil

	case "github_app":
		key, err := enc.Decrypt(cred.ClientSecretEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypting private key: %w", err)
		}
		var pc struct {
			AppID          string `json:"app_id"`
			InstallationID string `json:"installation_id"`
			APIBase        string `json:"api_base_url"`
		}
		if len(cred.ProviderConfig) > 0 {
			if err := json.Unmarshal(cred.ProviderConfig, &pc); err != nil {
				return nil, fmt.Errorf("parsing provider_config: %w", err)
			}
		}
		return gitrepo.GitHubAppAuth{
			CredentialID:   cred.ID.String(),
			AppID:          pc.AppID,
			InstallationID: pc.InstallationID,
			PrivateKeyPEM:  key,
			APIBase:        pc.APIBase,
			Cache:          tokens,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported credential kind %q", cred.Kind)
	}
}

// Scrubber returns a function that rewrites an error so none of auth's
// secret values appear in its text. Empty values are skipped (a blank
// passphrase would otherwise match everywhere). The result is a plain
// error — wrapping is deliberately dropped, because the whole point is that
// the original text never reaches a sink.
func Scrubber(auth gitrepo.Auth) func(error) error {
	var secrets []string
	switch a := auth.(type) {
	case gitrepo.BasicAuth:
		secrets = append(secrets, a.Password)
	case gitrepo.PATAuth:
		secrets = append(secrets, a.Token)
	case gitrepo.SSHAuth:
		secrets = append(secrets, a.Passphrase, string(a.PrivateKeyPEM))
	case gitrepo.AdoSPAuth:
		secrets = append(secrets, a.ClientSecret)
	case gitrepo.GitHubAppAuth:
		secrets = append(secrets, string(a.PrivateKeyPEM))
	}
	return func(err error) error { return Redact(err, secrets) }
}

// Redact replaces every non-empty secret in err's text with "[REDACTED]".
// A nil error stays nil; an error containing none of the secrets is
// returned unchanged, wrapping intact.
func Redact(err error, secrets []string) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	changed := false
	for _, s := range secrets {
		if s == "" || !strings.Contains(text, s) {
			continue
		}
		text = strings.ReplaceAll(text, s, "[REDACTED]")
		changed = true
	}
	if !changed {
		return err
	}
	return errors.New(text)
}
