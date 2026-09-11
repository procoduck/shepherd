// Package agentapi implements the collector.v1 Connect RPC service.
package agentapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/store"
)

type contextKey int

const tokenIDKey contextKey = iota

// NewAuthGate returns a Connect request gate that validates agent token
// Basic auth credentials: username = token UUID, password = 32-byte base64url
// secret. Mount it with connect.WithRequestGate.
//
// A gate rather than an interceptor because the decision needs only the
// headers: it runs before the request body is decompressed or decoded, so a
// caller with a bad token never makes the server unmarshal its payload. The
// authenticated token id is placed in the returned context for the handler.
func NewAuthGate(st *store.Store) connect.RequestGateFunc {
	return func(ctx context.Context, _ connect.Spec, _ connect.Peer, header http.Header) (context.Context, error) {
		tokenID, err := verifyBasicAuth(ctx, header.Get("Authorization"), st)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnauthenticated, err)
		}
		return context.WithValue(ctx, tokenIDKey, tokenID), nil
	}
}

// verifyBasicAuth parses Authorization: Basic <base64(uuid:secret)> and
// validates the secret against the stored sha256 hash using constant-time compare.
func verifyBasicAuth(ctx context.Context, authHeader string, st *store.Store) (string, error) {
	const prefix = "Basic "
	if !strings.HasPrefix(authHeader, prefix) {
		return "", connect.NewError(connect.CodeUnauthenticated, nil)
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(authHeader, prefix))
	if err != nil {
		return "", connect.NewError(connect.CodeUnauthenticated, nil)
	}

	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return "", connect.NewError(connect.CodeUnauthenticated, nil)
	}
	tokenIDStr, secret := parts[0], parts[1]

	var tokenID pgtype.UUID
	if err := tokenID.Scan(tokenIDStr); err != nil {
		return "", connect.NewError(connect.CodeUnauthenticated, nil)
	}

	token, err := st.Queries.GetAgentTokenByID(ctx, tokenID)
	if err != nil {
		return "", connect.NewError(connect.CodeUnauthenticated, nil)
	}

	// Compare sha256(provided_secret) with stored hash using constant-time compare.
	hash := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(hash[:], token.TokenHash) != 1 {
		return "", connect.NewError(connect.CodeUnauthenticated, nil)
	}

	return tokenIDStr, nil
}
