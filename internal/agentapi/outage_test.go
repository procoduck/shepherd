package agentapi_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	collectorv1 "shepherd/gen/collector/v1"
	"shepherd/gen/collector/v1/collectorv1connect"
	"shepherd/internal/agentapi"
	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
)

// cuttableDB is a store whose database can be made unreachable mid-spec, the
// way a real outage looks to pgx: once cut, every new connection is dialled at
// a closed local port (connection refused, a *pgconn.ConnectError) and the
// pool's existing connections are dropped. Other specs' databases in the
// shared container are untouched.
type cuttableDB struct {
	pool *pgxpool.Pool
	down atomic.Bool
	// slow, when set, delays the next query whose SQL contains it by
	// slowFor — long enough for a client's Connect-Timeout-Ms to expire
	// mid-request — then clears itself.
	slow    atomic.Pointer[string]
	slowFor time.Duration
}

// slowTracer sleeps in TraceQueryStart, ignoring ctx, so the query then runs
// with whatever deadline the caller's context has left.
type slowTracer struct{ c *cuttableDB }

func (t slowTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if want := t.c.slow.Load(); want != nil && strings.Contains(data.SQL, *want) && t.c.slow.CompareAndSwap(want, nil) {
		time.Sleep(t.c.slowFor)
	}
	return ctx
}

func (slowTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *cuttableDB) slowNext(sqlName string, d time.Duration) {
	c.slowFor = d
	c.slow.Store(&sqlName)
}

func newCuttableDB(ctx context.Context, url string) (*store.Store, *cuttableDB) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	deadPort := uint16(l.Addr().(*net.TCPAddr).Port) //nolint:gosec // a TCP port fits in uint16
	Expect(l.Close()).To(Succeed())

	c := &cuttableDB{}
	cfg, err := pgxpool.ParseConfig(url)
	Expect(err).NotTo(HaveOccurred())
	cfg.MaxConns = 4
	cfg.ConnConfig.Tracer = slowTracer{c: c}
	cfg.BeforeConnect = func(_ context.Context, cc *pgx.ConnConfig) error {
		if c.down.Load() {
			cc.Host, cc.Port, cc.Fallbacks = "127.0.0.1", deadPort, nil
			cc.ConnectTimeout = 2 * time.Second
		}
		return nil
	}
	c.pool, err = pgxpool.NewWithConfig(ctx, cfg)
	Expect(err).NotTo(HaveOccurred())
	return store.NewWithPool(c.pool), c
}

func (c *cuttableDB) cut() {
	c.down.Store(true)
	c.pool.Reset()
}

func (c *cuttableDB) restore() { c.down.Store(false) }

