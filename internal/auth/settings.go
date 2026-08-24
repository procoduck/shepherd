package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Settings source values. Source is derived, never stored: it records which
// of the two configuration inputs won, so the UI can tell an admin why the
// form is read-only.
const (
	// SourceHelm means config.OIDCConfig (the Helm chart / SHEPHERD_OIDC_*
	// environment) supplied the issuer. Chart config always wins — a cluster
	// whose identity provider is declared in git must not be re-pointed by
	// whoever holds an app-admin session.
	SourceHelm = "helm"
	// SourceDatabase means the oidc_settings row (0014) supplied it, because
	// the chart did not.
	SourceDatabase = "database"
)

// Settings is the fully resolved OIDC configuration one login flow needs:
// endpoints, client credentials, and the claim names that turn an ID token
// into a Session. It is the single shape both configuration sources produce,
// so nothing downstream of resolution branches on where the values came from.
//
// ClientSecret is plaintext and lives only in memory. It is never logged (see
// LogValue) and never leaves the process — the admin API answers
// ClientSecretSet, not the value.
type Settings struct {
	Enabled     bool
	Provider    string
	DisplayName string

	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string

	SubjectClaim string
	EmailClaim   string
	NameClaim    string
	GroupsClaim  string

	// AppAdminGroups holds the group values that grant app-admin. Values are
	// compared against whatever the groups claim (or the Graph lookup) yields
	// — Entra object GUIDs for Entra, group names or full paths elsewhere.
	AppAdminGroups []string

	// UseGraphGroups routes group resolution through Microsoft Graph's
	// transitiveMemberOf instead of reading the ID token claim. Meaningful
	// for Entra only.
	UseGraphGroups bool
	GraphBaseURL   string

	Source    string
	UpdatedAt time.Time
	UpdatedBy string
}

// LogValue redacts the client secret in structured logs, matching
// config.OIDCConfig.LogValue.
func (s Settings) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("source", s.Source),
		slog.Bool("enabled", s.Enabled),
		slog.String("provider", s.Provider),
		slog.String("issuer", s.Issuer),
		slog.String("client_id", s.ClientID),
		slog.String("client_secret", "***"),
		slog.String("redirect_url", s.RedirectURL),
		slog.Any("scopes", s.Scopes),
		slog.String("groups_claim", s.GroupsClaim),
		slog.Bool("use_graph_groups", s.UseGraphGroups),
	)
}

// ErrNoSettings is returned by SettingsStore.Get when no oidc_settings row
// exists yet — the normal state of a deployment that has never configured
// OIDC through the UI, not an error condition.
var ErrNoSettings = errors.New("auth: no OIDC settings configured")

// ErrEncryptionUnavailable is returned when a settings write is attempted
// without an encryptor. Without one the client secret could only be stored in
// plaintext, and this feature declines to do that rather than degrade
// quietly: security.encryption_key is already required by config.Load, so in
// practice only a hand-built test wiring hits this.
var ErrEncryptionUnavailable = errors.New("auth: OIDC settings require security.encryption_key to be configured")

// SettingsStore reads and writes the singleton oidc_settings row, handling
// client-secret encryption on the way through. It is the only place the
// plaintext secret crosses the database boundary.
type SettingsStore struct {
	store *store.Store
	enc   *crypto.Encryptor
}

// NewSettingsStore constructs a SettingsStore. enc may be nil, in which case
// reads still work for rows written earlier (they will fail to decrypt and
// report it) and writes are refused with ErrEncryptionUnavailable.
func NewSettingsStore(st *store.Store, enc *crypto.Encryptor) *SettingsStore {
	return &SettingsStore{store: st, enc: enc}
}

