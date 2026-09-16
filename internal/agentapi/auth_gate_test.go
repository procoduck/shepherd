package agentapi_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	"shepherd/internal/agentapi"
	"shepherd/internal/auth"
)

// The gate branches on the Authorization scheme. The Bearer branch is unit
// tested here with a stub verifier (no store, no DB); the Basic branch and the
// enforcement that follows an OIDC principal are covered by the integration
// specs. verifyBasicAuth rejects a non-Basic header before touching the store,
// so a nil store is safe for these cases.
func TestAuthGateBearerBranch(t *testing.T) {
	verify := func(_ context.Context, raw string) (auth.AgentClaims, error) {
		if raw == "good-token" {
			return auth.AgentClaims{Issuer: "https://idp/", AppID: "client-1", Roles: []string{"Collector.Poll"}}, nil
		}
		return auth.AgentClaims{}, errors.New("bad token")
	}
	call := func(gate connect.RequestGateFunc, authz string) (context.Context, error) {
		return gate(context.Background(), connect.Spec{}, connect.Peer{}, http.Header{"Authorization": {authz}})
	}
	codeOf := func(err error) connect.Code {
		var ce *connect.Error
		if errors.As(err, &ce) {
			return ce.Code()
		}
		return connect.Code(0)
	}

	t.Run("a valid Bearer token yields an OIDC principal", func(t *testing.T) {
		ctx, err := call(agentapi.NewAuthGate(nil, verify), "Bearer good-token")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		p := agentapi.PrincipalFrom(ctx)
		if p.Kind != agentapi.AuthKindOIDC || p.Claims == nil || p.Claims.AppID != "client-1" {
			t.Fatalf("principal not populated from claims: %+v", p)
		}
	})

	t.Run("a rejected Bearer token is unauthenticated", func(t *testing.T) {
		_, err := call(agentapi.NewAuthGate(nil, verify), "Bearer nope")
		if codeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("want unauthenticated, got %v", err)
		}
	})

	t.Run("Bearer with OIDC disabled (nil verifier) is unauthenticated", func(t *testing.T) {
		_, err := call(agentapi.NewAuthGate(nil, nil), "Bearer good-token")
		if codeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("want unauthenticated when the feature is off, got %v", err)
		}
	})

	t.Run("a header with no known scheme is unauthenticated", func(t *testing.T) {
		_, err := call(agentapi.NewAuthGate(nil, verify), "Token abc")
		if codeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("want unauthenticated, got %v", err)
		}
	})
}
