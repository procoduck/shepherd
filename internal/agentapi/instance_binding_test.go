package agentapi_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	dto "github.com/prometheus/client_model/go"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	collectorv1 "shepherd/gen/collector/v1"
	"shepherd/gen/collector/v1/collectorv1connect"
	"shepherd/internal/agentapi"
	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/metrics"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Instances are keyed per collector (review step 5, R9/R10, B-AGENT-INSTANCE,
// migration 0036): an instance id never moves between collectors, so no org
// can squat or rebind another org's id; GetConfig writes the instance row only
// after the org check; a poll clears unregistered_at; UnregisterCollector is
// scoped to the caller's org. Every call goes through the real Connect handler
// behind NewAuthGate: a Basic agent token, or a Bearer token the fake verifier
// below maps to claims (the verifier itself is tested in internal/auth).
var _ = Describe("Collector instance identity", Label("integration"), func() {
	var (
		st     *store.Store
		server *httptest.Server
		orgA   sqlc.Org
		orgB   sqlc.Org
		basic  string
		claims map[string]auth.AgentClaims
	)

	const issuer = "https://idp.example/"

	BeforeEach(func(ctx context.Context) {
		url := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: url, MaxConns: 10})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)

		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		secret := base64.URLEncoding.EncodeToString(raw)
		hash := sha256.Sum256([]byte(secret))
		tok, err := st.Queries.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{Name: "t", TokenHash: hash[:], CreatedBy: "test"})
		Expect(err).NotTo(HaveOccurred())
		basic = "Basic " + base64.StdEncoding.EncodeToString([]byte(tok.ID.String()+":"+secret))

		claims = map[string]auth.AgentClaims{}
		verify := func(_ context.Context, raw string) (auth.AgentClaims, error) {
			c, ok := claims[raw]
			if !ok {
				return auth.AgentClaims{}, errors.New("unknown token")
			}
			return c, nil
		}

		svc := agentapi.New(st, nil, slog.New(slog.DiscardHandler), testSchemaRegistry())
		path, handler := agentapi.NewHandler(svc, connect.WithRequestGate(agentapi.NewAuthGate(st, verify)))
		mux := http.NewServeMux()
		mux.Handle(path, handler)
		server = httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
		DeferCleanup(server.Close)

		orgA, err = st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "org-a", DisplayName: "A", AdminGroupID: "ga"})
		Expect(err).NotTo(HaveOccurred())
		orgB, err = st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "org-b", DisplayName: "B", AdminGroupID: "gb"})
		Expect(err).NotTo(HaveOccurred())
	})

	// client authenticates with the given Authorization header value.
	client := func(authz string) collectorv1connect.CollectorServiceClient {
		return collectorv1connect.NewCollectorServiceClient(server.Client(), server.URL, connect.WithGRPC(),
			connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
				return func(c context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
					req.Header().Set("Authorization", authz)
					return next(c, req)
				}
			})))
	}
	token := func() collectorv1connect.CollectorServiceClient { return client(basic) }

	// bound returns a client whose Bearer token maps to an agent_identities
	// binding for org, with an optional role allowlist.
	bound := func(ctx context.Context, appID string, org sqlc.Org, roles ...string) collectorv1connect.CollectorServiceClient {
		if roles == nil {
			roles = []string{}
		}
		rolesJSON, err := json.Marshal(roles)
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.CreateAgentIdentity(ctx, sqlc.CreateAgentIdentityParams{
			Issuer: issuer, AppID: appID, OrgID: org.ID, Clusters: json.RawMessage(`[]`), Roles: rolesJSON, CreatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())
		claims["jwt-"+appID] = auth.AgentClaims{Issuer: issuer, AppID: appID}
		return client("Bearer jwt-" + appID)
	}

	// asserted returns a client whose Bearer token asserts org by name (mode 2).
	asserted := func(appID, orgName string) collectorv1connect.CollectorServiceClient {
		claims["jwt-"+appID] = auth.AgentClaims{Issuer: issuer, AppID: appID, Org: orgName}
		return client("Bearer jwt-" + appID)
	}

	refused := func(reason string) float64 {
		m := &dto.Metric{}
		Expect(metrics.AgentRefusedTotal.WithLabelValues(reason).Write(m)).To(Succeed())
		return m.Counter.GetValue()
	}

	poll := func(ctx context.Context, c collectorv1connect.CollectorServiceClient, id, cluster, role string,
		extra map[string]string, status *collectorv1.RemoteConfigStatus,
	) (*connect.Response[collectorv1.GetConfigResponse], error) {
		attrs := map[string]string{"cluster": cluster, "role": role}
		for k, v := range extra {
			attrs[k] = v
		}
		return c.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
			Id: id, LocalAttributes: attrs, RemoteConfigStatus: status, Hash: "h-" + id,
		}))
	}
	mustPoll := func(ctx context.Context, c collectorv1connect.CollectorServiceClient, id, cluster, role string, extra map[string]string) {
		_, err := poll(ctx, c, id, cluster, role, extra, nil)
		Expect(err).NotTo(HaveOccurred())
	}
	failed := &collectorv1.RemoteConfigStatus{Status: collectorv1.RemoteConfigStatuses_RemoteConfigStatuses_FAILED, ErrorMessage: "boom"}

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

	// row reads the instance row (collector, id); ok is false when there is none.
	row := func(ctx context.Context, cluster, role, id string) (sqlc.CollectorInstance, bool) {
		inst, err := st.Queries.GetCollectorInstance(ctx, sqlc.GetCollectorInstanceParams{CollectorID: collectorOf(ctx, cluster, role), ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.CollectorInstance{}, false
		}
		Expect(err).NotTo(HaveOccurred())
		return inst, true
	}
	mustRow := func(ctx context.Context, cluster, role, id string) sqlc.CollectorInstance {
		inst, ok := row(ctx, cluster, role, id)
		Expect(ok).To(BeTrue(), "expected an instance row %s under %s/%s", id, cluster, role)
		return inst
	}

	attrsOf := func(inst sqlc.CollectorInstance) map[string]string {
		var m map[string]string
		Expect(json.Unmarshal(inst.LocalAttributes, &m)).To(Succeed())
		return m
	}

	Describe("an instance id under several collectors", func() {
		It("cannot be squatted: another org polling the id first does not lock its real collector out", func(ctx context.Context) {
			claim(ctx, "a-cluster", orgA)
			claim(ctx, "b-cluster", orgB)
			// An org-B identity reports org A's hostname-like id first.
			mustPoll(ctx, bound(ctx, "client-b", orgB), "host-1", "b-cluster", "metrics", map[string]string{"team": "b"})

			mustPoll(ctx, token(), "host-1", "a-cluster", "metrics", map[string]string{"team": "a"})
			Expect(attrsOf(mustRow(ctx, "a-cluster", "metrics", "host-1"))).To(HaveKeyWithValue("team", "a"))
			Expect(attrsOf(mustRow(ctx, "b-cluster", "metrics", "host-1"))).To(HaveKeyWithValue("team", "b"),
				"each collector keeps its own row")
		})

		It("cannot be rebound: polling the id under another org's collector leaves the first org's row untouched", func(ctx context.Context) {
			claim(ctx, "a-cluster", orgA)
			claim(ctx, "b-cluster", orgB)
			mustPoll(ctx, token(), "host-1", "a-cluster", "metrics", map[string]string{"team": "a"})
			before := mustRow(ctx, "a-cluster", "metrics", "host-1")

			_, err := poll(ctx, token(), "host-1", "b-cluster", "metrics", map[string]string{"team": "b"}, failed)
			Expect(err).NotTo(HaveOccurred())

			after := mustRow(ctx, "a-cluster", "metrics", "host-1")
			Expect(attrsOf(after)).To(Equal(attrsOf(before)))
			Expect(after.RemoteConfigStatus).To(Equal(before.RemoteConfigStatus))
			Expect(after.LastSeen).To(Equal(before.LastSeen))
			Expect(mustRow(ctx, "b-cluster", "metrics", "host-1").RemoteConfigStatus.String).To(Equal("FAILED"),
				"the status lands on the row of the collector that polled")
		})

		It("writes a second row on a same-org move through a binding", func(ctx context.Context) {
			claim(ctx, "a-cluster", orgA)
			claim(ctx, "a-cluster-2", orgA)
			c := bound(ctx, "client-a", orgA)
			mustPoll(ctx, c, "host-1", "a-cluster", "metrics", nil)
			mustPoll(ctx, c, "host-1", "a-cluster-2", "logs", nil)
			mustRow(ctx, "a-cluster", "metrics", "host-1")
			mustRow(ctx, "a-cluster-2", "logs", "host-1")
		})

		It("serves empty config, not PermissionDenied, after a cluster rename under a static token", func(ctx context.Context) {
			claim(ctx, "a-cluster", orgA)
			mustPoll(ctx, token(), "host-1", "a-cluster", "metrics", nil)

			resp, err := poll(ctx, token(), "host-1", "renamed-cluster", "metrics", nil, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Msg.GetContent()).To(BeEmpty())
			Expect(resp.Msg.GetHash()).To(Equal(agentapi.EmptyHash))
			mustRow(ctx, "renamed-cluster", "metrics", "host-1")
			mustRow(ctx, "a-cluster", "metrics", "host-1")
		})

		It("survives a concurrent first poll for the same id under two collectors", func(ctx context.Context) {
			claim(ctx, "a-cluster", orgA)
			claim(ctx, "b-cluster", orgB)
			var wg sync.WaitGroup
			errs := make([]error, 2)
			for i, cl := range []string{"a-cluster", "b-cluster"} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer GinkgoRecover()
					_, errs[i] = poll(ctx, token(), "race-host", cl, "metrics", nil, nil)
				}()
			}
			wg.Wait()
			Expect(errs).To(HaveEach(Not(HaveOccurred())))
			mustRow(ctx, "a-cluster", "metrics", "race-host")
			mustRow(ctx, "b-cluster", "metrics", "race-host")
		})

		It("applies the same identity to RegisterCollector", func(ctx context.Context) {
			claim(ctx, "a-cluster", orgA)
			claim(ctx, "b-cluster", orgB)
			mustPoll(ctx, token(), "host-1", "a-cluster", "metrics", nil)
			_, err := token().RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id: "host-1", LocalAttributes: map[string]string{"cluster": "b-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())
			mustRow(ctx, "a-cluster", "metrics", "host-1")
			mustRow(ctx, "b-cluster", "metrics", "host-1")
		})
	})

	Describe("a refused poll", func() {
		It("writes no instance row for a new instance id and counts role_denied", func(ctx context.Context) {
			c := bound(ctx, "client-a", orgA, "logs")
			before := refused(metrics.AgentRefusedRoleDenied)
			_, err := poll(ctx, c, "fresh-host", "a-cluster", "metrics", nil, failed)
			Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied))
			_, ok := row(ctx, "a-cluster", "metrics", "fresh-host")
			Expect(ok).To(BeFalse(), "a refused poll leaves no instance row")
			Expect(refused(metrics.AgentRefusedRoleDenied)).To(Equal(before + 1))
		})

		It("leaves an existing instance row's attributes, status and last_seen untouched", func(ctx context.Context) {
			claim(ctx, "b-cluster", orgB)
			mustPoll(ctx, token(), "host-1", "b-cluster", "metrics", map[string]string{"team": "b"})
			before := mustRow(ctx, "b-cluster", "metrics", "host-1")

			// An org-A identity names org B's cluster with the same id.
			_, err := poll(ctx, bound(ctx, "client-a", orgA), "host-1", "b-cluster", "metrics", map[string]string{"team": "x"}, failed)
			Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied))

			after := mustRow(ctx, "b-cluster", "metrics", "host-1")
			Expect(attrsOf(after)).To(Equal(attrsOf(before)))
			Expect(after.RemoteConfigStatus).To(Equal(before.RemoteConfigStatus))
			Expect(after.LastSeen).To(Equal(before.LastSeen))
		})

		It("counts a cluster refusal as claim_denied", func(ctx context.Context) {
			claim(ctx, "b-cluster", orgB)
			before := refused(metrics.AgentRefusedClaimDenied)
			_, err := poll(ctx, bound(ctx, "client-a", orgA), "host-1", "b-cluster", "metrics", nil, nil)
			Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied))
			Expect(refused(metrics.AgentRefusedClaimDenied)).To(Equal(before + 1))
		})
	})

	Describe("request size bounds", func() {
		long := strings.Repeat("x", agentapi.MaxIdentifierBytes+1)

		It("refuses an over-long instance id on every RPC before any write", func(ctx context.Context) {
			_, err := poll(ctx, token(), long, "a-cluster", "metrics", nil, nil)
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeInvalidArgument))
			_, err = token().RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id: long, LocalAttributes: map[string]string{"cluster": "a-cluster", "role": "metrics"},
			}))
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeInvalidArgument))
			_, err = token().UnregisterCollector(ctx, connect.NewRequest(&collectorv1.UnregisterCollectorRequest{Id: long}))
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeInvalidArgument))
			_, err = st.Queries.GetClusterByName(ctx, "a-cluster")
			Expect(errors.Is(err, pgx.ErrNoRows)).To(BeTrue(), "refused before the cluster upsert")
		})

		It("accepts an id at the limit", func(ctx context.Context) {
			mustPoll(ctx, token(), long[:agentapi.MaxIdentifierBytes], "a-cluster", "metrics", nil)
		})

		It("refuses an empty instance id", func(ctx context.Context) {
			_, err := poll(ctx, token(), "", "a-cluster", "metrics", nil, nil)
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeInvalidArgument))
			_, err = token().UnregisterCollector(ctx, connect.NewRequest(&collectorv1.UnregisterCollectorRequest{}))
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeInvalidArgument))
		})

		It("refuses an over-long cluster attribute", func(ctx context.Context) {
			_, err := poll(ctx, token(), "host-1", long, "metrics", nil, nil)
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeInvalidArgument))
		})

		It("refuses a request message over MaxRequestBytes", func(ctx context.Context) {
			big := strings.Repeat("y", agentapi.MaxRequestBytes)
			_, err := poll(ctx, token(), "host-1", "a-cluster", "metrics", map[string]string{"blob": big}, nil)
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeResourceExhausted))
			_, err = st.Queries.GetClusterByName(ctx, "a-cluster")
			Expect(errors.Is(err, pgx.ErrNoRows)).To(BeTrue(), "refused before the handler ran")
		})
	})

	Describe("UnregisterCollector", func() {
		unregister := func(ctx context.Context, c collectorv1connect.CollectorServiceClient, id string) {
			_, err := c.UnregisterCollector(ctx, connect.NewRequest(&collectorv1.UnregisterCollectorRequest{Id: id}))
			Expect(err).NotTo(HaveOccurred(), "unregister answers success in and out of scope alike")
		}
		unregistered := func(ctx context.Context, cluster, role, id string) bool {
			return mustRow(ctx, cluster, role, id).UnregisteredAt.Valid
		}

		BeforeEach(func(ctx context.Context) {
			claim(ctx, "a-cluster", orgA)
			claim(ctx, "b-cluster", orgB)
			// One id under a collector of each org.
			mustPoll(ctx, token(), "shared-host", "a-cluster", "metrics", nil)
			mustPoll(ctx, token(), "shared-host", "b-cluster", "metrics", nil)
		})

		It("marks only the caller's org's row for a binding, and counts unregister_denied for the rest", func(ctx context.Context) {
			before := refused(metrics.AgentRefusedUnregisterDenied)
			unregister(ctx, bound(ctx, "client-a", orgA), "shared-host")
			Expect(unregistered(ctx, "a-cluster", "metrics", "shared-host")).To(BeTrue())
			Expect(unregistered(ctx, "b-cluster", "metrics", "shared-host")).To(BeFalse(), "org B's row is out of scope")
			Expect(refused(metrics.AgentRefusedUnregisterDenied)).To(Equal(before + 1))
		})

		It("leaves another org's only row alone and answers success", func(ctx context.Context) {
			mustPoll(ctx, token(), "b-only", "b-cluster", "metrics", nil)
			unregister(ctx, bound(ctx, "client-a", orgA), "b-only")
			Expect(unregistered(ctx, "b-cluster", "metrics", "b-only")).To(BeFalse())
		})

		It("honours a binding's role allowlist", func(ctx context.Context) {
			unregister(ctx, bound(ctx, "client-a", orgA, "logs"), "shared-host")
			Expect(unregistered(ctx, "a-cluster", "metrics", "shared-host")).To(BeFalse())
		})

		It("scopes a mode-2 asserted org the same way", func(ctx context.Context) {
			unregister(ctx, asserted("client-b", "org-b"), "shared-host")
			Expect(unregistered(ctx, "b-cluster", "metrics", "shared-host")).To(BeTrue())
			Expect(unregistered(ctx, "a-cluster", "metrics", "shared-host")).To(BeFalse())
		})

		It("marks nothing for an asserted org that does not exist", func(ctx context.Context) {
			unregister(ctx, asserted("client-x", "no-such-org"), "shared-host")
			Expect(unregistered(ctx, "a-cluster", "metrics", "shared-host")).To(BeFalse())
			Expect(unregistered(ctx, "b-cluster", "metrics", "shared-host")).To(BeFalse())
		})

		It("marks every row with the id for a static token", func(ctx context.Context) {
			unregister(ctx, token(), "shared-host")
			Expect(unregistered(ctx, "a-cluster", "metrics", "shared-host")).To(BeTrue())
			Expect(unregistered(ctx, "b-cluster", "metrics", "shared-host")).To(BeTrue())
		})

		It("answers success for an unknown id", func(ctx context.Context) {
			unregister(ctx, token(), "never-seen")
			unregister(ctx, bound(ctx, "client-a", orgA), "never-seen")
		})

		It("is cleared by the instance's next poll", func(ctx context.Context) {
			unregister(ctx, token(), "shared-host")
			Expect(unregistered(ctx, "a-cluster", "metrics", "shared-host")).To(BeTrue())
			mustPoll(ctx, token(), "shared-host", "a-cluster", "metrics", nil)
			Expect(unregistered(ctx, "a-cluster", "metrics", "shared-host")).To(BeFalse(), "a polling instance is registered again")
			Expect(unregistered(ctx, "b-cluster", "metrics", "shared-host")).To(BeTrue(), "only the polling collector's row")
		})
	})
})
