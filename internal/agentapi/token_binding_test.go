package agentapi_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	dto "github.com/prometheus/client_model/go"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	collectorv1 "shepherd/gen/collector/v1"
	"shepherd/gen/collector/v1/collectorv1connect"
	"shepherd/internal/agentapi"
	"shepherd/internal/config"
	"shepherd/internal/metrics"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Org-bound agent tokens (migration 0038): a token principal resolves its org
// through the same resolver as an OIDC agent_identities binding — the token's
// org, its optional cluster/role allowlists, auto-claim of an allowed
// unclaimed cluster, refusal of a cluster another org claimed — and a token
// with no org is refused outright (token_unbound). Every call goes through
// the real Connect handler behind NewAuthGate with a Basic token.
var _ = Describe("Org-bound agent tokens", Label("integration"), func() {
	var (
		st     *store.Store
		server *httptest.Server
		orgA   sqlc.Org
		orgB   sqlc.Org
	)

	BeforeEach(func(ctx context.Context) {
		url := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: url, MaxConns: 10})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)

		svc := agentapi.New(st, nil, slog.New(slog.DiscardHandler), testSchemaRegistry())
		path, handler := agentapi.NewHandler(svc, connect.WithRequestGate(agentapi.NewAuthGate(st, nil)))
		mux := http.NewServeMux()
		mux.Handle(path, handler)
		server = httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
		DeferCleanup(server.Close)

		orgA, err = st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "org-a", DisplayName: "A", AdminGroupID: "ga"})
		Expect(err).NotTo(HaveOccurred())
		orgB, err = st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "org-b", DisplayName: "B", AdminGroupID: "gb"})
		Expect(err).NotTo(HaveOccurred())
	})

	arr := func(vs ...string) json.RawMessage {
		if vs == nil {
			vs = []string{}
		}
		b, err := json.Marshal(vs)
		Expect(err).NotTo(HaveOccurred())
		return b
	}

	// mintToken creates a token row directly (a nil org is an unbound,
	// pre-0038 token) and returns a client authenticating with it plus its
	// id. mayClaim is agent_tokens.may_claim: true for a token an app admin
	// minted, false for one an org admin or a service account minted.
	mintToken := func(ctx context.Context, org *sqlc.Org, clusters, roles []string, mayClaim bool) (collectorv1connect.CollectorServiceClient, pgtype.UUID) {
		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		secret := base64.URLEncoding.EncodeToString(raw)
		hash := sha256.Sum256([]byte(secret))
		params := sqlc.CreateAgentTokenParams{
			Name: "t", TokenHash: hash[:], CreatedBy: "test",
			Clusters: arr(clusters...), Roles: arr(roles...), MayClaim: mayClaim,
		}
		if org != nil {
			params.OrgID = org.ID
		}
		tok, err := st.Queries.CreateAgentToken(ctx, params)
		Expect(err).NotTo(HaveOccurred())
		authz := "Basic " + base64.StdEncoding.EncodeToString([]byte(tok.ID.String()+":"+secret))
		c := collectorv1connect.NewCollectorServiceClient(server.Client(), server.URL, connect.WithGRPC(),
			connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
				return func(c context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
					req.Header().Set("Authorization", authz)
					return next(c, req)
				}
			})))
		return c, tok.ID
	}

	// mint is an app-admin-minted token (may claim); orgMint an org admin's.
	mint := func(ctx context.Context, org *sqlc.Org, clusters, roles []string) (collectorv1connect.CollectorServiceClient, pgtype.UUID) {
		return mintToken(ctx, org, clusters, roles, true)
	}
	orgMint := func(ctx context.Context, org *sqlc.Org) collectorv1connect.CollectorServiceClient {
		c, _ := mintToken(ctx, org, nil, nil, false)
		return c
	}

	refused := func(reason string) float64 {
		m := &dto.Metric{}
		Expect(metrics.AgentRefusedTotal.WithLabelValues(reason).Write(m)).To(Succeed())
		return m.Counter.GetValue()
	}

	poll := func(ctx context.Context, c collectorv1connect.CollectorServiceClient, id, cluster, role string) error {
		_, err := c.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
			Id: id, LocalAttributes: map[string]string{"cluster": cluster, "role": role},
		}))
		return err
	}

	register := func(ctx context.Context, c collectorv1connect.CollectorServiceClient, id, cluster, role string) error {
		_, err := c.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
			Id: id, LocalAttributes: map[string]string{"cluster": cluster, "role": role},
		}))
		return err
	}

	clusterOrg := func(ctx context.Context, name string) pgtype.UUID {
		cl, err := st.Queries.GetClusterByName(ctx, name)
		Expect(err).NotTo(HaveOccurred())
		return cl.OrgID
	}

	claim := func(ctx context.Context, name string, org sqlc.Org) {
		cl, err := st.Queries.UpsertCluster(ctx, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cl.ID, OrgID: org.ID})).To(Succeed())
	}

	instanceCount := func(ctx context.Context, id string) int {
		var n int
		Expect(st.Pool().QueryRow(ctx, `SELECT count(*) FROM collector_instances WHERE id = $1`, id).Scan(&n)).To(Succeed()) // RAW-SQL-OK: test assertion
		return n
	}

	It("refuses an unbound token on every collector RPC, counted as token_unbound", func(ctx context.Context) {
		c, _ := mint(ctx, nil, nil, nil)
		before := refused(metrics.AgentRefusedTokenUnbound)

		err := poll(ctx, c, "inst-unbound", "free-cluster", "metrics")
		Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied))
		Expect(err.Error()).To(ContainSubstring("agent token is not bound to an organisation"))
		Expect(err.Error()).To(ContainSubstring("shepherd token bind"))

		err = register(ctx, c, "inst-unbound", "free-cluster", "metrics")
		Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied))

		_, err = c.UnregisterCollector(ctx, connect.NewRequest(&collectorv1.UnregisterCollectorRequest{Id: "inst-unbound"}))
		Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied))

		Expect(refused(metrics.AgentRefusedTokenUnbound) - before).To(Equal(3.0))
		Expect(instanceCount(ctx, "inst-unbound")).To(Equal(0), "a refused call leaves no instance row")
		Expect(clusterOrg(ctx, "free-cluster").Valid).To(BeFalse(), "an unbound token claims nothing")
	})

	Describe("a token an org admin or a service account minted", func() {
		It("never claims an unclaimed cluster: refused as claim_denied, the cluster stays unclaimed", func(ctx context.Context) {
			c := orgMint(ctx, &orgA)
			before := refused(metrics.AgentRefusedClaimDenied)
			err := poll(ctx, c, "inst-org", "new-cluster", "metrics")
			Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied))
			Expect(err.Error()).To(ContainSubstring("an organisation token cannot claim clusters; ask an app admin to claim it"))
			Expect(connect.CodeOf(register(ctx, c, "inst-org", "new-cluster", "metrics"))).To(Equal(connect.CodePermissionDenied))
			Expect(refused(metrics.AgentRefusedClaimDenied) - before).To(Equal(2.0))
			Expect(clusterOrg(ctx, "new-cluster").Valid).To(BeFalse(), "the cluster stays unclaimed")
			Expect(instanceCount(ctx, "inst-org")).To(Equal(0))
		})

		It("is served for a cluster its org has already claimed", func(ctx context.Context) {
			claim(ctx, "a-prod", orgA)
			Expect(poll(ctx, orgMint(ctx, &orgA), "inst-org", "a-prod", "metrics")).To(Succeed())
			Expect(instanceCount(ctx, "inst-org")).To(Equal(1))
		})

		It("is refused for a cluster another org claimed", func(ctx context.Context) {
			claim(ctx, "b-prod", orgB)
			err := poll(ctx, orgMint(ctx, &orgA), "inst-org", "b-prod", "metrics")
			Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied))
			Expect(err.Error()).To(ContainSubstring("claimed by another organisation"))
			Expect(clusterOrg(ctx, "b-prod")).To(Equal(orgB.ID))
		})
	})

	It("auto-claims an allowed unclaimed cluster for an app-admin token's org and serves it", func(ctx context.Context) {
		c, _ := mint(ctx, &orgA, nil, nil)
		Expect(poll(ctx, c, "inst-a", "a-prod", "metrics")).To(Succeed())
		Expect(clusterOrg(ctx, "a-prod")).To(Equal(orgA.ID))
		Expect(instanceCount(ctx, "inst-a")).To(Equal(1))
	})

	It("refuses a cluster claimed by another org — a token bound to org A cannot act as org B's cluster", func(ctx context.Context) {
		claim(ctx, "b-prod", orgB)
		c, _ := mint(ctx, &orgA, nil, nil)
		before := refused(metrics.AgentRefusedClaimDenied)

		err := poll(ctx, c, "inst-cross", "b-prod", "metrics")
		Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied))
		Expect(err.Error()).To(ContainSubstring("claimed by another organisation"))
		err = register(ctx, c, "inst-cross", "b-prod", "metrics")
		Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied))

		Expect(refused(metrics.AgentRefusedClaimDenied) - before).To(Equal(2.0))
		Expect(clusterOrg(ctx, "b-prod")).To(Equal(orgB.ID), "the claim is untouched")
		Expect(instanceCount(ctx, "inst-cross")).To(Equal(0))
	})

	It("serves only the clusters and roles in the token's allowlists", func(ctx context.Context) {
		c, _ := mint(ctx, &orgA, []string{"a-prod"}, []string{"metrics"})
		claimBefore := refused(metrics.AgentRefusedClaimDenied)
		roleBefore := refused(metrics.AgentRefusedRoleDenied)

		Expect(poll(ctx, c, "inst-ok", "a-prod", "metrics")).To(Succeed())

		err := poll(ctx, c, "inst-other-cluster", "a-staging", "metrics")
		Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied))
		Expect(err.Error()).To(ContainSubstring(`cluster "a-staging" is not permitted for this agent token`))

		err = poll(ctx, c, "inst-other-role", "a-prod", "logs")
		Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied))
		Expect(err.Error()).To(ContainSubstring(`role "logs" is not permitted for this agent token`))

		Expect(refused(metrics.AgentRefusedClaimDenied) - claimBefore).To(Equal(1.0))
		Expect(refused(metrics.AgentRefusedRoleDenied) - roleBefore).To(Equal(1.0))
		Expect(clusterOrg(ctx, "a-staging").Valid).To(BeFalse(), "a disallowed cluster is not claimed")
	})

	It("binds a token in place: the same secret starts working, for the bound org only", func(ctx context.Context) {
		c, id := mint(ctx, nil, nil, nil)
		Expect(connect.CodeOf(poll(ctx, c, "inst-bind", "a-prod", "metrics"))).To(Equal(connect.CodePermissionDenied))

		_, err := st.Queries.BindAgentToken(ctx, sqlc.BindAgentTokenParams{ID: id, OrgID: orgA.ID, Clusters: arr(), Roles: arr()})
		Expect(err).NotTo(HaveOccurred())

		Expect(poll(ctx, c, "inst-bind", "a-prod", "metrics")).To(Succeed())
		Expect(clusterOrg(ctx, "a-prod")).To(Equal(orgA.ID))

		claim(ctx, "b-prod", orgB)
		Expect(connect.CodeOf(poll(ctx, c, "inst-bind-b", "b-prod", "metrics"))).To(Equal(connect.CodePermissionDenied))
	})
})
