package auth

import (
	"crypto/sha256"
	"encoding/hex"
)

// NewSessionToken mints the value a shepherd_session cookie carries: 24 bytes
// from crypto/rand, base64url-encoded. The token itself is never stored; the
// sessions table keys each row by HashSessionToken(token).
func NewSessionToken() string {
	return randomState()
}

// HashSessionToken returns the sessions.id a cookie value maps to: the
// lowercase hex SHA-256 of the token. Storing only the digest means a reader
// of the sessions table cannot turn a row back into a working cookie, the
// same way agent and service-account tokens are kept. A plain hash (no salt,
// no key) suffices because the token carries 192 random bits.
func HashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
