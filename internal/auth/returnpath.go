package auth

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
)

// OIDCReturnCookie remembers, across the round trip to the identity provider,
// the page a signed-out user originally asked for (#250). LoginHandler writes
// it from ?next=; CallbackHandler reads it, re-validates it and redirects
// there instead of to "/".
//
// It is a cookie of its own rather than a fourth field of oidc_state because
// a path can contain the "|" that cookie is split on.
const OIDCReturnCookie = "oidc_return"

// returnCookieMaxAge matches oidc_state: a sign-in that takes longer than the
// state cookie lives fails anyway.
const returnCookieMaxAge = 300

// maxReturnPathLen bounds what is stored in a cookie. A real SPA path with its
// query string is far shorter; anything longer is refused, not truncated.
const maxReturnPathLen = 1024

// maxReturnPathDecodes is how many layers of percent-encoding are peeled off
// when looking for a hidden "//" or backslash. A value still encoded after
// that many rounds is refused rather than trusted.
const maxReturnPathDecodes = 3

// SafeReturnPath returns raw when it is a same-origin relative path that is
// safe to redirect to after sign-in, and "" otherwise. The caller falls back
// to "/" on "".
//
// The rules are deliberately stricter than "url.Parse found no host", because
// browsers are more lenient than Go's parser: they treat "\" as "/", and strip
// tabs and newlines before resolving, so "/\evil.example" and "/\t/evil.example"
// both become the protocol-relative "//evil.example". Each percent-decoded
// layer is checked as well, because the SPA router decodes the path before it
// navigates.
//
// The login page and the server's own /auth routes are refused too: sending a
// freshly signed-in user back to /login or to /auth/logout is a loop, not a
// return.
//
// web/src/lib/returnPath.ts is the client-side twin of this function; keep the
// two in step.
func SafeReturnPath(raw string) string {
	if raw == "" || len(raw) > maxReturnPathLen {
		return ""
	}
	candidate := raw
	for i := 0; ; i++ {
		if !returnPathShapeOK(candidate) {
			return ""
		}
		if !strings.Contains(candidate, "%") {
			break
		}
		if i == maxReturnPathDecodes {
			return ""
		}
		decoded, err := url.PathUnescape(candidate)
		if err != nil {
			return ""
		}
		candidate = decoded
	}

	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return ""
	}
	if u.Path == "/login" || strings.HasPrefix(u.Path, "/login/") ||
		u.Path == "/auth" || strings.HasPrefix(u.Path, "/auth/") {
		return ""
	}
	return raw
}

// returnPathShapeOK is the layer-independent part of SafeReturnPath: one
// leading slash, never two, no backslash anywhere and no control character.
func returnPathShapeOK(p string) bool {
	// A single leading "/" whose next character is neither "/" nor "\": the
	// form CodeQL's go/bad-redirect-check recognises, since a browser reads
	// both "//host" and "/\host" as protocol-relative. The loop below also
	// refuses a backslash anywhere else.
	if len(p) == 0 || p[0] != '/' {
		return false
	}
	if len(p) > 1 && (p[1] == '/' || p[1] == '\\') {
		return false
	}
	for i := 0; i < len(p); i++ {
		if c := p[i]; c == '\\' || c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

// encodeReturnCookie base64url-encodes a path for the cookie: a path's "?",
// "=", ";" and spaces are not all legal in a cookie value, and net/http would
// silently drop the ones that are not.
func encodeReturnCookie(p string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(p))
}

// setReturnCookie remembers next for the callback, or clears a stale value
// when next is not a safe return path — a previous, abandoned sign-in must
// not decide where this one lands.
func (h *Handler) setReturnCookie(w http.ResponseWriter, next string) {
	c := &http.Cookie{ //nolint:gosec // G124: Secure/HttpOnly/SameSite all set
		Name:     OIDCReturnCookie,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
		Secure:   !h.cfg.Auth.InsecureCookies,
	}
	if safe := SafeReturnPath(next); safe != "" {
		c.Value = encodeReturnCookie(safe)
		c.MaxAge = returnCookieMaxAge
	} else {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

// clearReturnCookie removes the remembered return path once the callback has
// consumed it, whatever the outcome of the sign-in.
func (h *Handler) clearReturnCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: OIDCReturnCookie, MaxAge: -1, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: !h.cfg.Auth.InsecureCookies}) //nolint:gosec // G124: all attributes set
}

// oidcReturnPath is where CallbackHandler sends a user it has just signed in:
// the remembered path when there is one and it still passes SafeReturnPath,
// "/" otherwise. The cookie is client-controlled — a sibling subdomain can
// toss one — so it is validated again here, not trusted because
// LoginHandler validated what it wrote.
func oidcReturnPath(r *http.Request) string {
	c, err := r.Cookie(OIDCReturnCookie)
	if err != nil || c.Value == "" {
		return "/"
	}
	decoded, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return "/"
	}
	if safe := SafeReturnPath(string(decoded)); safe != "" {
		return safe
	}
	return "/"
}
