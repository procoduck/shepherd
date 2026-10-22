package agentapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	dto "github.com/prometheus/client_model/go"

	collectorv1 "shepherd/gen/collector/v1"
	"shepherd/internal/agentapi"
	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/metrics"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Instance ids are bound to an org (review step 5, R9/R10, B-AGENT-INSTANCE):
// GetConfig writes the instance row only after the org check, an instance id
// moves to another collector only within its org (or out of an unclaimed
// cluster), and UnregisterCollector is scoped to the caller's org. The
// principal is injected directly, as in agent_oidc_enforcement_test.go; a
// context with no principal is the agent-token path.
var _ = Describe("Collector instance binding", Label("integration"), func() {
	var (
		st   *store.Store
		svc  *agentapi.Service
		orgA sqlc.Org
		orgB sqlc.Org
	)

	const issuer = "https://idp.example/"

	refused := func(reason string) float64 {
		m := &dto.Metric{}
		Expect(metrics.AgentRefusedTotal.WithLabelValues(reason).Write(m)).To(Succeed())
		return m.Counter.GetValue()
	}

	poll := func(ctx context.Context, id, cluster, role string, extra map[string]string, status *collectorv1.RemoteConfigStatus) error {
		attrs := map[string]string{"cluster": cluster, "role": role}
		for k, v := range extra {
			attrs[k] = v
		}
		_, err := svc.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
			Id: id, LocalAttributes: attrs, RemoteConfigStatus: status, Hash: "h-" + id,
		}))
		return err
	}

	claim := func(ctx context.Context, cluster string, org sqlc.Org) {
		cl, err := st.Queries.UpsertCluster(ctx, cluster)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cl.ID, OrgID: org.ID})).To(Succeed())
	}

	collectorOf := func(ctx context.Context, cluster, role string) pgtype.UUID {
		c, err := st.Queries.GetCollectorByClusterAndRole(ctx, sqlc.GetCollectorByClusterAndRoleParams{Name: cluster, Role: role})
		Expect(err).NotTo(HaveOccurred())
		return c.ID
	}

	instance := func(ctx context.Context, id string) sqlc.CollectorInstance {
		inst, err := st.Queries.GetCollectorInstanceByID(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		return inst
	}

	attrsOf := func(inst sqlc.CollectorInstance) map[string]string {
		var m map[string]string
		Expect(json.Unmarshal(inst.LocalAttributes, &m)).To(Succeed())
		return m
	}

	bindingCtx := func(ctx context.Context, appID string, org sqlc.Org, roles ...string) context.Context {
		if roles == nil {
			roles = []string{}
		}
		rolesJSON, err := json.Marshal(roles)
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.CreateAgentIdentity(ctx, sqlc.CreateAgentIdentityParams{
			Issuer: issuer, AppID: appID, OrgID: org.ID, Clusters: json.RawMessage(`[]`), Roles: rolesJSON, CreatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())
		return agentapi.ContextWithOIDCPrincipal(ctx, auth.AgentClaims{Issuer: issuer, AppID: appID})
	}

	BeforeEach(func(ctx context.Context) {
		url := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: url, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		svc = agentapi.New(st, nil, slog.New(slog.DiscardHandler), testSchemaRegistry())
		orgA, err = st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "org-a", DisplayName: "A", AdminGroupID: "ga"})
		Expect(err).NotTo(HaveOccurred())
		orgB, err = st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "org-b", DisplayName: "B", AdminGroupID: "gb"})
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("rebinding an instance id", func() {
		It("refuses a move to another org's collector and leaves the row untouched", func(ctx context.Context) {
			claim(ctx, "a-cluster", orgA)
			claim(ctx, "b-cluster", orgB)
			Expect(poll(ctx, "host-1", "a-cluster", "metrics", map[string]string{"team": "a"}, nil)).To(Succeed())
			before := refused(metrics.AgentRefusedInstanceRebind)

			err := poll(ctx, "host-1", "b-cluster", "metrics", map[string]string{"team": "b"},
				&collectorv1.RemoteConfigStatus{Status: collectorv1.RemoteConfigStatuses_RemoteConfigStatuses_FAILED, ErrorMessage: "boom"})
			Expect(err).To(HaveOccurred())
			Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied))

			inst := instance(ctx, "host-1")
			Expect(inst.CollectorID).To(Equal(collectorOf(ctx, "a-cluster", "metrics")), "the instance stays on org A's collector")
			Expect(attrsOf(inst)).To(HaveKeyWithValue("team", "a"), "the refused poll's attributes are not written")
			Expect(inst.RemoteConfigStatus.String).NotTo(Equal("FAILED"), "the refused poll's status is not written")
			Expect(refused(metrics.AgentRefusedInstanceRebind)).To(Equal(before+1))
		})

		It("allows a move between two collectors of the same org", func(ctx context.Context) {
			claim(ctx, "a-cluster", orgA)
			claim(ctx, "a-cluster-2", orgA)
			Expect(poll(ctx, "host-1", "a-cluster", "metrics", nil, nil)).To(Succeed())
			Expect(poll(ctx, "host-1", "a-cluster-2", "logs", nil, nil)).To(Succeed())
			Expect(instance(ctx, "host-1").CollectorID).To(Equal(collectorOf(ctx, "a-cluster-2", "logs")))
		})

		It("allows a move out of an unclaimed cluster", func(ctx context.Context) {
			Expect(poll(ctx, "host-1", "new-cluster", "metrics", nil, nil)).To(Succeed())
			claim(ctx, "b-cluster", orgB)
			Expect(poll(ctx, "host-1", "b-cluster", "metrics", nil, nil)).To(Succeed())
			Expect(instance(ctx, "host-1").CollectorID).To(Equal(collectorOf(ctx, "b-cluster", "metrics")))
		})

		It("refuses a move from a claimed cluster into an unclaimed one", func(ctx context.Context) {
			claim(ctx, "a-cluster", orgA)
			Expect(poll(ctx, "host-1", "a-cluster", "metrics", nil, nil)).To(Succeed())
			err := poll(ctx, "host-1", "new-cluster", "metrics", nil, nil)
			Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied))
			Expect(instance(ctx, "host-1").CollectorID).To(Equal(collectorOf(ctx, "a-cluster", "metrics")))
		})

		It("applies the same rule to RegisterCollector", func(ctx context.Context) {
			claim(ctx, "a-cluster", orgA)
			claim(ctx, "b-cluster", orgB)
			Expect(poll(ctx, "host-1", "a-cluster", "metrics", nil, nil)).To(Succeed())
			_, err := svc.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id: "host-1", LocalAttributes: map[string]string{"cluster": "b-cluster", "role": "metrics"},
			}))
			Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied))
			Expect(instance(ctx, "host-1").CollectorID).To(Equal(collectorOf(ctx, "a-cluster", "metrics")))
		})
	})

	Describe("a refused poll", func() {
		It("writes no instance row for a new instance id", func(ctx context.Context) {
			oidc := bindingCtx(ctx, "client-a", orgA, "logs")
			before := refused(metrics.AgentRefusedRoleDenied)
			err := poll(oidc, "fresh-host", "a-cluster", "metrics", nil,
				&collectorv1.RemoteConfigStatus{Status: collectorv1.RemoteConfigStatuses_RemoteConfigStatuses_FAILED, ErrorMessage: "boom"})
			Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied))
			_, getErr := st.Queries.GetCollectorInstanceByID(ctx, "fresh-host")
			Expect(errors.Is(getErr, pgx.ErrNoRows)).To(BeTrue(), "a refused poll leaves no instance row")
			Expect(refused(metrics.AgentRefusedRoleDenied)).To(Equal(before+1))
		})

		It("leaves an existing instance's attributes and status untouched", func(ctx context.Context) {
			oidc := bindingCtx(ctx, "client-a", orgA, "metrics")
			Expect(poll(oidc, "host-1", "a-cluster", "metrics", map[string]string{"team": "a"}, nil)).To(Succeed())
			before := instance(ctx, "host-1")

			err := poll(oidc, "host-1", "a-cluster", "logs", map[string]string{"team": "x"},
				&collectorv1.RemoteConfigStatus{Status: collectorv1.RemoteConfigStatuses_RemoteConfigStatuses_FAILED, ErrorMessage: "boom"})
			Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied))

			after := instance(ctx, "host-1")
			Expect(after.CollectorID).To(Equal(before.CollectorID))
			Expect(attrsOf(after)).To(Equal(attrsOf(before)))
			Expect(after.RemoteConfigStatus).To(Equal(before.RemoteConfigStatus))
			Expect(after.LastSeen).To(Equal(before.LastSeen))
		})

		It("counts a cluster refusal as claim_denied", func(ctx context.Context) {
			claim(ctx, "b-cluster", orgB)
			oidc := bindingCtx(ctx, "client-a", orgA)
			before := refused(metrics.AgentRefusedClaimDenied)
			Expect(connect.CodeOf(poll(oidc, "host-1", "b-cluster", "metrics", nil, nil))).To(Equal(connect.CodePermissionDenied))
			Expect(refused(metrics.AgentRefusedClaimDenied)).To(Equal(before+1))
		})
	})

	Describe("UnregisterCollector", func() {
		unregister := func(ctx context.Context, id string) error {
			_, err := svc.UnregisterCollector(ctx, connect.NewRequest(&collectorv1.UnregisterCollectorRequest{Id: id}))
			return err
		}

		It("refuses to unregister another org's instance", func(ctx context.Context) {
			claim(ctx, "b-cluster", orgB)
			Expect(poll(ctx, "b-host", "b-cluster", "metrics", nil, nil)).To(Succeed())
			oidc := bindingCtx(ctx, "client-a", orgA)
			before := refused(metrics.AgentRefusedUnregisterDenied)

			err := unregister(oidc, "b-host")
			Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied))
			Expect(instance(ctx, "b-host").UnregisteredAt.Valid).To(BeFalse())
			Expect(refused(metrics.AgentRefusedUnregisterDenied)).To(Equal(before+1))
		})

		It("unregisters an instance of the caller's own org", func(ctx context.Context) {
			oidc := bindingCtx(ctx, "client-a", orgA)
			Expect(poll(oidc, "a-host", "a-cluster", "metrics", nil, nil)).To(Succeed())
			Expect(unregister(oidc, "a-host")).To(Succeed())
			Expect(instance(ctx, "a-host").UnregisteredAt.Valid).To(BeTrue())
		})

		It("is cleared by the instance's next poll", func(ctx context.Context) {
			claim(ctx, "a-cluster", orgA)
			Expect(poll(ctx, "a-host", "a-cluster", "metrics", nil, nil)).To(Succeed())
			Expect(unregister(ctx, "a-host")).To(Succeed())
			Expect(instance(ctx, "a-host").UnregisteredAt.Valid).To(BeTrue())

			Expect(poll(ctx, "a-host", "a-cluster", "metrics", nil, nil)).To(Succeed())
			Expect(instance(ctx, "a-host").UnregisteredAt.Valid).To(BeFalse(), "a polling instance is registered again")
		})
	})
})
