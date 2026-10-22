package server_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	collectorv1 "shepherd/gen/collector/v1"
	"shepherd/gen/collector/v1/collectorv1connect"
	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/server"
	"shepherd/internal/spa"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/testutil"
	"shepherd/internal/validate"
)

// The outage caches (#374) through the production route tree: the collector
// gate, GetConfig and AdminService.RevokeAgentToken must share ONE verdict
// cache, and agent.outage_cache_ttl must reach it. Each half is unit-tested
// in internal/agentapi; this proves internal/server wires them together.
//
// The rest of this suite needs no database, so the container starts on the
// first spec that does and is terminated after the suite (after every
// spec's isolated-database cleanup has run).
var (
	outagePG     *testutil.SharedPostgres
	outagePGOnce sync.Once
)

var _ = AfterSuite(func() {
	if outagePG != nil {
		Expect(outagePG.Terminate(context.Background())).To(Succeed())
	}
})

var _ = Describe("collector requests during a database outage (#374)", Label("integration"), func() {
	var pg *testutil.SharedPostgres

	BeforeEach(func() {
		outagePGOnce.Do(func() {
			var err error
			outagePG, err = testutil.StartSharedPostgres(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(store.MigrateUp(context.Background(), outagePG.RootURL)).To(Succeed())
		})
		Expect(outagePG).NotTo(BeNil())
		pg = outagePG
	})

	var (
		ctx    context.Context
		cancel context.CancelFunc
		st     *store.Store // always-up handle for seeding
		pool   *pgxpool.Pool
		down   atomic.Bool
		srv    *httptest.Server
		srvOrg pgtype.UUID
	)

	// boot builds the production router over a pool that can be cut: once
	// down, new connections dial a closed port and existing ones are dropped.
	boot := func(ttl time.Duration) {
		ctx, cancel = context.WithCancel(context.Background())
		url := pg.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: url, MaxConns: 4})
		Expect(err).NotTo(HaveOccurred())
		org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "srv-org", DisplayName: "Srv", AdminGroupID: "g"})
		Expect(err).NotTo(HaveOccurred())
		srvOrg = org.ID

		l, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		deadPort := uint16(l.Addr().(*net.TCPAddr).Port) //nolint:gosec // a TCP port fits in uint16
		Expect(l.Close()).To(Succeed())
		down.Store(false)
		pcfg, err := pgxpool.ParseConfig(url)
		Expect(err).NotTo(HaveOccurred())
		pcfg.MaxConns = 4
		pcfg.BeforeConnect = func(_ context.Context, cc *pgx.ConnConfig) error {
			if down.Load() {
				cc.Host, cc.Port, cc.Fallbacks, cc.ConnectTimeout = "127.0.0.1", deadPort, nil, 2*time.Second
			}
			return nil
		}
		pool, err = pgxpool.NewWithConfig(ctx, pcfg)
		Expect(err).NotTo(HaveOccurred())
		cutStore := store.NewWithPool(pool)

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}, Agent: config.AgentConfig{OutageCacheTTL: ttl}}
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		authHandler := auth.NewLocalAdmin(cfg, cutStore, logger)
		r := server.NewRouter(cfg, cutStore, nil, authHandler, validate.New(&cfg.Validate), server.EmbeddedSchema(), spa.BuildInfo{}, logger, func() bool { return false })
		srv = httptest.NewServer(r)
	}
	cut := func() { down.Store(true); pool.Reset() }

	AfterEach(func() {
		srv.Close()
		pool.Close()
		st.Close()
		cancel()
	})

	newToken := func() (id, header string) {
		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		secret := base64.URLEncoding.EncodeToString(raw)
		hash := sha256.Sum256([]byte(secret))
		tok, err := st.Queries.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{
			Name: "outage", TokenHash: hash[:], CreatedBy: "test", OrgID: srvOrg, MayClaim: true,
		})
		Expect(err).NotTo(HaveOccurred())
		return tok.ID.String(), "Basic " + base64.StdEncoding.EncodeToString([]byte(tok.ID.String()+":"+secret))
	}
	poll := func(authz string) (*collectorv1.GetConfigResponse, error) {
		c := collectorv1connect.NewCollectorServiceClient(srv.Client(), srv.URL)
		req := connect.NewRequest(&collectorv1.GetConfigRequest{
			Id: "srv-instance", LocalAttributes: map[string]string{"cluster": "srv-cluster", "role": "metrics"},
		})
		req.Header().Set("Authorization", authz)
		resp, err := c.GetConfig(ctx, req)
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	srvAttrs := map[string]string{"cluster": "srv-cluster", "role": "metrics"}
	register := func(authz, instanceID string) error {
		c := collectorv1connect.NewCollectorServiceClient(srv.Client(), srv.URL)
		req := connect.NewRequest(&collectorv1.RegisterCollectorRequest{Id: instanceID, LocalAttributes: srvAttrs})
		req.Header().Set("Authorization", authz)
		_, err := c.RegisterCollector(ctx, req)
		return err
	}
	// claim gives srv-cluster/metrics a real served config, so the outage
	// fallback is proven on content and a hash, not "" against "".
	claim := func(authz string) {
		// The token may claim: registering claims srv-cluster for srvOrg.
		Expect(register(authz, "srv-instance")).To(Succeed())
		_, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: srvOrg, Name: "srv-pipeline", Contents: "// srv-pipeline",
			Matchers: json.RawMessage(`["cluster=\"srv-cluster\""]`), Enabled: true, Source: "ui",
			WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())
	}
	adminRPC := func(procedure, body string) {
		sessionID := fmt.Sprintf("outage-admin-%d", time.Now().UnixNano())
		_, err := st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: sessionID, UserOid: "admin-oid", Email: "admin@example.com", DisplayName: "Admin",
			GroupIds: json.RawMessage(`[]`), IsAppAdmin: true,
			ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}, Source: "test",
		})
		Expect(err).NotTo(HaveOccurred())
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/shepherd.mgmt.v1.AdminService/"+procedure,
			strings.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.AddCookie(&http.Cookie{Name: "shepherd_session", Value: sessionID})
		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		out, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK), string(out))
	}

	revoke := func(tokenID string) { adminRPC("RevokeAgentToken", `{"id":"`+tokenID+`"}`) }

	It("keeps answering a recently served collector, with the same config", func() {
		boot(time.Minute)
		_, basic := newToken()
		claim(basic)
		before, err := poll(basic)
		Expect(err).NotTo(HaveOccurred())
		Expect(before.Content).To(ContainSubstring("srv-pipeline"))
		Expect(before.Hash).NotTo(BeEmpty())

		cut()
		during, err := poll(basic)
		Expect(err).NotTo(HaveOccurred())
		Expect(during.Hash).To(Equal(before.Hash))
		Expect(during.Content).To(Equal(before.Content))
	})

	It("acknowledges a recently served collector's start-up registration, and only that one", func() {
		boot(time.Minute)
		_, basic := newToken()
		claim(basic)
		_, err := poll(basic)
		Expect(err).NotTo(HaveOccurred())
		_, otherBasic := newToken()
		other := collectorv1connect.NewCollectorServiceClient(srv.Client(), srv.URL)
		oreq := connect.NewRequest(&collectorv1.UnregisterCollectorRequest{Id: "nobody"})
		oreq.Header().Set("Authorization", otherBasic)
		_, err = other.UnregisterCollector(ctx, oreq) // verified, never served
		Expect(err).NotTo(HaveOccurred())

		cut()
		// A restarted pod (new instance id) of the served collector starts.
		Expect(register(basic, "srv-instance-restarted")).To(Succeed())
		// A token this replica never served this collector is not acknowledged.
		Expect(connect.CodeOf(register(otherBasic, "srv-instance-other"))).To(Equal(connect.CodeUnavailable))

		down.Store(false)
		_, err = st.Queries.GetCollectorInstanceByID(ctx, "srv-instance-restarted")
		Expect(errors.Is(err, pgx.ErrNoRows)).To(BeTrue(), "the outage path wrote an instance row: %v", err)
	})

	It("stops accepting a token revoked through AdminService on this replica at once", func() {
		boot(time.Minute)
		id, basic := newToken()
		_, err := poll(basic)
		Expect(err).NotTo(HaveOccurred())
		revoke(id)

		cut()
		_, err = poll(basic)
		Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable), "got %v", err)
	})

	It("forgets a token rebound through AdminService on this replica at once", func() {
		boot(time.Minute)
		id, basic := newToken()
		claim(basic)
		_, err := poll(basic)
		Expect(err).NotTo(HaveOccurred())
		other, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "srv-org-2", DisplayName: "Srv 2", AdminGroupID: "g2"})
		Expect(err).NotTo(HaveOccurred())
		adminRPC("BindAgentToken", `{"id":"`+id+`","orgId":"`+other.ID.String()+`","clusters":["elsewhere"]}`)

		cut()
		_, err = poll(basic)
		Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable), "the pre-rebind config was served: %v", err)
	})

	It("answers Unavailable when agent.outage_cache_ttl is 0", func() {
		boot(0)
		_, basic := newToken()
		_, err := poll(basic)
		Expect(err).NotTo(HaveOccurred())

		cut()
		_, err = poll(basic)
		Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable), "got %v", err)
	})
})