var _ = Describe("Riding out a database outage (#374)", Label("integration"), func() {
	var (
		ctx       context.Context
		cancel    context.CancelFunc
		st        *store.Store // direct, always-up handle for seeding and assertions
		db        *cuttableDB
		verdicts  *agentapi.TokenVerdicts
		svc       *agentapi.Service
		server    *httptest.Server
		tokenID   string
		goodBasic string
		seededOrg pgtype.UUID
		cst       *store.Store // the cuttable store the services use
	)

	clientWith := func(authz string) collectorv1connect.CollectorServiceClient {
		return collectorv1connect.NewCollectorServiceClient(server.Client(), server.URL, connect.WithGRPC(),
			connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
				return func(c context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
					req.Header().Set("Authorization", authz)
					return next(c, req)
				}
			})))
	}
	// mintToken creates a token bound to org (migration 0038); mayClaim is
	// agent_tokens.may_claim (an app admin's token).
	mintToken := func(org pgtype.UUID, mayClaim bool) (id, header string) {
		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		secret := base64.URLEncoding.EncodeToString(raw)
		hash := sha256.Sum256([]byte(secret))
		tok, err := st.Queries.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{
			Name: "outage", TokenHash: hash[:], CreatedBy: "test", OrgID: org, MayClaim: mayClaim,
		})
		Expect(err).NotTo(HaveOccurred())
		return tok.ID.String(), "Basic " + base64.StdEncoding.EncodeToString([]byte(tok.ID.String()+":"+secret))
	}
	newToken := func() (id, header string) { return mintToken(seededOrg, true) }
	attrs := map[string]string{"cluster": "outage-cluster", "role": "metrics"}
	revokeInDB := func(id pgtype.UUID) error {
		_, err := st.Queries.RevokeAgentToken(ctx, sqlc.RevokeAgentTokenParams{ID: id})
		return err
	}
	pollAs := func(c collectorv1connect.CollectorServiceClient, cluster string) (*collectorv1.GetConfigResponse, error) {
		resp, err := c.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
			Id: "outage-instance", LocalAttributes: map[string]string{"cluster": cluster, "role": "metrics"},
		}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	getConfig := func(c collectorv1connect.CollectorServiceClient, hash string) (*collectorv1.GetConfigResponse, error) {
		resp, err := c.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
			Id: "outage-instance", LocalAttributes: attrs, Hash: hash,
		}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	// seedClaimed gives outage-cluster/metrics a real served config, so the
	// fallback is proven to return content and not just the empty config.
	seedClaimed := func(c collectorv1connect.CollectorServiceClient) {
		// The token may claim, so registering claims outage-cluster for seededOrg.
		_, err := c.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{Id: "outage-instance", LocalAttributes: attrs}))
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: seededOrg, Name: "outage-pipeline", Contents: "// outage-pipeline",
			Matchers: json.RawMessage(`["cluster=\"outage-cluster\""]`), Enabled: true, Source: "ui",
			WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())
	}

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		url := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: url, MaxConns: 4})
		Expect(err).NotTo(HaveOccurred())
		cst, db = newCuttableDB(ctx, url)
		org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "outage-org", DisplayName: "Outage", AdminGroupID: "g"})
		Expect(err).NotTo(HaveOccurred())
		seededOrg = org.ID

		verdicts = agentapi.NewTokenVerdicts(time.Minute, agentapi.DefaultTokenVerdictMax)
		svc = agentapi.New(cst, nil, slog.New(slog.DiscardHandler), testSchemaRegistry(), agentapi.WithOutageCache(time.Minute))
		path, handler := agentapi.NewHandler(svc, connect.WithRequestGate(agentapi.NewAuthGate(cst, nil, agentapi.WithTokenVerdicts(verdicts))))
		mux := http.NewServeMux()
		mux.Handle(path, handler)
		server = httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
		server.Start()

		tokenID, goodBasic = newToken()
	})

	AfterEach(func() {
		server.Close()
		db.pool.Close()
		st.Close()
		cancel()
	})

	It("answers Unavailable, not Unauthenticated, when the token lookup cannot reach the database", func() {
		db.cut()
		_, err := getConfig(clientWith(goodBasic), "")
		Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable), "got %v", err)
	})

	It("answers Unavailable, not Internal, when GetConfig/RegisterCollector/UnregisterCollector cannot reach the database after an OIDC principal is verified", func() {
		// OIDC verifies without the DB, so the failure is the handler's.
		ctxOIDC := agentapi.ContextWithOIDCPrincipal(ctx, auth.AgentClaims{Issuer: "https://idp/", Subject: "app-1", AppID: "app-1"})
		db.cut()
		_, err := svc.GetConfig(ctxOIDC, connect.NewRequest(&collectorv1.GetConfigRequest{Id: "i", LocalAttributes: attrs}))
		Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable), "got %v", err)
		_, err = svc.RegisterCollector(ctxOIDC, connect.NewRequest(&collectorv1.RegisterCollectorRequest{Id: "i", LocalAttributes: attrs}))
		Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable), "got %v", err)
		_, err = svc.UnregisterCollector(ctxOIDC, connect.NewRequest(&collectorv1.UnregisterCollectorRequest{Id: "i"}))
		Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable), "got %v", err)
	})

	It("accepts a recently verified token during an outage and serves the last config with the same hash", func() {
		c := clientWith(goodBasic)
		seedClaimed(c)
		before, err := getConfig(c, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(before.Content).To(ContainSubstring("outage-pipeline"))

		db.cut()

		// A restarting collector (no hash yet) gets the full content back…
		during, err := getConfig(c, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(during.NotModified).To(BeFalse())
		Expect(during.Content).To(Equal(before.Content))
		Expect(during.Hash).To(Equal(before.Hash))
		// …a running one holding that hash is told nothing changed…
		same, err := getConfig(c, before.Hash)
		Expect(err).NotTo(HaveOccurred())
		Expect(same.NotModified).To(BeTrue())
		Expect(same.Hash).To(Equal(before.Hash))
		// …and Alloy's start-up RegisterCollector succeeds, so it does not exit.
		_, err = c.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{Id: "outage-instance-2", LocalAttributes: attrs}))
		Expect(err).NotTo(HaveOccurred())

		// The outage path wrote nothing: the second instance is unknown.
		db.restore()
		_, err = st.Queries.GetCollectorInstanceByID(ctx, "outage-instance-2")
		Expect(errors.Is(err, pgx.ErrNoRows)).To(BeTrue(), "got %v", err)
	})

	It("never serves a config remembered for another principal or another collector", func() {
		c := clientWith(goodBasic)
		seedClaimed(c)
		_, err := getConfig(c, "")
		Expect(err).NotTo(HaveOccurred())
		_, otherBasic := newToken()
		other := clientWith(otherBasic)
		otherBefore, err := getConfig(other, "") // verified and served while the DB is up
		Expect(err).NotTo(HaveOccurred())
		_, err = other.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
			Id: "x", LocalAttributes: map[string]string{"cluster": "elsewhere", "role": "metrics"},
		}))
		Expect(err).NotTo(HaveOccurred())

		db.cut()
		// Same token, a collector this replica never served it for.
		_, err = c.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
			Id: "outage-instance", LocalAttributes: map[string]string{"cluster": "elsewhere", "role": "metrics"},
		}))
		Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable), "got %v", err)
		// The other principal gets what IT was served (the same collector, so
		// the same content), keyed separately — never c's entry.
		resp, err := getConfig(other, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Hash).To(Equal(otherBefore.Hash))
		Expect(resp.Content).To(Equal(otherBefore.Content))
	})

	// A caller must not be able to force the outage path with its own
	// deadline (review of #385): connect-go turns Connect-Timeout-Ms into the
	// handler's context deadline, so a timeout tuned to expire inside the
	// database work used to make the failure look like an outage and skip
	// the rest of the request (the org check) in favour of the cached copy.
	// rawGetConfig posts with Connect-Timeout-Ms but waits for the server's
	// answer itself, so the spec sees what the server decided.
	rawGetConfig := func(authz string, timeoutMs int) (int, string) {
		body := `{"id":"outage-instance","localAttributes":{"cluster":"outage-cluster","role":"metrics"}}`
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			server.URL+collectorv1connect.CollectorServiceGetConfigProcedure, strings.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")
		req.Header.Set("Connect-Timeout-Ms", strconv.Itoa(timeoutMs))
		req.Header.Set("Authorization", authz)
		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		out, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		return resp.StatusCode, string(out)
	}

	It("does not treat a client's own deadline as an outage in GetConfig", func() {
		c := clientWith(goodBasic)
		seedClaimed(c)
		before, err := getConfig(c, "")
		Expect(err).NotTo(HaveOccurred())
		// The database would now serve something else.
		pl, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: seededOrg, Name: "outage-pipeline"})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.SetPipelineEnabled(ctx, sqlc.SetPipelineEnabledParams{ID: pl.ID, Enabled: false, UpdatedBy: "test"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.MarkServeCacheDirtyByOrg(ctx, seededOrg)).To(Succeed())

		db.slowNext("UpsertCluster", 600*time.Millisecond)
		_, body := rawGetConfig(goodBasic, 200)
		Expect(body).NotTo(ContainSubstring(before.Hash), "the cached config was served past the org check: %s", body)
	})

	It("does not treat a client's own deadline as an outage in the token lookup", func() {
		c := clientWith(goodBasic)
		_, err := getConfig(c, "")
		Expect(err).NotTo(HaveOccurred())
		var id pgtype.UUID
		Expect(id.Scan(tokenID)).To(Succeed())
		Expect(revokeInDB(id)).To(Succeed()) // another replica / the CLI

		db.slowNext("GetAgentTokenByID", 600*time.Millisecond)
		status, body := rawGetConfig(goodBasic, 200)
		Expect(status).NotTo(Equal(http.StatusOK), "a revoked token was accepted from the outage cache: %s", body)
		Expect(body).NotTo(ContainSubstring(`"hash"`))
	})

	It("keys an OIDC caller's remembered config on every claim the org check reads", func() {
		claims := auth.AgentClaims{Issuer: "https://idp/", Subject: "sub-1", AppID: "app-1"}
		req := func() *connect.Request[collectorv1.GetConfigRequest] {
			return connect.NewRequest(&collectorv1.GetConfigRequest{Id: "oidc-i", LocalAttributes: attrs})
		}
		before, err := svc.GetConfig(agentapi.ContextWithOIDCPrincipal(ctx, claims), req())
		Expect(err).NotTo(HaveOccurred())

		db.cut()
		same, err := svc.GetConfig(agentapi.ContextWithOIDCPrincipal(ctx, claims), req())
		Expect(err).NotTo(HaveOccurred())
		Expect(same.Msg.Hash).To(Equal(before.Msg.Hash))

		for name, changed := range map[string]auth.AgentClaims{
			"asserted org": {Issuer: claims.Issuer, Subject: claims.Subject, AppID: claims.AppID, Org: "outage-org"},
			"app id":       {Issuer: claims.Issuer, Subject: claims.Subject, AppID: "app-2"},
			"clusters":     {Issuer: claims.Issuer, Subject: claims.Subject, AppID: claims.AppID, Clusters: []string{"other"}},
		} {
			_, err := svc.GetConfig(agentapi.ContextWithOIDCPrincipal(ctx, changed), req())
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable), "%s: got %v", name, err)
		}
	})

	It("answers Unavailable during an outage for a token this replica has not verified", func() {
		_, freshBasic := newToken()
		db.cut()
		_, err := getConfig(clientWith(freshBasic), "")
		Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable), "got %v", err)
		_, err = clientWith(freshBasic).RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{Id: "i", LocalAttributes: attrs}))
		Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable), "got %v", err)
	})

	It("answers Unavailable for a verified token when nothing was served to it yet", func() {
		c := clientWith(goodBasic)
		_, err := c.UnregisterCollector(ctx, connect.NewRequest(&collectorv1.UnregisterCollectorRequest{Id: "nobody"}))
		Expect(err).NotTo(HaveOccurred()) // verifies the token, serves nothing
		db.cut()
		_, err = getConfig(c, "")
		Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable), "got %v", err)
	})

	It("rejects a wrong secret for a cached token during an outage", func() {
		c := clientWith(goodBasic)
		_, err := getConfig(c, "")
		Expect(err).NotTo(HaveOccurred())
		db.cut()
		wrong := "Basic " + base64.StdEncoding.EncodeToString([]byte(tokenID+":not-the-secret"))
		_, err = getConfig(clientWith(wrong), "")
		Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnauthenticated), "got %v", err)
	})

	It("forgets a revoked token immediately: revocation evicts it on this replica", func() {
		c := clientWith(goodBasic)
		_, err := getConfig(c, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(verdicts.Len()).To(Equal(1))

		verdicts.Evict(tokenID) // what AdminService.RevokeAgentToken does (internal/server wires it)
		db.cut()
		_, err = getConfig(c, "")
		Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable), "got %v", err)
	})

	It("forgets a token the database reports revoked, even when the revoke ran elsewhere", func() {
		c := clientWith(goodBasic)
		_, err := getConfig(c, "")
		Expect(err).NotTo(HaveOccurred())
		var id pgtype.UUID
		Expect(id.Scan(tokenID)).To(Succeed())
		Expect(revokeInDB(id)).To(Succeed()) // another replica / the CLI
		_, err = getConfig(c, "")
		Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnauthenticated))
		Expect(verdicts.Len()).To(BeZero())

		db.cut()
		_, err = getConfig(c, "")
		Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable), "got %v", err)
	})

	It("never caches an unknown token", func() {
		unknown := "Basic " + base64.StdEncoding.EncodeToString([]byte("00000000-0000-0000-0000-000000000001:whatever"))
		_, err := getConfig(clientWith(unknown), "")
		Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnauthenticated))
		Expect(verdicts.Len()).To(BeZero())
		db.cut()
		_, err = getConfig(clientWith(unknown), "")
		Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable), "got %v", err)
	})

	It("drops the remembered config when a later request is refused", func() {
		c := clientWith(goodBasic)
		seedClaimed(c)
		_, err := getConfig(c, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(agentapi.LastServedLen(svc)).To(Equal(1))

		// A non-outage refusal for the same key (an invalid instance id is
		// refused before any key is built, so use the handler's own logic
		// error: the DB refuses an over-long name it would never accept).
		ctxTok := agentapi.ContextWithTokenPrincipal(ctx, tokenID, agentapi.Binding{OrgID: seededOrg, MayClaim: true})
		_, err = svc.GetConfig(ctxTok, connect.NewRequest(&collectorv1.GetConfigRequest{
			Id: "outage-instance", LocalAttributes: map[string]string{"cluster": "outage-cluster", "role": "metrics", "collector.name": "\x00"},
		}))
		Expect(err).To(HaveOccurred())
		Expect(connect.CodeOf(err)).NotTo(Equal(connect.CodeUnavailable))
		Expect(agentapi.LastServedLen(svc)).To(BeZero())
	})

	// OIDC callers resolve through a server-side agent_identities binding the
	// claims do not carry (#385 re-review): the remembered config is keyed on
	// the binding as this replica last read it, and AdminService's create /
	// delete of an identity drops that on the serving replica.
	Describe("with OIDC agent identities", func() {
		const issuer, appID = "https://idp/", "collector-app"
		claims := auth.AgentClaims{Issuer: issuer, Subject: "sub-1", AppID: appID}
		oidcPoll := func() (*connect.Response[collectorv1.GetConfigResponse], error) {
			return svc.GetConfig(agentapi.ContextWithOIDCPrincipal(ctx, claims),
				connect.NewRequest(&collectorv1.GetConfigRequest{Id: "oidc-i", LocalAttributes: attrs}))
		}
		bindIdentity := func(org pgtype.UUID, clusters string) {
			_, err := st.Queries.CreateAgentIdentity(ctx, sqlc.CreateAgentIdentityParams{
				Issuer: issuer, AppID: appID, OrgID: org,
				Clusters: json.RawMessage(clusters), Roles: json.RawMessage(`[]`), CreatedBy: "test",
			})
			Expect(err).NotTo(HaveOccurred())
		}
		unbindIdentity := func() {
			n, err := st.Queries.DeleteAgentIdentity(ctx, sqlc.DeleteAgentIdentityParams{Issuer: issuer, AppID: appID})
			Expect(err).NotTo(HaveOccurred())
			Expect(n).To(BeEquivalentTo(1))
		}
		// served claims outage-cluster for seededOrg through the identity and
		// gives it seededOrg's pipeline.
		served := func() *collectorv1.GetConfigResponse {
			bindIdentity(seededOrg, `[]`)
			_, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: seededOrg, Name: "oidc-pipeline", Contents: "// oidc-pipeline",
				Matchers: json.RawMessage(`["cluster=\"outage-cluster\""]`), Enabled: true, Source: "ui",
				WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
			})
			Expect(err).NotTo(HaveOccurred())
			resp, err := oidcPoll()
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Msg.Content).To(ContainSubstring("oidc-pipeline"))
			return resp.Msg
		}

		It("keeps answering an OIDC caller whose binding is unchanged", func() {
			before := served()
			db.cut()
			resp, err := oidcPoll()
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Msg.Hash).To(Equal(before.Hash))
		})

		It("does not serve the old org's config after the identity is deleted on this replica", func() {
			served()
			unbindIdentity()
			svc.ForgetAgentIdentity(issuer, appID) // what AdminService.DeleteAgentIdentity does (internal/server wires it)

			db.cut()
			_, err := oidcPoll()
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable), "the deleted identity's config was served: %v", err)
		})

		It("does not serve the old org's config after the identity moved to another org on this replica", func() {
			served()
			orgB, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "org-b", DisplayName: "B", AdminGroupID: "gb"})
			Expect(err).NotTo(HaveOccurred())
			unbindIdentity()
			bindIdentity(orgB.ID, `["b-cluster"]`)
			svc.ForgetAgentIdentity(issuer, appID)

			db.cut()
			_, err = oidcPoll()
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable), "the moved identity got its old org's config: %v", err)
		})

		It("keys on the binding the database last returned, so a change seen by a poll is honoured", func() {
			served()
			// Moved elsewhere (another replica or the CLI); a poll on this
			// replica reads the new binding, which no longer permits the
			// cluster.
			orgB, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "org-b", DisplayName: "B", AdminGroupID: "gb"})
			Expect(err).NotTo(HaveOccurred())
			unbindIdentity()
			bindIdentity(orgB.ID, `["b-cluster"]`)
			_, err = oidcPoll()
			Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied), "got %v", err)

			db.cut()
			_, err = oidcPoll()
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable), "got %v", err)
		})
	})

	// The server-owned bound (dbWorkFloor) must never cut a validation short.
	// It cannot today because the poll path's recompute runs only in-process
	// Stage 1 — this spec proves that with a validator whose alloy binary
	// outlives the bound: if a change ever runs alloy here, it fails, and the
	// bound must then grow with validate.timeout / stage3_timeout.
	It("never runs alloy on the poll path, so the server-owned bound cannot truncate validation", func() {
		defer agentapi.SetDBWorkFloor(2 * time.Second)()
		slowAlloy := filepath.Join(GinkgoT().TempDir(), "alloy")
		Expect(os.WriteFile(slowAlloy, []byte("#!/bin/sh\nsleep 4\nexit 0\n"), 0o700)).To(Succeed())
		v := validate.New(&config.ValidateConfig{AlloyBinary: slowAlloy, StabilityLevel: "experimental", Timeout: 10 * time.Second, Stage3Timeout: 10 * time.Second})
		slow := agentapi.New(cst, v, slog.New(slog.DiscardHandler), testSchemaRegistry(), agentapi.WithOutageCache(time.Minute))

		c := clientWith(goodBasic)
		seedClaimed(c)
		start := time.Now()
		resp, err := slow.GetConfig(agentapi.ContextWithTokenPrincipal(ctx, tokenID, agentapi.Binding{OrgID: seededOrg, MayClaim: true}),
			connect.NewRequest(&collectorv1.GetConfigRequest{Id: "outage-instance", LocalAttributes: attrs}))
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Msg.Content).To(ContainSubstring("outage-pipeline"))
		Expect(time.Since(start)).To(BeNumerically("<", 2*time.Second), "the poll path ran alloy")
	})

	// Org-bound tokens (#386) on the outage path: the cached verdict carries
	// the token's whole binding, the binding is part of the last-served key,
	// an unbound token is refused, and nothing on the outage path claims.
	Describe("with org-bound tokens", func() {
		It("does not serve a config recorded under an earlier binding after a rebind to another org", func() {
			c := clientWith(goodBasic)
			seedClaimed(c)
			before, err := getConfig(c, "")
			Expect(err).NotTo(HaveOccurred())
			Expect(before.Content).To(ContainSubstring("outage-pipeline"))

			// Rebound elsewhere (another replica or the CLI) to org B, scoped
			// to b-cluster. A poll that sees the new binding refreshes the
			// verdict before the database goes away.
			orgB, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "org-b", DisplayName: "B", AdminGroupID: "gb"})
			Expect(err).NotTo(HaveOccurred())
			var id pgtype.UUID
			Expect(id.Scan(tokenID)).To(Succeed())
			_, err = st.Queries.BindAgentToken(ctx, sqlc.BindAgentTokenParams{
				ID: id, OrgID: orgB.ID, Clusters: json.RawMessage(`["b-cluster"]`), Roles: json.RawMessage(`[]`),
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = pollAs(c, "b-cluster")
			Expect(err).NotTo(HaveOccurred())

			db.cut()
			_, err = pollAs(c, "outage-cluster")
			Expect(err).To(HaveOccurred(), "the old org's config was served under the new binding")
			Expect(connect.CodeOf(err)).To(BeElementOf(connect.CodeUnavailable, connect.CodePermissionDenied), "got %v", err)
		})

		It("refuses during an outage a token the database had already unbound (org deleted)", func() {
			orgX, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "org-x", DisplayName: "X", AdminGroupID: "gx"})
			Expect(err).NotTo(HaveOccurred())
			_, basic := mintToken(orgX.ID, true)
			c := clientWith(basic)
			_, err = pollAs(c, "x-cluster")
			Expect(err).NotTo(HaveOccurred())

			// Deleting the org unbinds the token (ON DELETE SET NULL); the
			// next poll sees it and is refused while the database is up.
			cl, err := st.Queries.GetClusterByName(ctx, "x-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.UnclaimCluster(ctx, cl.ID)).To(Succeed())
			Expect(st.Queries.DeleteOrg(ctx, orgX.ID)).To(Succeed())
			_, err = pollAs(c, "x-cluster")
			Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied), "got %v", err)

			db.cut()
			_, err = pollAs(c, "x-cluster")
			Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied), "got %v", err)
			_, err = c.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id: "x-i", LocalAttributes: map[string]string{"cluster": "x-cluster", "role": "metrics"},
			}))
			Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied), "got %v", err)
		})

		It("never claims on the outage path for a token without may_claim", func() {
			_, basic := mintToken(seededOrg, false)
			c := clientWith(basic)
			_, err := pollAs(c, "unclaimed-cluster")
			Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied), "an org token cannot claim: %v", err)

			db.cut()
			_, err = pollAs(c, "unclaimed-cluster")
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable), "got %v", err)
			_, err = c.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id: "u-i", LocalAttributes: map[string]string{"cluster": "unclaimed-cluster", "role": "metrics"},
			}))
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable), "got %v", err)

			db.restore()
			cl, err := st.Queries.GetClusterByName(ctx, "unclaimed-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(cl.OrgID.Valid).To(BeFalse(), "the cluster was claimed")
		})
	})
})