// Get returns the stored settings with the client secret decrypted, or
// ErrNoSettings when the row does not exist.
func (s *SettingsStore) Get(ctx context.Context) (*Settings, error) {
	if s == nil || s.store == nil {
		return nil, ErrNoSettings
	}
	row, err := s.store.Queries.GetOIDCSettings(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoSettings
		}
		return nil, fmt.Errorf("loading OIDC settings: %w", err)
	}
	secret := ""
	if len(row.ClientSecretEnc) > 0 {
		if s.enc == nil {
			return nil, ErrEncryptionUnavailable
		}
		plain, decErr := s.enc.Decrypt(row.ClientSecretEnc)
		if decErr != nil {
			// A decrypt failure here means the encryption key changed under a
			// stored secret. Say so precisely: the recovery is to re-enter the
			// client secret, and an admin who reads "decrypting: ..." without
			// that sentence will go looking for a database problem instead.
			return nil, fmt.Errorf("decrypting OIDC client secret (the encryption key may have changed since it was saved; re-enter the client secret to fix): %w", decErr)
		}
		secret = string(plain)
	}
	return &Settings{
		Enabled:        row.Enabled,
		Provider:       row.Provider,
		DisplayName:    row.DisplayName,
		Issuer:         row.Issuer,
		ClientID:       row.ClientID,
		ClientSecret:   secret,
		RedirectURL:    row.RedirectUrl,
		Scopes:         row.Scopes,
		SubjectClaim:   row.SubjectClaim,
		EmailClaim:     row.EmailClaim,
		NameClaim:      row.NameClaim,
		GroupsClaim:    row.GroupsClaim,
		AppAdminGroups: row.AppAdminGroups,
		UseGraphGroups: row.UseGraphGroups,
		GraphBaseURL:   row.GraphBaseUrl,
		Source:         SourceDatabase,
		UpdatedAt:      row.UpdatedAt.Time,
		UpdatedBy:      row.UpdatedBy,
	}, nil
}

// Save validates and upserts the settings row, encrypting the client secret.
// actor is recorded in updated_by.
func (s *SettingsStore) Save(ctx context.Context, in *Settings, actor string) (*Settings, error) {
	if s == nil || s.store == nil {
		return nil, ErrNoSettings
	}
	if s.enc == nil {
		return nil, ErrEncryptionUnavailable
	}
	normalized := *in
	normalized.Normalize()
	if err := normalized.Validate(); err != nil {
		return nil, err
	}
	secretEnc, err := s.enc.Encrypt([]byte(normalized.ClientSecret))
	if err != nil {
		return nil, fmt.Errorf("encrypting OIDC client secret: %w", err)
	}
	row, err := s.store.Queries.UpsertOIDCSettings(ctx, sqlc.UpsertOIDCSettingsParams{
		Enabled:         normalized.Enabled,
		Provider:        normalized.Provider,
		DisplayName:     normalized.DisplayName,
		Issuer:          normalized.Issuer,
		ClientID:        normalized.ClientID,
		ClientSecretEnc: secretEnc,
		RedirectUrl:     normalized.RedirectURL,
		Scopes:          normalized.Scopes,
		SubjectClaim:    normalized.SubjectClaim,
		EmailClaim:      normalized.EmailClaim,
		NameClaim:       normalized.NameClaim,
		GroupsClaim:     normalized.GroupsClaim,
		AppAdminGroups:  normalized.AppAdminGroups,
		UseGraphGroups:  normalized.UseGraphGroups,
		GraphBaseUrl:    normalized.GraphBaseURL,
		UpdatedBy:       actor,
	})
	if err != nil {
		return nil, fmt.Errorf("saving OIDC settings: %w", err)
	}
	normalized.Source = SourceDatabase
	normalized.UpdatedAt = row.UpdatedAt.Time
	normalized.UpdatedBy = row.UpdatedBy
	return &normalized, nil
}

// Delete removes the settings row.
func (s *SettingsStore) Delete(ctx context.Context) error {
	if s == nil || s.store == nil {
		return ErrNoSettings
	}
	if err := s.store.Queries.DeleteOIDCSettings(ctx); err != nil {
		return fmt.Errorf("deleting OIDC settings: %w", err)
	}
	return nil
}

