// Package auth implements OIDC BFF, session management, and RBAC middleware.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	oidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/oauth2"

	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/graph"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Session is the decoded session stored in context.
type Session struct {
	ID          string
	UserOID     string
	Email       string
	DisplayName string
	GroupIDs    []string
	IsAppAdmin  bool
	Source      string
}

type contextKey int

const (
	sessionKey contextKey = iota
	actorKey
)

// sessionCookie constructs the canonical shepherd_session cookie.
// value is the session ID or "" for the clear cookie; maxAge is 0 for a session
// cookie, positive for TTL, or -1 to delete.
func sessionCookie(value string, maxAge int, insecureCookies bool) *http.Cookie {
	return &http.Cookie{ //nolint:gosec // G124: Secure/HttpOnly/SameSite all set
		Name:     "shepherd_session",
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
		Secure:   !insecureCookies,
	}
}

// SessionFromCtx returns the session from the context, or nil if not present.
func SessionFromCtx(ctx context.Context) *Session {
	if s, ok := ctx.Value(sessionKey).(*Session); ok {
		return s
	}
	return nil
}

// SetActor stores the actor identity in the context.
func SetActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey, actor)
}

// ActorFromCtx returns the actor identity from context, or anonymous.
func ActorFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(actorKey).(string); ok && v != "" {
		return v
	}
	return "anonymous"
}

// oidcRuntime is one resolved, live OIDC configuration: the settings that
// produced it plus the discovered provider and the oauth2 client built from
// them. It is immutable once built and swapped wholesale (see Handler.rt), so
// a request that reads it mid-reload sees a consistent set of values rather
// than a half-updated struct.
type oidcRuntime struct {
	settings Settings
	provider *oidc.Provider
	oauth2   *oauth2.Config
}

// settingsRefreshInterval bounds how stale a replica's view of the
// UI-managed oidc_settings row may be. Shepherd runs more than one replica,
// and the admin API can only reload the process it happened to land on, so
// the other replicas learn about a change by re-reading on the auth paths.
// Thirty seconds keeps "I saved it and it did not work" out of the failure
// modes without turning every login into a database round trip.
const settingsRefreshInterval = 30 * time.Second

// settingsRefreshTimeout bounds one background refresh. reloadMu is held for
// the whole of Reload, and the routes that trigger a refresh are
// unauthenticated, so this is the ceiling on how long one slow identity
// provider can make an anonymous request hold that lock.
const settingsRefreshTimeout = 15 * time.Second

// Handler manages OIDC login/callback/logout flows and session middleware.
//
// The OIDC half is late-bound: its configuration can come from the Helm chart
// (config.OIDCConfig) or, when the chart supplies no issuer, from the
// oidc_settings row an app admin writes through the UI. The second source can
// change while the process is running, so the provider and oauth2 client live
// behind an atomic pointer that Reload swaps rather than in fields fixed at
// construction. A nil runtime means "OIDC is not configured", which is a
// normal state, not an error — LoginHandler and CallbackHandler say so and
// the login page offers whatever other method is enabled.
type Handler struct {
	store  *store.Store
	cfg    *config.Config
	logger *slog.Logger

	// settings reads the UI-managed row. nil in the local-admin-only wiring
	// (NewLocalAdmin) and in tests that never touch OIDC.
	settings *SettingsStore

	rt atomic.Pointer[oidcRuntime]

	// reloadMu serializes Reload so concurrent requests cannot both run
	// discovery, and lastReload backs off retries after a failure just as it
	// paces them after a success — a provider that is down must not make every
	// /auth/methods call wait out a discovery timeout.
	reloadMu   sync.Mutex
	lastReload time.Time
}

// New creates an auth Handler and resolves the initial OIDC configuration.
//
// When the chart configured an issuer, a discovery failure is fatal: the
// operator asked for OIDC explicitly and a server that silently came up
// without it would be a worse outcome than a crash-loop naming the cause.
// When the issuer comes from the database instead, a discovery failure is
// logged and the handler starts with OIDC off — the deployment may not have
// been configured yet, and refusing to boot would take away the local-admin
// session that is the only way to fix it.
func New(ctx context.Context, cfg *config.Config, st *store.Store, enc *crypto.Encryptor, logger *slog.Logger) (*Handler, error) {
	h := &Handler{store: st, cfg: cfg, logger: logger}
	if st != nil {
		h.settings = NewSettingsStore(st, enc)
	}
	err := h.Reload(ctx)
	if err == nil {
		return h, nil
	}
	if cfg.OIDC.Issuer != "" {
		return nil, err
	}
	logger.Error("stored OIDC settings could not be activated; OIDC sign-in is off until it is fixed in the admin UI", "err", err)
	return h, nil
}

