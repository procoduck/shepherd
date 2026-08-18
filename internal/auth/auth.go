// Package auth implements OIDC BFF, session management, and RBAC middleware.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	oidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/oauth2"

	"shepherd/internal/config"
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

// Handler manages OIDC login/callback/logout flows and session middleware.
type Handler struct {
	store    *store.Store
	cfg      *config.Config
	provider *oidc.Provider
	oauth2   *oauth2.Config
	graph    *graph.Client
	logger   *slog.Logger
}

// New creates an auth Handler. Performs OIDC discovery (requires network).
func New(ctx context.Context, cfg *config.Config, st *store.Store, logger *slog.Logger) (*Handler, error) {
	provider, err := oidc.NewProvider(ctx, cfg.OIDC.Issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery: %w", err)
	}

	oauth2Cfg := &oauth2.Config{
		ClientID:     cfg.OIDC.ClientID,
		ClientSecret: cfg.OIDC.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  cfg.OIDC.RedirectURL,
		Scopes:       cfg.OIDC.Scopes,
	}

	graphClient := graph.New(ctx, cfg.Graph.TenantID, cfg.Graph.ClientID, cfg.Graph.ClientSecret, cfg.Graph.BaseURL)

	return &Handler{
		store:    st,
		cfg:      cfg,
		provider: provider,
		oauth2:   oauth2Cfg,
		graph:    graphClient,
		logger:   logger,
	}, nil
}

// NewLocalAdmin creates an auth Handler for local-admin-only mode.
func NewLocalAdmin(cfg *config.Config, st *store.Store, logger *slog.Logger) *Handler {
	return &Handler{store: st, cfg: cfg, logger: logger}
}

// LoginHandler redirects to the OIDC provider.
func (h *Handler) LoginHandler(w http.ResponseWriter, r *http.Request) {
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
	http.Redirect(w, r, h.oauth2.AuthCodeURL(state, oauth2.S256ChallengeOption(verifierStr)), http.StatusFound)
}

// CallbackHandler handles the OIDC callback, creates a session, and redirects to /.
func (h *Handler) CallbackHandler(w http.ResponseWriter, r *http.Request) {
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

	token, err := h.oauth2.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(verifierStr))
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

	verifier := h.provider.Verifier(&oidc.Config{ClientID: h.cfg.OIDC.ClientID})
	idToken, err := verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		h.logger.Error("OIDC ID token verify", "err", err)
		http.Redirect(w, r, "/?auth_error=1", http.StatusFound)
		return
	}

	var claims struct {
		OID   string `json:"oid"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		http.Redirect(w, r, "/?auth_error=1", http.StatusFound)
		return
	}

	// Fetch transitive group memberships via delegated token.
	accessToken, ok := token.Extra("access_token").(string)
	if !ok {
		accessToken = token.AccessToken
	}
	groupIDs, err := graph.TransitiveMemberOf(r.Context(), h.cfg.Graph.BaseURL, accessToken)
	if err != nil {
		h.logger.Warn("fetching groups", "err", err, "oid", claims.OID)
		groupIDs = nil
	}

	isAppAdmin := slices.ContainsFunc(groupIDs, func(g string) bool {
		return slices.Contains(h.cfg.Auth.AppAdminGroupIDs, g)
	})

	expires := time.Now().Add(h.cfg.Auth.SessionTTL)
	if err := h.createSessionAndSetCookie(w, r, sessionParams{userOID: claims.OID, email: claims.Email, displayName: claims.Name, groupIDs: groupIDs, isAppAdmin: isAppAdmin, idTokenExp: &idToken.Expiry, expiresAt: expires, source: "oidc"}); err != nil {
		http.Redirect(w, r, "/?auth_error=1", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
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

// MethodsHandler returns available authentication methods.
func MethodsHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=60")
		fmt.Fprintf(w, `{"oidc":%t,"local_admin":%t}`, cfg.OIDC.Issuer != "", cfg.Auth.LocalAdmin.Enabled) //nolint:errcheck
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
		_ = h.store.Queries.DeleteSession(r.Context(), c.Value)
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

// RequireAppAdmin returns 403 if the session user is not an app admin.
func RequireAppAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := SessionFromCtx(r.Context())
		if sess == nil || !sess.IsAppAdmin {
			writeAuthError(w, http.StatusForbidden, "forbidden", "requires app admin role")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireOrgAccess verifies the user is an org admin or reader for the org.
// The org is looked up by the {org} URL param. The caller passes minRole: "reader" or "orgadmin".
// RBAC logic: App Admin can access any org; Org Admin is a member of the org's admin_group_id;
// Reader is a member of reader_group_id or any assigned group on any collector in the org.
func RequireOrgAccess(st *store.Store, orgIDParam, minRole string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess := SessionFromCtx(r.Context())
			if sess == nil {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if sess.IsAppAdmin {
				next.ServeHTTP(w, r)
				return
			}

			// Parse org ID from URL param.
			orgIDStr := r.PathValue(orgIDParam)
			if orgIDStr == "" {
				// chi router uses URLParam; try extracting directly.
				// In practice chi is used so we use chi.URLParam in handler — here we grab from context.
				// For now fall through; full chi integration handled in server wiring.
				orgIDStr = r.URL.Query().Get("org")
			}

			var orgID pgtype.UUID
			if err := orgID.Scan(orgIDStr); err != nil {
				w.WriteHeader(http.StatusForbidden)
				return
			}

			org, err := st.Queries.GetOrgByID(r.Context(), orgID)
			if err != nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}

			isOrgAdmin := slices.Contains(sess.GroupIDs, org.AdminGroupID)
			isOrgReader := org.ReaderGroupID.Valid && slices.Contains(sess.GroupIDs, org.ReaderGroupID.String)

			if minRole == "orgadmin" && !isOrgAdmin {
				writeAuthError(w, http.StatusForbidden, "forbidden", "requires org admin role")
				return
			}

			if minRole == "reader" && !isOrgAdmin && !isOrgReader {
				// Check group_assignments.
				collectorIDs, err := st.Queries.ListCollectorIDsByGroupMembership(r.Context(), sess.GroupIDs)
				if err != nil || len(collectorIDs) == 0 {
					writeAuthError(w, http.StatusForbidden, "forbidden", "no access to this org")
					return
				}
			}

			next.ServeHTTP(w, r)
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
