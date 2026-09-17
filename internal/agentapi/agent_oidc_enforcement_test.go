package agentapi_test

import (
	"context"
	"encoding/json"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	collectorv1 "shepherd/gen/collector/v1"
	"shepherd/internal/agentapi"
	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Collector-OIDC enforcement (docs/plans/2026-09-16-agent-oidc-auth.md, D1):
// an OIDC-authenticated collector's org comes from an agent_identities binding
// on its (issuer, app_id), which auto-claims the cluster to that org and
// enforces the binding's cluster/role allowlists. A principal with no binding
// falls through to the existing admin cluster-claim model. The principal is
// injected directly (the gate's Bearer branch is tested in TestAuthGateBearer,
// the verifier in internal/auth) so this exercises resolveOrg through the real
// GetConfig path against a real database.
var _ = Describe("Collector OIDC enforcement", Label("integration"), func() {
	var (
		st  *store.Store
		db  *pgxpool.Pool
		svc *agentapi.Service
		org sqlc.Org
	)

	const issuer = "https://idp.example/"

	arr := func(vs ...string) json.RawMessage {
		if vs == nil {
			vs = []string{}
		}
		b, err := json.Marshal(vs)
		Expect(err).NotTo(HaveOccurred())
		return b
	}

	// oidcCtx injects the principal the Bearer gate would have produced.
	oidcCtx := func(ctx context.Context, appID string) context.Context {
		return agentapi.ContextWithOIDCPrincipal(ctx, auth.AgentClaims{Issuer: issuer, AppID: appID})
	}

	getConfig := func(ctx context.Context, cluster, role string) (*connect.Response[collectorv1.GetConfigResponse], error) {
		return svc.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
			Id:              "inst-" + cluster + "-" + role,
			LocalAttributes: map[string]string{"cluster": cluster, "role": role},
		}))
	}

	clusterOrg := func(ctx context.Context, name string) pgtype.UUID {
		var orgID pgtype.UUID
		Expect(db.QueryRow(ctx, `SELECT org_id FROM clusters WHERE name = $1`, name).Scan(&orgID)).To(Succeed())
		return orgID
	}

	codeOf := func(err error) connect.Code {
		Expect(err).To(HaveOccurred())
		return connect.CodeOf(err)
	}

	BeforeEach(func(ctx context.Context) {
		url := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: url, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		svc = agentapi.New(st, nil, slog.New(slog.DiscardHandler), testSchemaRegistry())
		org, err = st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "platform-eng", DisplayName: "Platform Eng", AdminGroupID: "g"})
		Expect(err).NotTo(HaveOccurred())
	})

	bind := func(ctx context.Context, appID string, clusters, roles json.RawMessage) {
		_, err := st.Queries.CreateAgentIdentity(ctx, sqlc.CreateAgentIdentityParams{
			Issuer: issuer, AppID: appID, OrgID: org.ID, Clusters: clusters, Roles: roles, CreatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())
	}

	It("auto-claims an unclaimed cluster to the binding's org and serves it", func(ctx context.Context) {
		bind(ctx, "client-eu", arr(), arr())
		_, err := getConfig(oidcCtx(ctx, "client-eu"), "prod-eu-1", "metrics")
		Expect(err).NotTo(HaveOccurred())
		Expect(clusterOrg(ctx, "prod-eu-1")).To(Equal(org.ID), "the cluster must be bound to the token's org on first poll")
	})

	It("refuses a cluster outside the binding's allowlist", func(ctx context.Context) {
		bind(ctx, "client-eu", arr("staging-eu-1"), arr())
		_, err := getConfig(oidcCtx(ctx, "client-eu"), "prod-eu-1", "metrics")
		Expect(codeOf(err)).To(Equal(connect.CodePermissionDenied))
		Expect(clusterOrg(ctx, "prod-eu-1").Valid).To(BeFalse(), "a refused poll must not claim the cluster")
	})

	It("refuses a role outside the binding's allowlist", func(ctx context.Context) {
		bind(ctx, "client-eu", arr(), arr("logs"))
		_, err := getConfig(oidcCtx(ctx, "client-eu"), "prod-eu-1", "metrics")
		Expect(codeOf(err)).To(Equal(connect.CodePermissionDenied))
	})

	It("refuses a cluster already claimed by another org", func(ctx context.Context) {
		other, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "other-org", DisplayName: "Other", AdminGroupID: "g2"})
		Expect(err).NotTo(HaveOccurred())
		// Register the cluster and claim it to the other org first.
		_, err = getConfig(ctx, "prod-eu-1", "metrics") // no principal = agent-token path
		Expect(err).NotTo(HaveOccurred())
		cl, err := st.Queries.GetClusterByName(ctx, "prod-eu-1")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cl.ID, OrgID: other.ID})).To(Succeed())

		bind(ctx, "client-eu", arr(), arr())
		_, err = getConfig(oidcCtx(ctx, "client-eu"), "prod-eu-1", "metrics")
		Expect(codeOf(err)).To(Equal(connect.CodePermissionDenied), "a token must never take over another org's cluster")
		Expect(clusterOrg(ctx, "prod-eu-1")).To(Equal(other.ID), "the other org's claim is untouched")
	})

	It("serves an already-claimed matching-org cluster without error (idempotent claim)", func(ctx context.Context) {
		bind(ctx, "client-eu", arr(), arr())
		_, err := getConfig(oidcCtx(ctx, "client-eu"), "prod-eu-1", "metrics")
		Expect(err).NotTo(HaveOccurred())
		// Second poll: cluster already ours; must still succeed.
		_, err = getConfig(oidcCtx(ctx, "client-eu"), "prod-eu-1", "metrics")
		Expect(err).NotTo(HaveOccurred())
		Expect(clusterOrg(ctx, "prod-eu-1")).To(Equal(org.ID))
	})

	It("falls through to the admin cluster-claim model when the identity has no binding", func(ctx context.Context) {
		// No binding for client-unbound: OIDC proved liveness only. The cluster
		// stays unclaimed and empty config is served, exactly as for an
		// agent-token collector before an admin claims it.
		resp, err := getConfig(oidcCtx(ctx, "client-unbound"), "prod-eu-1", "metrics")
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Msg.Content).To(BeEmpty())
		Expect(clusterOrg(ctx, "prod-eu-1").Valid).To(BeFalse(), "no binding must not auto-claim")
	})

	// Mode 2 (D1 tier 2): no binding, but the IdP asserts an org via Claims.Org.
	oidcCtxOrg := func(ctx context.Context, appID, orgName string, clusters ...string) context.Context {
		return agentapi.ContextWithOIDCPrincipal(ctx, auth.AgentClaims{
			Issuer: issuer, AppID: appID, Org: orgName, Clusters: clusters,
		})
	}

	It("resolves the org from the token's asserted claim and auto-claims the cluster", func(ctx context.Context) {
		_, err := getConfig(oidcCtxOrg(ctx, "client-eu", "platform-eng"), "prod-eu-1", "metrics")
		Expect(err).NotTo(HaveOccurred())
		Expect(clusterOrg(ctx, "prod-eu-1")).To(Equal(org.ID), "the asserted org's cluster is claimed on first poll")
	})

	It("refuses an asserted org that does not exist", func(ctx context.Context) {
		_, err := getConfig(oidcCtxOrg(ctx, "client-eu", "no-such-org"), "prod-eu-1", "metrics")
		Expect(codeOf(err)).To(Equal(connect.CodePermissionDenied))
		Expect(clusterOrg(ctx, "prod-eu-1").Valid).To(BeFalse(), "a refused poll must not claim the cluster")
	})

	It("refuses a cluster outside the token's own clusters claim", func(ctx context.Context) {
		_, err := getConfig(oidcCtxOrg(ctx, "client-eu", "platform-eng", "staging-eu-1"), "prod-eu-1", "metrics")
		Expect(codeOf(err)).To(Equal(connect.CodePermissionDenied))
	})

	It("refuses an asserted org taking over another org's cluster", func(ctx context.Context) {
		other, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "other-org", DisplayName: "Other", AdminGroupID: "g2"})
		Expect(err).NotTo(HaveOccurred())
		_, err = getConfig(ctx, "prod-eu-1", "metrics") // agent-token path registers the cluster
		Expect(err).NotTo(HaveOccurred())
		cl, err := st.Queries.GetClusterByName(ctx, "prod-eu-1")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cl.ID, OrgID: other.ID})).To(Succeed())

		_, err = getConfig(oidcCtxOrg(ctx, "client-eu", "platform-eng"), "prod-eu-1", "metrics")
		Expect(codeOf(err)).To(Equal(connect.CodePermissionDenied))
		Expect(clusterOrg(ctx, "prod-eu-1")).To(Equal(other.ID), "the other org's claim is untouched")
	})

	It("prefers a binding over an asserted org claim when both are present", func(ctx context.Context) {
		// A Shepherd-local binding is authoritative: even if the token also
		// asserts a different org, the binding's org wins (tier 1 before 2).
		bind(ctx, "client-eu", arr(), arr())
		ctxBoth := agentapi.ContextWithOIDCPrincipal(ctx, auth.AgentClaims{
			Issuer: issuer, AppID: "client-eu", Org: "other-org",
		})
		_, err := getConfig(ctxBoth, "prod-eu-1", "metrics")
		Expect(err).NotTo(HaveOccurred())
		Expect(clusterOrg(ctx, "prod-eu-1")).To(Equal(org.ID), "the binding's org wins over the asserted claim")
	})
})
