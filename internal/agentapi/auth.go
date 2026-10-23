// Package agentapi implements the collector.v1 Connect RPC service.
package agentapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/auth"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

type contextKey int

const principalKey contextKey = iota

// AuthKind is how a collector authenticated. A token carries its own org
// binding (migration 0038: an org plus cluster/role allowlists); an OIDC
// principal carries claims that are resolved to a binding per request (agent
// OIDC, docs/archive/plans/2026-09-16-agent-oidc-auth.md).
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

	// Binding is an AuthKindToken principal's org binding, read with the
	// token row by the gate — whole, in one value, so a caller that caches
	// the principal caches what it may act for with it. An invalid OrgID is
	// an unbound token.
	Binding Binding
}

// Binding is what a bound principal may act for: an org, optional
// cluster/role allowlists (jsonb string arrays, empty = any), and whether it
// may claim an unclaimed cluster for that org. An agent token row and an OIDC
// agent_identities row both carry one, and both resolve through
// Service.resolveBoundOrg.
type Binding struct {
	OrgID    pgtype.UUID
	Clusters json.RawMessage
	Roles    json.RawMessage
	// MayClaim: an unclaimed cluster may be claimed for OrgID on a poll.
	// True for an OIDC binding and for a token an app admin minted or bound
	// (agent_tokens.may_claim); a token an org admin or a service account
	// minted serves only clusters OrgID has already claimed.
	MayClaim bool
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
		tok, err := verifyBasicAuth(ctx, authz, st)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnauthenticated, err)
		}
		return context.WithValue(ctx, principalKey, Principal{
			Kind:    AuthKindToken,
			TokenID: tok.ID.String(),
			Binding: Binding{OrgID: tok.OrgID, Clusters: tok.Clusters, Roles: tok.Roles, MayClaim: tok.MayClaim},
		}), nil
	}
}

// verifyBasicAuth parses Authorization: Basic <base64(uuid:secret)> and
// validates the secret against the stored sha256 hash using constant-time
// compare. It returns the live token row: this is authentication only —
// what the token may act for is its org binding, resolved by resolveOrg.
func verifyBasicAuth(ctx context.Context, authHeader string, st *store.Store) (sqlc.AgentToken, error) {
	const prefix = "Basic "
	if !strings.HasPrefix(authHeader, prefix) {
		return sqlc.AgentToken{}, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(authHeader, prefix))
	if err != nil {
		return sqlc.AgentToken{}, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return sqlc.AgentToken{}, connect.NewError(connect.CodeUnauthenticated, nil)
	}
	tokenIDStr, secret := parts[0], parts[1]

	var tokenID pgtype.UUID
	if err := tokenID.Scan(tokenIDStr); err != nil {
		return sqlc.AgentToken{}, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	token, err := st.Queries.GetAgentTokenByID(ctx, tokenID)
	if err != nil {
		return sqlc.AgentToken{}, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	// Compare sha256(provided_secret) with stored hash using constant-time compare.
	hash := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(hash[:], token.TokenHash) != 1 {
		return sqlc.AgentToken{}, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	return token, nil
}
