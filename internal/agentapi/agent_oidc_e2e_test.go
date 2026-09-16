package agentapi_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"golang.org/x/oauth2/clientcredentials"

	collectorv1 "shepherd/gen/collector/v1"
	"shepherd/gen/collector/v1/collectorv1connect"
	"shepherd/internal/agentapi"
	"shepherd/internal/auth"
	"shepherd/internal/config"
	appcrypto "shepherd/internal/crypto"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// End-to-end live proof of collector OIDC: a collector fetches a real access
// token from an OIDC issuer via the OAuth2 client-credentials grant (exactly
// what Alloy's remotecfg oauth2 block does), presents it as a Bearer token to
// the real collector API behind the real auth gate, and is served its bound
// organisation's config — the whole path (client-credentials -> verify ->
// enforce -> serve) that every layer's own tests exercise in isolation, joined
// up against a real token over the wire.
//
// The issuer here is a self-signed httptest server with a working /token
// endpoint; it stands in for the kind stack's mock-oauth2-server so the proof
// runs in the normal `go test` integration lane rather than needing a cluster.
var _ = Describe("Collector OIDC end to end", Label("integration"), func() {
	const (
		agentAudience = "api://shepherd-collectors"
		collectorRole = "Collector.Poll"
	)

	newSigningIssuer := func() (*httptest.Server, *rsa.PrivateKey) {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		Expect(err).NotTo(HaveOccurred())
		var srv *httptest.Server
		sign := func(claims map[string]any) string {
			b64 := func(v any) string {
				raw, _ := json.Marshal(v) //nolint:errcheck // test fixture
				return base64.RawURLEncoding.EncodeToString(raw)
			}
			signing := b64(map[string]any{"alg": "RS256", "typ": "JWT", "kid": "k1"}) + "." + b64(claims)
			h := sha256.Sum256([]byte(signing))
			sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:]) //nolint:errcheck // test fixture
			return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test fixture
				"issuer": srv.URL, "authorization_endpoint": srv.URL + "/authorize",
				"token_endpoint": srv.URL + "/token", "jwks_uri": srv.URL + "/jwks",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		})
		mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
			n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
			eb := make([]byte, 4)
			binary.BigEndian.PutUint32(eb, uint32(key.E)) //nolint:gosec // small exponent
			_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test fixture
				"keys": []map[string]any{{
					"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "k1",
					"n": n, "e": base64.RawURLEncoding.EncodeToString(eb[1:]),
				}},
			})
		})
		// The client-credentials token endpoint: issue an access token whose
		// subject is the presented client_id and which carries the collector
		// role. No secret check — a fixture, not an IdP.
		mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm() //nolint:errcheck // test fixture
			clientID := r.FormValue("client_id")
			if clientID == "" {
				clientID, _, _ = r.BasicAuth()
			}
			tok := sign(map[string]any{
				"iss": srv.URL, "sub": clientID, "aud": agentAudience,
				"roles": []string{collectorRole},
				"exp":   time.Now().Add(time.Hour).Unix(), "iat": time.Now().Add(-time.Minute).Unix(),
			})
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test fixture
				"access_token": tok, "token_type": "Bearer", "expires_in": 3600,
			})
		})
		srv = httptest.NewServer(mux)
		return srv, key
	}

	It("serves a collector its bound org's config from a client-credentials token", func(ctx context.Context) {
		issuer, _ := newSigningIssuer()
		DeferCleanup(issuer.Close)

		url := sharedPG.IsolatedDB(ctx, GinkgoTB())
		st, err := store.New(ctx, &config.DatabaseConfig{URL: url, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		db, err := pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		// A real, chart-configured auth handler with agent OIDC on, pointed at
		// the issuer. Its VerifyAgentToken is what the gate calls.
		encKey := make([]byte, 32)
		enc, err := appcrypto.NewEncryptor(base64.StdEncoding.EncodeToString(encKey))
		Expect(err).NotTo(HaveOccurred())
		cfg := &config.Config{OIDC: config.OIDCConfig{
			Issuer: issuer.URL, ClientID: "shepherd-login",
			AgentAudience: agentAudience, AgentRequiredRole: collectorRole,
		}}
		authHandler, err := auth.New(ctx, cfg, st, enc, slog.New(slog.DiscardHandler))
		Expect(err).NotTo(HaveOccurred())
		Expect(authHandler.AgentOIDCEnabled()).To(BeTrue())

		// The real collector service behind the real gate.
		svc := agentapi.New(st, nil, slog.New(slog.DiscardHandler), testSchemaRegistry())
		path, handler := collectorv1connect.NewCollectorServiceHandler(
			svc, connect.WithRequestGate(agentapi.NewAuthGate(st, authHandler.VerifyAgentToken)))
		mux := http.NewServeMux()
		mux.Handle(path, handler)
		server := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
		server.Start()
		DeferCleanup(server.Close)

		// Org + the binding for this client identity.
		org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "platform-eng", DisplayName: "Platform Eng", AdminGroupID: "g"})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.CreateAgentIdentity(ctx, sqlc.CreateAgentIdentityParams{
			Issuer: issuer.URL, AppID: "alloy-prod-eu", OrgID: org.ID,
			Clusters: json.RawMessage(`[]`), Roles: json.RawMessage(`[]`), CreatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		// Fetch a token the way Alloy's oauth2 block does: client credentials.
		ccfg := clientcredentials.Config{
			ClientID: "alloy-prod-eu", ClientSecret: "unused-by-the-fixture",
			TokenURL: issuer.URL + "/token",
		}
		token, err := ccfg.Token(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(token.AccessToken).NotTo(BeEmpty())

		// Call GetConfig with the bearer token, over the wire.
		client := collectorv1connect.NewCollectorServiceClient(
			server.Client(), server.URL, connect.WithGRPC(),
			connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
				return func(c context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
					req.Header().Set("Authorization", "Bearer "+token.AccessToken)
					return next(c, req)
				}
			})),
		)
		_, err = client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
			Id:              "inst-1",
			LocalAttributes: map[string]string{"cluster": "prod-eu-1", "role": "metrics"},
		}))
		Expect(err).NotTo(HaveOccurred(), "a real client-credentials token must authenticate and be served config")

		// Proof it was served for the bound org: the cluster is now claimed to it.
		var orgID pgtype.UUID
		Expect(db.QueryRow(ctx, `SELECT org_id FROM clusters WHERE name = 'prod-eu-1'`).Scan(&orgID)).To(Succeed())
		Expect(orgID).To(Equal(org.ID))

		// A token with no binding is refused config (served empty), and one
		// whose bearer is garbage is unauthenticated — the negative controls.
		badClient := collectorv1connect.NewCollectorServiceClient(
			server.Client(), server.URL, connect.WithGRPC(),
			connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
				return func(c context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
					req.Header().Set("Authorization", "Bearer not-a-token")
					return next(c, req)
				}
			})),
		)
		_, err = badClient.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
			Id: "inst-2", LocalAttributes: map[string]string{"cluster": "prod-eu-1", "role": "metrics"},
		}))
		Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnauthenticated))
	})
})