// Normalize fills in the defaults a partially specified submission omits and
// trims the whitespace an admin pasting values out of a provider console
// invariably brings along. It runs before Validate, so Validate only has to
// judge values that are already in canonical form.
func (s *Settings) Normalize() {
	preset := PresetByKey(s.Provider)
	if s.Provider == "" {
		s.Provider = ProviderGeneric
	}
	s.Issuer = strings.TrimSpace(s.Issuer)
	// A pasted discovery-document URL is the single most common wrong value
	// for this field, and the fix is unambiguous, so make it rather than
	// rejecting it.
	s.Issuer = strings.TrimSuffix(s.Issuer, "/.well-known/openid-configuration")
	// Trailing slashes matter: go-oidc compares the discovered issuer against
	// the one it was given, byte for byte. Auth0 genuinely issues tokens with
	// a trailing slash and everything else does not, so trim it everywhere
	// except there and let discovery's own mismatch error speak if an admin
	// still gets it wrong.
	if s.Provider != ProviderAuth0 {
		s.Issuer = strings.TrimSuffix(s.Issuer, "/")
	}
	s.ClientID = strings.TrimSpace(s.ClientID)
	s.ClientSecret = strings.TrimSpace(s.ClientSecret)
	s.RedirectURL = strings.TrimSpace(s.RedirectURL)
	s.DisplayName = strings.TrimSpace(s.DisplayName)
	if s.DisplayName == "" {
		s.DisplayName = preset.DisplayName
	}
	s.Scopes = normalizeScopes(s.Scopes, preset.Scopes)
	s.SubjectClaim = defaultString(strings.TrimSpace(s.SubjectClaim), preset.SubjectClaim)
	s.EmailClaim = defaultString(strings.TrimSpace(s.EmailClaim), preset.EmailClaim)
	s.NameClaim = defaultString(strings.TrimSpace(s.NameClaim), preset.NameClaim)
	s.GroupsClaim = defaultString(strings.TrimSpace(s.GroupsClaim), preset.GroupsClaim)
	s.AppAdminGroups = normalizeList(s.AppAdminGroups)
	if !preset.SupportsGraphGroups {
		// Graph is Entra's directory API. Storing "on" for any other provider
		// would be a setting that silently does nothing, so it is cleared at
		// the boundary rather than ignored at login time.
		s.UseGraphGroups = false
	}
	s.GraphBaseURL = defaultString(strings.TrimSpace(s.GraphBaseURL), "https://graph.microsoft.com")
	s.GraphBaseURL = strings.TrimSuffix(s.GraphBaseURL, "/")
}

// Validate reports the first problem that would make this configuration fail
// at login time, phrased for an admin reading it in the settings form.
func (s *Settings) Validate() error {
	if err := validateIssuer(s.Issuer); err != nil {
		return err
	}
	if s.ClientID == "" {
		return errors.New("client ID is required")
	}
	if s.ClientSecret == "" {
		return errors.New("client secret is required")
	}
	if err := validateRedirectURL(s.RedirectURL); err != nil {
		return err
	}
	if !slices.Contains(s.Scopes, "openid") {
		return errors.New("scopes must include \"openid\" — without it the provider returns an OAuth2 access token but no ID token, and Shepherd has no identity to build a session from")
	}
	for _, claim := range []struct{ name, value string }{
		{"subject claim", s.SubjectClaim},
		{"email claim", s.EmailClaim},
		{"name claim", s.NameClaim},
		{"groups claim", s.GroupsClaim},
	} {
		if claim.value == "" {
			return fmt.Errorf("%s is required", claim.name)
		}
	}
	if s.UseGraphGroups {
		if s.Provider != ProviderEntra {
			return errors.New("the Microsoft Graph group lookup is only available for the Microsoft Entra ID provider")
		}
		if err := validateGraphBaseURL(s.GraphBaseURL); err != nil {
			return err
		}
	}
	return nil
}

// graphHosts is every host Microsoft actually serves Graph on: the global
// endpoint plus the sovereign clouds.
//
//nolint:gochecknoglobals // static allowlist, read-only after init
var graphHosts = []string{
	"graph.microsoft.com",             // global
	"graph.microsoft.us",              // US Gov (GCC High)
	"dod-graph.microsoft.us",          // US Gov (DoD)
	"microsoftgraph.chinacloudapi.cn", // China (21Vianet)
}

// validateGraphBaseURL pins the Graph endpoint to Microsoft's own hosts.
//
// This is an allowlist rather than a shape check because of what the value is
// used for: resolveGroups sends the signing-in user's DELEGATED Entra access
// token to it as a bearer credential. "Any https URL" would let whoever holds
// an app-admin session redirect every user's access token — a token whose
// scopes reach well beyond Shepherd — to a collector they control, on the next
// login, with no other compromise required. The field exists to select a
// sovereign cloud, and that is a closed set.
func validateGraphBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("the Microsoft Graph base URL is not a valid URL: %w", err)
	}
	if u.Scheme != "https" {
		return errors.New("the Microsoft Graph base URL must use https")
	}
	if !slices.Contains(graphHosts, strings.ToLower(u.Hostname())) {
		return fmt.Errorf("the Microsoft Graph base URL must be one of Microsoft's own Graph endpoints (%s) — every signing-in user's access token is sent to it, so it cannot point anywhere else", strings.Join(graphHosts, ", "))
	}
	if u.Path != "" && u.Path != "/" {
		return errors.New("the Microsoft Graph base URL must not include a path")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("the Microsoft Graph base URL must not include credentials, a query string, or a fragment")
	}
	return nil
}