// NewLocalAdmin creates an auth Handler for local-admin-only mode: no OIDC
// runtime and no settings store, so Reload is a no-op and the OIDC handlers
// report "not configured".
func NewLocalAdmin(cfg *config.Config, st *store.Store, logger *slog.Logger) *Handler {
	return &Handler{store: st, cfg: cfg, logger: logger}
}

// Reload re-resolves the effective OIDC configuration, runs discovery against
// it, and swaps the live runtime. It is called at construction, by the admin
// API immediately after a settings write (so the admin who saved sees the
// change at once), and by refreshIfStale on the auth paths (so every other
// replica catches up within settingsRefreshInterval).
//
// Reload is atomic in the way that matters: the existing runtime is left in
// place until the new one is fully built, so a save that turns out not to
// discover does not take a working provider offline.
func (h *Handler) Reload(ctx context.Context) error {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	// Stamped before the attempt, not after, so a failing provider is retried
	// on the same schedule as a healthy one is refreshed.
	h.lastReload = time.Now()

	settings, err := h.resolveSettings(ctx)
	if err != nil {
		return err
	}
	if settings == nil || !settings.Enabled || settings.Issuer == "" {
		h.rt.Store(nil)
		return nil
	}
	if current := h.rt.Load(); current != nil && current.settings.equivalentTo(*settings) {
		// Nothing that affects discovery or the oauth2 client changed. Skipping
		// re-discovery here is what makes the periodic refresh cheap enough to
		// sit on the login path at all.
		return nil
	}
	doc, err := fetchDiscovery(ctx, settings.Issuer)
	if err != nil {
		return err
	}
	// contextcheck: newProviderFromDiscovery deliberately does not take this
	// context. ProviderConfig.NewProvider uses a context only to pick up the
	// HTTP client, never for cancellation, and the provider it returns outlives
	// this request — binding it to a request context would tie the JWKS client
	// to a scope that has already ended.
	provider, err := newProviderFromDiscovery(settings.Issuer, doc) //nolint:contextcheck // provider outlives the request; ctx would only supply the client
	if err != nil {
		return err
	}
	h.rt.Store(&oidcRuntime{
		settings: *settings,
		provider: provider,
		oauth2: &oauth2.Config{
			ClientID:     settings.ClientID,
			ClientSecret: settings.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  settings.RedirectURL,
			Scopes:       settings.Scopes,
		},
	})
	h.logger.Info("OIDC provider active", "settings", settings)
	return nil
}

