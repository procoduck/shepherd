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

	"shepherd/internal/auth"
	"shepherd/internal/store"
)

type contextKey int

const principalKey contextKey = iota

// AuthKind is how a collector authenticated. A token proves liveness only; an
// OIDC principal additionally carries claims that scope which org, cluster and
// role it may act for (agent OIDC, docs/plans/2026-09-16-agent-oidc-auth.md).
type AuthKind int

const (
	AuthKindToken AuthKind = iota
	AuthKindOIDC
)

// Principal is the authenticated collector, placed in the request context by
// the gate for the service handlers to read.
type Principal struct {
	Kind    AuthKind
	TokenID string            // AuthKindToken
	Claims  *auth.AgentClaims // AuthKindOIDC
}

// PrincipalFrom returns the authenticated principal, or the zero value (an
// AuthKindToken with no id) when none is present — which only happens on a
// path that did not run the gate.
func PrincipalFrom(ctx context.Context) Principal {
	p, _ := ctx.Value(principalKey).(Principal)
	return p
}

// AgentVerifier verifies a collector's OAuth2 access token. It is
// auth.Handler.VerifyAgentToken in production; nil disables the Bearer branch
// (collector OIDC not configured).
type AgentVerifier func(ctx context.Context, raw string) (auth.AgentClaims, error)

// NewAuthGate returns a Connect request gate that authenticates a collector.
// It branches on the Authorization scheme: `Bearer <jwt>` is verified as an
// OIDC access token (when verifyAgent is non-nil), `Basic <base64(uuid:secret)>`
// as an agent token. Mount it with connect.WithRequestGate.
//
// A gate rather than an interceptor because the decision needs only the
// headers: it runs before the request body is decompressed or decoded, so a
// caller with a bad credential never makes the server unmarshal its payload.
// The authenticated Principal is placed in the returned context.
func NewAuthGate(st *store.Store, verifyAgent AgentVerifier) connect.RequestGateFunc {
	return func(ctx context.Context, _ connect.Spec, _ connect.Peer, header http.Header) (context.Context, error) {
		authz := header.Get("Authorization")
		if raw, ok := strings.CutPrefix(authz, "Bearer "); ok {
			// A Bearer header with OIDC not configured is simply unauthenticated
			// — the feature is off, indistinguishable to a caller from a bad token.
			if verifyAgent == nil {
				return nil, connect.NewError(connect.CodeUnauthenticated, nil)
			}
			claims, err := verifyAgent(ctx, raw)
			if err != nil {
				return nil, connect.NewError(connect.CodeUnauthenticated, nil)
			}
			return context.WithValue(ctx, principalKey, Principal{Kind: AuthKindOIDC, Claims: &claims}), nil
		}
		tokenID, err := verifyBasicAuth(ctx, authz, st)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnauthenticated, err)
		}
		return context.WithValue(ctx, principalKey, Principal{Kind: AuthKindToken, TokenID: tokenID}), nil
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
