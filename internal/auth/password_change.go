package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// ChangePasswordPath is the one route a user owing a password change may still
// reach. Referenced by both the handler and the middleware so they cannot
// disagree about which endpoint is exempt.
const ChangePasswordPath = "/api/auth/local/password" //nolint:gosec // G101: a route path, not a credential

// ChangePasswordHandler lets the signed-in local user set a new password.
func (h *Handler) ChangePasswordHandler(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromCtx(r.Context())
	if sess == nil {
		writeAuthError(w, http.StatusUnauthorized, "unauthenticated", "not authenticated")
		return
	}
	if h.users == nil || sess.Source != SourceLocal || !sess.UserID.Valid {
		// An OIDC identity has no password here to change. Saying so is more
		// useful than a generic 403, which would read as "you lack permission".
		writeAuthError(w, http.StatusBadRequest, "not_a_local_user",
			"this account signs in through your identity provider; change the password there")
		return
	}

	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAuthError(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}

	if err := h.users.ChangePassword(r.Context(), sess.UserID, req.CurrentPassword, req.NewPassword, true); err != nil {
		switch {
		case errors.Is(err, ErrWrongPassword):
			writeAuthError(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect")
		case errors.Is(err, ErrPasswordTooShort):
			writeAuthError(w, http.StatusBadRequest, "password_too_short", err.Error())
		default:
			h.logger.Warn("changing password", "err", err, "user_id", sess.UserID.String())
			writeAuthError(w, http.StatusBadRequest, "bad_request", err.Error())
		}
		return
	}

	h.logger.Info("password changed", "user_id", sess.UserID.String(), "actor", ActorFromCtx(r.Context()))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]any{"ok": true}); err != nil {
		h.logger.Warn("writing password change response", "err", err)
	}
}

// RequirePasswordChange blocks a local user who still owes a password change
// from reaching anything except the change endpoint itself.
//
// Without this the must_change_password flag would be a suggestion the SPA is
// free to ignore — and a seeded admin/admin that merely *asks* to be changed is
// how a default credential ends up live in production. The check is on the
// server, so skipping the UI does not skip the requirement.
//
// Deliberately not applied to /auth/* (sign-out must always work) or to the
// change endpoint (that would be a deadlock).
func (h *Handler) RequirePasswordChange(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == ChangePasswordPath || strings.HasPrefix(r.URL.Path, "/auth/") {
			next.ServeHTTP(w, r)
			return
		}
		sess := SessionFromCtx(r.Context())
		if sess != nil && h.users != nil && h.users.MustChangePassword(r.Context(), sess) {
			writeAuthError(w, http.StatusForbidden, "password_change_required",
				"set a new password before continuing")
			return
		}
		next.ServeHTTP(w, r)
	})
}