// resolveSettings applies the precedence rule: chart config wins whenever it
// names an issuer, and the UI-managed row is consulted only when it does not.
// Returns (nil, nil) when neither source is configured.
func (h *Handler) resolveSettings(ctx context.Context) (*Settings, error) {
	if h.cfg != nil && h.cfg.OIDC.Issuer != "" {
		return settingsFromConfig(h.cfg), nil
	}
	if h.settings == nil {
		return nil, nil //nolint:nilnil // "no settings, no error" is the normal unconfigured state
	}
	s, err := h.settings.Get(ctx)
	if errors.Is(err, ErrNoSettings) {
		return nil, nil //nolint:nilnil // as above
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// refreshIfStale re-reads the UI-managed settings when this replica's copy is
// older than settingsRefreshInterval. It is a no-op for chart-configured
// deployments, whose settings cannot change without a restart anyway.
func (h *Handler) refreshIfStale(ctx context.Context) {
	if h.settings == nil || (h.cfg != nil && h.cfg.OIDC.Issuer != "") {
		return
	}
	h.reloadMu.Lock()
	stale := time.Since(h.lastReload) >= settingsRefreshInterval
	h.reloadMu.Unlock()
	if !stale {
		return
	}
	// Detached from the request, and bounded on its own.
	//
	// Inheriting cancellation would hand an UNAUTHENTICATED caller a way to
	// keep OIDC permanently off: /auth/methods reaches this, Reload stamps
	// lastReload before it attempts anything (so the backoff is spent either
	// way), and a client that opens the request and immediately aborts would
	// cancel the reload before it could install a runtime. Repeat every
	// settingsRefreshInterval and a replica that starts with no runtime never
	// gets one. WithoutCancel severs that; the timeout is what still bounds it.
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settingsRefreshTimeout)
	defer cancel()
	if err := h.Reload(refreshCtx); err != nil {
		h.logger.Warn("refreshing OIDC settings", "err", err)
	}
}

// OIDCEnabled reports whether a login through an identity provider is
// currently possible.
func (h *Handler) OIDCEnabled() bool { return h.rt.Load() != nil }

// OIDCDisplayName is the login button label for the active provider, or "" if
// none is active.
func (h *Handler) OIDCDisplayName() string {
	if rt := h.rt.Load(); rt != nil {
		return rt.settings.DisplayName
	}
	return ""
}

// OIDCProvider is the active provider's preset key, or "" if none is active.
func (h *Handler) OIDCProvider() string {
	if rt := h.rt.Load(); rt != nil {
		return rt.settings.Provider
	}
	return ""
}

// equivalentTo reports whether two settings would produce the same runtime.
// Only the fields discovery and the oauth2 client are built from are compared;
// claim names and the app-admin group list are read per request from the
// stored settings, so a change to one of those still needs a swap — hence
// they are compared here too, and only the audit metadata is ignored.
func (s Settings) equivalentTo(other Settings) bool {
	return s.Enabled == other.Enabled &&
		s.Provider == other.Provider &&
		s.DisplayName == other.DisplayName &&
		s.Issuer == other.Issuer &&
		s.ClientID == other.ClientID &&
		s.ClientSecret == other.ClientSecret &&
		s.RedirectURL == other.RedirectURL &&
		slices.Equal(s.Scopes, other.Scopes) &&
		s.SubjectClaim == other.SubjectClaim &&
		s.EmailClaim == other.EmailClaim &&
		s.NameClaim == other.NameClaim &&
		s.GroupsClaim == other.GroupsClaim &&
		slices.Equal(s.AppAdminGroups, other.AppAdminGroups) &&
		s.UseGraphGroups == other.UseGraphGroups &&
		s.GraphBaseURL == other.GraphBaseURL
}

// oidcUnavailable answers a request that reached an OIDC route while no
// provider is configured. The routes are mounted unconditionally — a
// UI-configured provider can become live without a restart, so they cannot be
// registered based on startup configuration — which makes this the handler
// that has to explain the state.
func (h *Handler) oidcUnavailable(w http.ResponseWriter, r *http.Request) {
	h.logger.Warn("OIDC route reached with no provider configured", "path", r.URL.Path)
	http.Redirect(w, r, "/login?auth_error=oidc_not_configured", http.StatusFound)
}

// LoginHandler redirects to the OIDC provider.
func (h *Handler) LoginHandler(w http.ResponseWriter, r *http.Request) {
	h.refreshIfStale(r.Context())
	rt := h.rt.Load()
	if rt == nil {
		h.oidcUnavailable(w, r)
		return
	}
	state := randomState()
	// Generate PKCE S256 verifier and challenge.
	verifierStr := oauth2.GenerateVerifier()
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure/HttpOnly/SameSite all set
		Name:     "oidc_state",
		Value:    state + "|" + verifierStr,
		MaxAge:   300,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
		Secure:   !h.cfg.Auth.InsecureCookies,
	})
	http.Redirect(w, r, rt.oauth2.AuthCodeURL(state, oauth2.S256ChallengeOption(verifierStr)), http.StatusFound)
}

