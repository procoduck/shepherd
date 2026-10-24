package agentapi_test

import (
	"bytes"
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
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	collectorv1 "shepherd/gen/collector/v1"
	"shepherd/gen/collector/v1/collectorv1connect"
	"shepherd/internal/agentapi"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Refused polls on the collector row (migration 0041): a refused poll writes
// no instance row (#370), so without this the Collectors page kept showing the
// last successful poll's APPLIED for a fleet whose every poll was refused. The
// refusal is recorded on the collector (cluster + role) row the poll upserts
// anyway, at most once per refusal-log window per reason, and cleared by the
// next poll that passes the org check.
var _ = Describe("Collector refusal recording", Label("integration"), func() {
	var (
		st     *store.Store
		svc    *agentapi.Service
		server *httptest.Server
		orgA   sqlc.Org
		orgB   sqlc.Org
		logBuf *bytes.Buffer
	)

	BeforeEach(func(ctx context.Context) {
		url := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: url, MaxConns: 10})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)

		logBuf = &bytes.Buffer{}
		svc = agentapi.New(st, nil, slog.New(slog.NewTextHandler(logBuf, nil)), testSchemaRegistry())
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

	mint := func(ctx context.Context, org *sqlc.Org, roles ...string) (collectorv1connect.CollectorServiceClient, pgtype.UUID) {
		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		secret := base64.URLEncoding.EncodeToString(raw)
		hash := sha256.Sum256([]byte(secret))
		if roles == nil {
			roles = []string{}
		}
		rolesJSON, err := json.Marshal(roles)
		Expect(err).NotTo(HaveOccurred())
		params := sqlc.CreateAgentTokenParams{
			Name: "t", TokenHash: hash[:], CreatedBy: "test",
			Clusters: json.RawMessage(`[]`), Roles: rolesJSON, MayClaim: true,
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

	poll := func(ctx context.Context, c collectorv1connect.CollectorServiceClient, id, cluster, role string) error {
		_, err := c.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
			Id: id, LocalAttributes: map[string]string{"cluster": cluster, "role": role},
		}))
		return err
	}

	collector := func(ctx context.Context, cluster, role string) sqlc.Collector {
		coll, err := st.Queries.GetCollectorByClusterAndRole(ctx, sqlc.GetCollectorByClusterAndRoleParams{Name: cluster, Role: role})
		Expect(err).NotTo(HaveOccurred())
		return coll
	}

	claim := func(ctx context.Context, name string, org sqlc.Org) {
		cl, err := st.Queries.UpsertCluster(ctx, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cl.ID, OrgID: org.ID})).To(Succeed())
	}

	It("records token_unbound on the collector, and the next successful poll clears it", func(ctx context.Context) {
		claim(ctx, "prod", orgA)
		c, id := mint(ctx, nil)

		Expect(connect.CodeOf(poll(ctx, c, "inst-1", "prod", "metrics"))).To(Equal(connect.CodePermissionDenied))
		coll := collector(ctx, "prod", "metrics")
		Expect(coll.LastRefusedReason.String).To(Equal("token_unbound"))
		Expect(coll.LastRefusedAt.Valid).To(BeTrue())

		_, err := st.Queries.BindAgentToken(ctx, sqlc.BindAgentTokenParams{
			ID: id, OrgID: orgA.ID, Clusters: json.RawMessage(`[]`), Roles: json.RawMessage(`[]`),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(poll(ctx, c, "inst-1", "prod", "metrics")).To(Succeed())
		coll = collector(ctx, "prod", "metrics")
		Expect(coll.LastRefusedReason.Valid).To(BeFalse(), "a successful poll clears the refusal")
		Expect(coll.LastRefusedAt.Valid).To(BeFalse())
	})

	It("records claim_denied and role_denied, and RegisterCollector records too", func(ctx context.Context) {
		claim(ctx, "b-prod", orgB)
		cross, _ := mint(ctx, &orgA)
		Expect(connect.CodeOf(poll(ctx, cross, "inst-x", "b-prod", "metrics"))).To(Equal(connect.CodePermissionDenied))
		Expect(collector(ctx, "b-prod", "metrics").LastRefusedReason.String).To(Equal("claim_denied"))

		metricsOnly, _ := mint(ctx, &orgA, "metrics")
		_, err := metricsOnly.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
			Id: "inst-y", LocalAttributes: map[string]string{"cluster": "a-prod", "role": "logs"},
		}))
		Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied))
		Expect(collector(ctx, "a-prod", "logs").LastRefusedReason.String).To(Equal("role_denied"))
	})

	It("writes the row at most once per window per reason, however often the fleet is refused", func(ctx context.Context) {
		claim(ctx, "prod", orgA)
		c, _ := mint(ctx, nil)
		Expect(connect.CodeOf(poll(ctx, c, "inst-1", "prod", "metrics"))).To(Equal(connect.CodePermissionDenied))
		first := collector(ctx, "prod", "metrics").LastRefusedAt
		Expect(first.Valid).To(BeTrue())

		for i := range 20 {
			inst := "inst-" + string(rune('a'+i))
			Expect(connect.CodeOf(poll(ctx, c, inst, "prod", "metrics"))).To(Equal(connect.CodePermissionDenied))
		}
		Expect(collector(ctx, "prod", "metrics").LastRefusedAt.Time).To(Equal(first.Time),
			"refusals inside the window do not rewrite the row")

		// Past the window (here: a zero window), a refusal refreshes it.
		agentapi.SetCollectorRefusalWindow(svc, 0)
		Expect(connect.CodeOf(poll(ctx, c, "inst-1", "prod", "metrics"))).To(Equal(connect.CodePermissionDenied))
		Expect(collector(ctx, "prod", "metrics").LastRefusedAt.Time).To(BeTemporally(">", first.Time))
	})

	// 0.22 -> 0.23 upgrade walkthrough: during the rolling upgrade, collectors
	// polling an old pod got "internal: upserting instance: ERROR: there is no
	// unique or exclusion constraint matching the ON CONFLICT specification
	// (SQLSTATE 42P10)". A store error never reaches the agent verbatim: a
	// schema mismatch (a rollout in progress) is a retryable Unavailable, any
	// other one a generic Internal, and the detail is logged server-side.
	Describe("an unexpected store error", func() {
		BeforeEach(func(ctx context.Context) { claim(ctx, "prod", orgA) })

		// RAW-SQL-OK: test-only schema surgery reproducing a migration the
		// running binary does not match.
		alter := func(ctx context.Context, stmt string) {
			_, err := st.Pool().Exec(ctx, stmt)
			Expect(err).NotTo(HaveOccurred())
		}

		expectNoSQL := func(err error) {
			Expect(err.Error()).NotTo(Or(ContainSubstring("SQLSTATE"), ContainSubstring("ON CONFLICT"),
				ContainSubstring("upserting"), ContainSubstring("constraint")))
		}

		It("answers a schema mismatch (42P10) Unavailable with a generic message, logging the detail", func(ctx context.Context) {
			alter(ctx, `ALTER TABLE collector_instances DROP CONSTRAINT collector_instances_pkey`)
			c, _ := mint(ctx, &orgA)

			err := poll(ctx, c, "inst-1", "prod", "metrics")
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable))
			expectNoSQL(err)
			_, err = c.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id: "inst-1", LocalAttributes: map[string]string{"cluster": "prod", "role": "metrics"},
			}))
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable))
			expectNoSQL(err)
			Expect(logBuf.String()).To(ContainSubstring("42P10"), "the detail is logged server-side")
		})

		It("answers an undefined column (42703) Unavailable", func(ctx context.Context) {
			alter(ctx, `ALTER TABLE collectors RENAME COLUMN labels TO labels_wtgaps`)
			c, _ := mint(ctx, &orgA)
			err := poll(ctx, c, "inst-1", "prod", "metrics")
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnavailable))
			expectNoSQL(err)
		})

		It("answers any other store error Internal with a generic message", func(ctx context.Context) {
			alter(ctx, `ALTER TABLE collector_instances ADD CONSTRAINT wtgaps_no_boom CHECK (id <> 'boom') NOT VALID`)
			c, _ := mint(ctx, &orgA)
			err := poll(ctx, c, "boom", "prod", "metrics")
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeInternal))
			Expect(err.Error()).To(Equal("internal: internal error"))
			Expect(logBuf.String()).To(ContainSubstring("wtgaps_no_boom"))
		})
	})
})
