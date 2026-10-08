package wizard

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Destination is the non-sensitive part of an org destination (the
// destinations table, docs/spec.md §5) that a wizard renders a writer for.
// It never carries a credential: for a Secret auth mode it names the
// Kubernetes Secret on the spoke cluster, and the collector reads that Secret
// at runtime through remote.kubernetes.secret (docs/spec.md §11.4).
type Destination struct {
	Name string
	// Type is the destinations.type value: "prometheus", "loki" or "otlp".
	Type string
	// URL is the writer's full endpoint URL, rendered verbatim — Shepherd
	// appends no path.
	URL             string
	AuthMode        AuthMode
	SecretNamespace string
	SecretName      string
	// OAuth2Scopes comes from the destination's extra.oauth2_scopes
	// (ExtraKeyOAuth2Scopes). Only rendered for AuthOAuth2Secret.
	OAuth2Scopes []string
}

// Destinations is an org's destinations keyed by name — the set a wizard's
// `*_dest_name` fields are resolved against. internal/mgmtapi's WizardService
// builds it from the org's rows for both RenderWizard and CommitWizard, so a
// preview and the committed pipeline always render from the same set.
type Destinations map[string]Destination

// AuthMode is a destination's destinations.auth_mode (0001_init's CHECK).
type AuthMode string

// The auth modes the destinations table admits.
const (
	AuthNone         AuthMode = "none"
	AuthBasicSecret  AuthMode = "basic_secret"
	AuthOAuth2Secret AuthMode = "oauth2_secret" //nolint:gosec // G101: an auth-mode enum value, not a credential
)

// The Secret key contract (#229): the keys a destination's Kubernetes Secret
// must hold for its auth mode. Shepherd never sees the values; only these key
// names, and the Secret's namespace/name, appear in served config.
const (
	SecretKeyUsername     = "username"      // basic_secret
	SecretKeyPassword     = "password"      // basic_secret
	SecretKeyClientID     = "client_id"     // oauth2_secret
	SecretKeyClientSecret = "client_secret" // oauth2_secret
	SecretKeyTokenURL     = "token_url"     // oauth2_secret
)

// ExtraKeyOAuth2Scopes is the destinations.extra key holding an
// oauth2_secret destination's scopes (a list of strings). Scopes are not a
// Secret key: Shepherd never reads the Secret, so it cannot know whether an
// optional key is present, and Alloy has no expression that omits an
// attribute when a key is absent. Scopes are not sensitive either.
const ExtraKeyOAuth2Scopes = "oauth2_scopes"

// SecretKeys returns the keys a destination's Secret must hold for mode, in
// the order they are rendered. Nil for AuthNone.
func SecretKeys(mode AuthMode) ([]string, error) {
	switch mode {
	case AuthNone:
		return nil, nil
	case AuthBasicSecret:
		return []string{SecretKeyUsername, SecretKeyPassword}, nil
	case AuthOAuth2Secret:
		return []string{SecretKeyClientID, SecretKeyClientSecret, SecretKeyTokenURL}, nil
	default:
		return nil, fmt.Errorf("unknown auth_mode %q: want one of none, basic_secret, oauth2_secret", mode)
	}
}

// WriterKind is an Alloy writer component a destination can be rendered
// into, paired with the destination type it accepts.
type WriterKind string

// The writer components wizards emit.
const (
	WriterPrometheus WriterKind = "prometheus.remote_write"
	WriterLoki       WriterKind = "loki.write"
)

func (k WriterKind) destinationType() (string, error) {
	switch k {
	case WriterPrometheus:
		return "prometheus", nil
	case WriterLoki:
		return "loki", nil
	default:
		return "", fmt.Errorf("unsupported writer kind %q", k)
	}
}

// labelRE is an Alloy component label as wizards use them.
var labelRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// k8sNameRE is a Kubernetes Secret name (RFC 1123 subdomain) and
// k8sNamespaceRE a namespace (RFC 1123 label).
var (
	k8sNameRE      = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
	k8sNamespaceRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
)

// ValidateSecretRef checks a Secret auth mode's namespace and name are valid
// Kubernetes names. AuthNone needs neither and accepts anything.
func ValidateSecretRef(mode AuthMode, namespace, name string) error {
	if _, err := SecretKeys(mode); err != nil {
		return err
	}
	if mode == AuthNone {
		return nil
	}
	if err := ValidateSecretName(namespace, name); err != nil {
		return fmt.Errorf("auth_mode %s needs %w", mode, err)
	}
	return nil
}

// ValidateSecretName checks namespace is a valid Kubernetes namespace name
// and name a valid Secret name — the reference a wizard renders into a
// `remote.kubernetes.secret` block.
func ValidateSecretName(namespace, name string) error {
	if !k8sNamespaceRE.MatchString(namespace) {
		return fmt.Errorf("secret_namespace, a Kubernetes namespace name (got %q)", namespace)
	}
	if !k8sNameRE.MatchString(name) {
		return fmt.Errorf("secret_name, a Kubernetes Secret name (got %q)", name)
	}
	return nil
}

// k8sSecretKeyRE is a key of a Kubernetes Secret's data map.
var k8sSecretKeyRE = regexp.MustCompile(`^[-._a-zA-Z0-9]{1,253}$`)

// ValidateSecretKey checks key is a legal Kubernetes Secret data key.
func ValidateSecretKey(key string) error {
	if !k8sSecretKeyRE.MatchString(key) || key == "." || key == ".." {
		return fmt.Errorf("%q is not a Kubernetes Secret key (letters, digits, '-', '_' and '.', at most 253)", key)
	}
	return nil
}

// RenderWriter renders the writer component `<kind> "<label>"` shipping to
// the destination destName, which must exist in dests with the type kind
// accepts. For a Secret auth mode it first renders the
// `remote.kubernetes.secret "<label>_auth"` the writer's auth block reads.
//
// Every wizard writer goes through this one function, so the URL model and
// the Secret key contract (SecretKeys) are defined once — see
// docs/plans/2026-10-01-destination-auth.md.
func RenderWriter(kind WriterKind, label string, dests Destinations, destName string) (string, error) {
	wantType, err := kind.destinationType()
	if err != nil {
		return "", err
	}
	if !labelRE.MatchString(label) {
		return "", fmt.Errorf("invalid writer label %q", label)
	}
	d, ok := dests[destName]
	if !ok {
		return "", fmt.Errorf("destination %q does not exist in this org — create it on the Destinations page first", destName)
	}
	if d.Type != wantType {
		return "", fmt.Errorf("destination %q is type %s; %s needs a %s destination", destName, d.Type, kind, wantType)
	}
	if err := validateURL(d.URL); err != nil {
		return "", fmt.Errorf("destination %q: %w", destName, err)
	}
	if err := ValidateSecretRef(d.AuthMode, d.SecretNamespace, d.SecretName); err != nil {
		return "", fmt.Errorf("destination %q: %w", destName, err)
	}

	secretLabel := label + "_auth"
	data := func(key string) string {
		return fmt.Sprintf("remote.kubernetes.secret.%s.data[%s]", secretLabel, Quote(key))
	}

	var sb strings.Builder
	if d.AuthMode != AuthNone {
		fmt.Fprintf(&sb, `remote.kubernetes.secret %s {
  namespace = %s
  name      = %s
}

`, Quote(secretLabel), Quote(d.SecretNamespace), Quote(d.SecretName))
	}

	fmt.Fprintf(&sb, `%s %s {
  endpoint {
    name = %s
    url  = %s
`, kind, Quote(label), Quote(d.Name), Quote(d.URL))

	switch d.AuthMode {
	case AuthNone:
	case AuthBasicSecret:
		fmt.Fprintf(&sb, `
    basic_auth {
      username = convert.nonsensitive(%s)
      password = %s
    }
`, data(SecretKeyUsername), data(SecretKeyPassword))
	case AuthOAuth2Secret:
		fmt.Fprintf(&sb, `
    oauth2 {
      client_id     = convert.nonsensitive(%s)
      client_secret = %s
      token_url     = convert.nonsensitive(%s)
`, data(SecretKeyClientID), data(SecretKeyClientSecret), data(SecretKeyTokenURL))
		if len(d.OAuth2Scopes) > 0 {
			quoted := make([]string, len(d.OAuth2Scopes))
			for i, s := range d.OAuth2Scopes {
				quoted[i] = Quote(s)
			}
			fmt.Fprintf(&sb, "      scopes        = [%s]\n", strings.Join(quoted, ", "))
		}
		sb.WriteString("    }\n")
	}

	sb.WriteString("  }\n}\n")
	return sb.String(), nil
}

// validateURL accepts only an absolute http(s) URL with a host — the same
// rule the destination form applies.
func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("url %q is not an absolute http:// or https:// URL", raw)
	}
	return nil
}

// Quote renders s as an Alloy string literal. Every operator-supplied value a
// wizard places inside quotes must go through it, so a quote or backslash
// cannot end the literal early and inject syntax.
func Quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