func validateIssuer(issuer string) error {
	if issuer == "" {
		return errors.New("issuer URL is required")
	}
	u, err := url.Parse(issuer)
	if err != nil {
		return fmt.Errorf("issuer URL is not a valid URL: %w", err)
	}
	if u.Scheme != "https" {
		return errors.New("issuer URL must use https — the discovery document names the JWKS endpoint Shepherd fetches signing keys from, and over http anyone on the path can substitute their own")
	}
	if u.Host == "" {
		return errors.New("issuer URL must include a host")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("issuer URL must not contain a query string or fragment")
	}
	return nil
}

func validateRedirectURL(redirect string) error {
	if redirect == "" {
		return errors.New("redirect URL is required")
	}
	u, err := url.Parse(redirect)
	if err != nil {
		return fmt.Errorf("redirect URL is not a valid URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return errors.New("redirect URL must be an absolute http:// or https:// URL")
	}
	if u.Host == "" {
		return errors.New("redirect URL must include a host")
	}
	if u.User != nil {
		return errors.New("redirect URL must not contain credentials")
	}
	// EXACT match, not a suffix. The route tree serves the callback at exactly
	// /auth/callback (internal/server/server.go), so this is the only path that
	// can work; a suffix test would also accept /evil/auth/callback, which
	// cannot.
	//
	// Note what this does and does not promise. It rejects a path that could
	// never work. It does NOT verify the URL points at THIS deployment — the
	// host is unconstrained here, because Shepherd cannot reliably know its own
	// external hostname (server.base_url is frequently left at its placeholder)
	// and refusing a legitimate redirect URL would be worse than accepting a
	// useless one. The provider's own registered-redirect allowlist is the
	// control that matters for a wrong host; the settings page warns when the
	// host differs from the one the admin is browsing.
	if strings.TrimSuffix(u.Path, "/") != CallbackPath {
		return fmt.Errorf("redirect URL path must be exactly %s — that is the only path Shepherd serves the OIDC callback on, and it must match a redirect URI registered with the provider", CallbackPath)
	}
	return nil
}

// CallbackPath is the single path Shepherd serves the OIDC callback on. It is
// referenced by the route tree, by redirect-URL validation, and by the admin
// UI's suggested value, so all three cannot drift apart.
const CallbackPath = "/auth/callback"

func normalizeScopes(scopes, fallback []string) []string {
	out := normalizeList(scopes)
	if len(out) == 0 {
		out = normalizeList(fallback)
	}
	// "openid" is not optional (see Validate) and its absence is far more
	// often an editing slip than a decision, so put it back rather than
	// failing a save over it.
	if !slices.Contains(out, "openid") {
		out = append([]string{"openid"}, out...)
	}
	return out
}

// normalizeList trims, drops empties, and de-duplicates while preserving the
// admin's ordering (which the UI shows back to them).
func normalizeList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || slices.Contains(out, v) {
			continue
		}
		out = append(out, v)
	}
	return out
}

func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// settingsFromConfig projects the Helm/environment configuration
// (config.OIDCConfig plus the app-admin group list and Graph settings that
// have always lived beside it) onto the same Settings shape the database path
// produces. Resolution below picks one or the other; nothing downstream needs
// to know which.
func settingsFromConfig(cfg *config.Config) *Settings {
	s := &Settings{
		Enabled:        true,
		Provider:       cfg.OIDC.Provider,
		DisplayName:    cfg.OIDC.DisplayName,
		Issuer:         cfg.OIDC.Issuer,
		ClientID:       cfg.OIDC.ClientID,
		ClientSecret:   cfg.OIDC.ClientSecret,
		RedirectURL:    cfg.OIDC.RedirectURL,
		Scopes:         cfg.OIDC.Scopes,
		SubjectClaim:   cfg.OIDC.SubjectClaim,
		EmailClaim:     cfg.OIDC.EmailClaim,
		NameClaim:      cfg.OIDC.NameClaim,
		GroupsClaim:    cfg.OIDC.GroupsClaim,
		AppAdminGroups: cfg.Auth.AppAdminGroupIDs,
		UseGraphGroups: cfg.OIDC.UseGraphGroups,
		GraphBaseURL:   cfg.Graph.BaseURL,
		Source:         SourceHelm,
	}
	// Normalize is applied for the claim/scope defaults, but the issuer is
	// restored verbatim afterwards. Its rewrites (trailing-slash and
	// discovery-suffix trimming) exist to forgive a value pasted into a form;
	// a chart value was typed deliberately, and go-oidc's own
	// issuer-mismatch error names the problem far more clearly than a silent
	// rewrite that changes what the operator declared.
	declaredIssuer := s.Issuer
	s.Normalize()
	s.Issuer = declaredIssuer
	return s
}