// CallbackHandler handles the OIDC callback, creates a session, and redirects to /.
func (h *Handler) CallbackHandler(w http.ResponseWriter, r *http.Request) {
	// Refreshed here as well as in LoginHandler: with more than one replica,
	// the callback can land on a process that has never served /auth/login and
	// so has never had a reason to load the settings.
	h.refreshIfStale(r.Context())
	rt := h.rt.Load()
	if rt == nil {
		h.oidcUnavailable(w, r)
		return
	}
	stateCookie, err := r.Cookie("oidc_state")
	if err != nil {
		http.Error(w, "missing state cookie", http.StatusBadRequest)
		return
	}

	// Split state and PKCE verifier stored in the cookie.
	parts := strings.SplitN(stateCookie.Value, "|", 2)
	if len(parts) != 2 || parts[0] != r.URL.Query().Get("state") {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}
	verifierStr := parts[1]

	http.SetCookie(w, &http.Cookie{Name: "oidc_state", MaxAge: -1, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: !h.cfg.Auth.InsecureCookies}) //nolint:gosec // G124: all attributes set

	token, err := rt.oauth2.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(verifierStr))
	if err != nil {
		h.logger.Error("OIDC token exchange", "err", err)
		http.Redirect(w, r, "/?auth_error=1", http.StatusFound)
		return
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		http.Redirect(w, r, "/?auth_error=1", http.StatusFound)
		return
	}

	verifier := rt.provider.Verifier(&oidc.Config{ClientID: rt.settings.ClientID})
	idToken, err := verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		h.logger.Error("OIDC ID token verify", "err", err)
		http.Redirect(w, r, "/?auth_error=1", http.StatusFound)
		return
	}

	// Claims are decoded into a map rather than a fixed struct because which
	// claim carries the subject, the email, the display name, and the group
	// list is now per-provider configuration (Settings.*Claim) — a struct's
	// json tags are fixed at compile time and could only ever describe one
	// provider, which is exactly the limitation this feature removes.
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		h.logger.Error("OIDC ID token claims", "err", err)
		http.Redirect(w, r, "/?auth_error=1", http.StatusFound)
		return
	}

	subject := claimString(claims, rt.settings.SubjectClaim)
	if subject == "" {
		// idToken.Subject is the spec-mandated "sub", which every compliant
		// provider emits. Falling back to it means a mistyped subject-claim
		// setting degrades to a working (if differently-keyed) identity instead
		// of writing a session with an empty user_oid that matches nothing.
		subject = idToken.Subject
	}
	if subject == "" {
		h.logger.Error("OIDC ID token carried no subject", "subject_claim", rt.settings.SubjectClaim)
		http.Redirect(w, r, "/?auth_error=1", http.StatusFound)
		return
	}
	email := claimString(claims, rt.settings.EmailClaim)
	if email == "" {
		// Widely emitted when "email" is absent (Entra without the email claim
		// mapped, Keycloak with the email scope withheld). The session's actor
		// string is built from this and lands in the audit log, so an empty
		// value would make every entry anonymous.
		email = claimString(claims, "preferred_username")
	}
	displayName := claimString(claims, rt.settings.NameClaim)

	accessToken, ok := token.Extra("access_token").(string)
	if !ok {
		accessToken = token.AccessToken
	}
	groupIDs := h.resolveGroups(r.Context(), rt.settings, claims, accessToken, subject)

	isAppAdmin := slices.ContainsFunc(groupIDs, func(g string) bool {
		return slices.Contains(rt.settings.AppAdminGroups, g)
	})

	expires := time.Now().Add(h.cfg.Auth.SessionTTL)
	if err := h.createSessionAndSetCookie(w, r, sessionParams{userOID: subject, email: email, displayName: displayName, groupIDs: groupIDs, isAppAdmin: isAppAdmin, idTokenExp: &idToken.Expiry, expiresAt: expires, source: "oidc"}); err != nil {
		h.logger.Error("OIDC session creation", "err", err)
		http.Redirect(w, r, "/?auth_error=1", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// resolveGroups produces the group list a session is authorized from.
//
// With the Microsoft Graph lookup on (Entra), the directory is authoritative:
// it is the only source that stays correct past the ~200-group point where
// Entra drops the claim from the token entirely. When the lookup fails the
// claim is used as a fallback rather than treating the user as group-less —
// a transient Graph outage should not silently demote every administrator,
// and the claim, when present, is signed by the same provider.
func (h *Handler) resolveGroups(ctx context.Context, s Settings, claims map[string]any, accessToken, subject string) []string {
	claimGroups := claimStrings(claims, s.GroupsClaim)
	if !s.UseGraphGroups {
		return claimGroups
	}
	groups, err := graph.TransitiveMemberOf(ctx, s.GraphBaseURL, accessToken)
	if err == nil {
		return groups
	}
	h.logger.Warn("fetching groups from Microsoft Graph; falling back to the ID token groups claim",
		"err", err, "subject", subject, "claim_groups", len(claimGroups))
	return claimGroups
}

// claimString reads a string claim, tolerating the numeric and boolean values
// a provider may emit for a claim an admin pointed at by mistake — rendering
// them rather than returning empty makes the misconfiguration visible in the
// UI instead of silently blank.
func claimString(claims map[string]any, key string) string {
	if key == "" {
		return ""
	}
	switch v := claims[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		return ""
	}
}

// claimStrings reads a group-list claim. Providers disagree on the shape:
// most emit a JSON array, some emit a single string when there is exactly one
// value, and a few emit a space-separated list. All three are accepted,
// because the alternative is an admin seeing an empty group list with no
// error and no way to tell which of the three their provider chose.
func claimStrings(claims map[string]any, key string) []string {
	if key == "" {
		return nil
	}
	switch v := claims[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return slices.Clone(v)
	case string:
		return strings.Fields(v)
	default:
		return nil
	}
}

type sessionParams struct {
	userOID     string
	email       string
	displayName string
	groupIDs    []string
	isAppAdmin  bool
	idTokenExp  *time.Time
	expiresAt   time.Time
	source      string
}

func (h *Handler) createSessionAndSetCookie(w http.ResponseWriter, r *http.Request, p sessionParams) error {
	if !p.expiresAt.After(time.Now()) {
		return fmt.Errorf("session expiry must be in the future")
	}
	groupsJSON, err := json.Marshal(p.groupIDs)
	if err != nil {
		return fmt.Errorf("marshaling groups: %w", err)
	}
	idTokenExpires := pgtype.Timestamptz{}
	if p.idTokenExp != nil {
		idTokenExpires = pgtype.Timestamptz{Time: *p.idTokenExp, Valid: true}
	}
	sessionID := randomState()
	if _, err := h.store.Queries.CreateSession(r.Context(), sqlc.CreateSessionParams{ID: sessionID, UserOid: p.userOID, Email: p.email, DisplayName: p.displayName, GroupIds: groupsJSON, IsAppAdmin: p.isAppAdmin, IDTokenExpires: idTokenExpires, ExpiresAt: pgtype.Timestamptz{Time: p.expiresAt, Valid: true}, Source: p.source}); err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	http.SetCookie(w, sessionCookie(sessionID, int(time.Until(p.expiresAt).Seconds()), h.cfg.Auth.InsecureCookies))
	return nil
}

// MethodsHandler returns the authentication methods the login page should
// offer, including the label to put on the OIDC button.
//
// It reports whether OIDC is LIVE (a discovered provider is loaded), not
// whether one is configured somewhere: a saved-but-undiscoverable provider
// would otherwise render a sign-in button that can only dead-end.
//
// The response is deliberately not cached any more. It used to carry
// Cache-Control: max-age=60, which was free when the answer could not change
// without a restart; now an admin can turn OIDC on and the very next thing
// they do is reload the login page to check.
func (h *Handler) MethodsHandler(w http.ResponseWriter, r *http.Request) {
	h.refreshIfStale(r.Context())
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	resp := map[string]any{
		"oidc":              h.OIDCEnabled(),
		"local_admin":       h.cfg.Auth.LocalAdmin.Enabled,
		"oidc_display_name": h.OIDCDisplayName(),
		"oidc_provider":     h.OIDCProvider(),
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.logger.Warn("writing auth methods response", "err", err)
	}
}

// LocalLoginHandler handles local admin login.
func (h *Handler) LocalLoginHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAuthError(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}
	cfg := h.cfg.Auth.LocalAdmin
	pwdMatch, verifyErr := VerifyPassword(cfg.PasswordHash, req.Password)
	if verifyErr != nil {
		pwdMatch = false
	}
	if req.Username != cfg.Username || !pwdMatch {
		writeAuthError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	if err := h.createSessionAndSetCookie(w, r, sessionParams{userOID: "local:" + cfg.Username, displayName: cfg.Username, groupIDs: []string{}, isAppAdmin: true, expiresAt: time.Now().Add(cfg.SessionTTL), source: "local"}); err != nil {
		writeAuthError(w, http.StatusInternalServerError, "internal", "failed to create session")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"ok":true}`) //nolint:errcheck
}

// LogoutHandler deletes the session and clears the cookie.
func (h *Handler) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("shepherd_session"); err == nil {
		if delErr := h.store.Queries.DeleteSession(r.Context(), c.Value); delErr != nil {
			h.logger.Warn("logout: delete session", "err", delErr)
		}
	}
	http.SetCookie(w, sessionCookie("", -1, h.cfg.Auth.InsecureCookies))
	http.Redirect(w, r, "/login", http.StatusFound)
}

// SessionMiddleware loads the session from the cookie and stores it in context.
// API routes use RequireAuth (below) to gate access; this middleware just enriches context.
func (h *Handler) SessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("shepherd_session")
		if err == nil && c.Value != "" {
			row, err := h.store.Queries.GetSessionByID(r.Context(), c.Value)
			if err == nil {
				var groups []string
				if err := json.Unmarshal(row.GroupIds, &groups); err != nil {
					h.logger.Debug("unmarshal group ids", "err", err)
				}
				sess := &Session{
					ID:          row.ID,
					UserOID:     row.UserOid,
					Email:       row.Email,
					DisplayName: row.DisplayName,
					GroupIDs:    groups,
					IsAppAdmin:  row.IsAppAdmin,
					Source:      row.Source,
				}
				r = r.WithContext(context.WithValue(r.Context(), sessionKey, sess))
				actor := sess.Email
				if sess.Source == "local" {
					actor = sess.UserOID
				}
				r = r.WithContext(SetActor(r.Context(), actor)) //nolint:contextcheck // SetActor preserves the request context
			}
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAuth returns 401 if there is no valid session.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if SessionFromCtx(r.Context()) == nil {
			writeAuthError(w, http.StatusUnauthorized, "unauthenticated", "not authenticated")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAppAdmin returns 401 if there is no session, or 403 if the session
// user is not an app admin. The role decision itself lives in Authorize
// (authz.go) so it can be reused by the Connect authz interceptor.
func RequireAppAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := SessionFromCtx(r.Context())
		switch err := Authorize(r.Context(), nil, sess, "", RoleAppAdmin); {
		case err == nil:
			next.ServeHTTP(w, r)
		case errors.Is(err, ErrUnauthenticated):
			writeAuthError(w, http.StatusUnauthorized, "unauthenticated", "not authenticated")
		default:
			writeAuthError(w, http.StatusForbidden, "forbidden", "requires app admin role")
		}
	})
}

// RequireOrgAccess verifies the user is an org admin or reader for the org.
// The org is looked up by the {org} URL param. The caller passes minRole: "reader" or "orgadmin".
// The role decision itself lives in Authorize (authz.go) so it can be reused
// by the Connect authz interceptor; this middleware only translates the
// decision into the exact HTTP responses this endpoint has always returned.
func RequireOrgAccess(st *store.Store, orgIDParam, minRole string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess := SessionFromCtx(r.Context())

			// Parse org ID from URL param.
			orgIDStr := r.PathValue(orgIDParam)
			if orgIDStr == "" {
				// ?org= fallback for handlers registered without an {org} path
				// param. Every production route lives under /orgs/{org} (chi
				// populates r.PathValue), so this is defensive, not load-bearing.
				orgIDStr = r.URL.Query().Get("org")
			}

			role := RoleOrgReader
			if minRole == "orgadmin" {
				role = RoleOrgAdmin
			}

			switch err := Authorize(r.Context(), st, sess, orgIDStr, role); {
			case err == nil:
				next.ServeHTTP(w, r)
			case errors.Is(err, ErrUnauthenticated):
				w.WriteHeader(http.StatusUnauthorized)
			case errors.Is(err, ErrInvalidOrgID):
				w.WriteHeader(http.StatusForbidden)
			case errors.Is(err, ErrOrgNotFound):
				w.WriteHeader(http.StatusNotFound)
			case minRole == "orgadmin":
				writeAuthError(w, http.StatusForbidden, "forbidden", "requires org admin role")
			default:
				writeAuthError(w, http.StatusForbidden, "forbidden", "no access to this org")
			}
		})
	}
}

func randomState() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)
}

// writeAuthError writes a JSON error response to w. It is safe to call after WriteHeader.
func writeAuthError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":{"code":%q,"message":%q}}`, code, message) //nolint:errcheck // header already sent
}

// CSRFMiddleware rejects mutating requests that lack the X-Requested-With: XMLHttpRequest header.
// This is sufficient CSRF protection when combined with SameSite=Lax cookies (spec §7.1).
func CSRFMiddleware(next http.Handler) http.Handler {
	safeMethods := map[string]bool{"GET": true, "HEAD": true, "OPTIONS": true, "TRACE": true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !safeMethods[r.Method] && r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			writeAuthError(w, http.StatusForbidden, "csrf_required", "X-Requested-With: XMLHttpRequest header required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
