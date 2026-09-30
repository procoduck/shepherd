package mgmtapi_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// FleetService.ListAttributes feeds matcher suggestions — the pipeline
// editor's matcher input and the MCP list_fleet_attributes tool. It must offer
// exactly the keys pipeline matching evaluates for the org (#139): cluster and
// role always; admin labels only with allow_label_matching; agent-reported
// local_attributes only with allow_local_attribute_matching; never a reserved
// key. Suggesting a key matching ignores sends an author to a matcher that can
// never hit.
var _ = Describe("shepherd.mgmt.v1.FleetService/ListAttributes", Label("integration"), func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
		st     *store.Store
		server *httptest.Server
		orgID  pgtype.UUID
		reader *http.Cookie
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "attrs-org", DisplayName: "Attrs Org", AdminGroupID: "attrs-admin",
			ReaderGroupID: pgtype.Text{String: "attrs-reader", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID

		cluster, err := st.Queries.UpsertCluster(ctx, "attrs-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgID})).To(Succeed())
		coll, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())

		// An admin label, plus a reserved key written straight to the row the
		// way a pre-#139 label could have been (SetCollectorLabel refuses it now).
		_, err = st.Queries.SetCollectorLabel(ctx, sqlc.SetCollectorLabelParams{ID: coll.ID, LabelKey: "team", LabelValue: "payments"})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Pool().Exec(ctx, `UPDATE collectors SET labels = labels || '{"os":"linux"}'::jsonb WHERE id = $1`, coll.ID)
		Expect(err).NotTo(HaveOccurred())

		// Agent-reported attributes: a mixed-case key (matching lowercases it)
		// and a reserved-prefix key.
		_, err = st.Queries.UpsertCollectorInstance(ctx, sqlc.UpsertCollectorInstanceParams{
			ID: "attrs-inst", CollectorID: coll.ID, Name: "node-a",
			LocalAttributes: json.RawMessage(`{"Zone":"eu-1","collector.id":"x"}`),
		})
		Expect(err).NotTo(HaveOccurred())

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		server = httptest.NewServer(newRPCWiringRouter(st, auth.NewLocalAdmin(cfg, st, slog.Default()), cfg))
		reader = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "attrs-reader")}
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	setFlags := func(labels, localAttrs bool) {
		o, err := st.Queries.GetOrgByID(ctx, orgID)
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.UpdateOrg(ctx, sqlc.UpdateOrgParams{
			ID: orgID, DisplayName: o.DisplayName, AdminGroupID: o.AdminGroupID,
			ReaderGroupID: o.ReaderGroupID, EditorGroupID: o.EditorGroupID,
			AllowExperimentalComponents: o.AllowExperimentalComponents,
			AllowLabelMatching:          labels,
			AllowLocalAttributeMatching: localAttrs,
		})
		Expect(err).NotTo(HaveOccurred())
	}

	attributes := func() map[string]any {
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.FleetService/ListAttributes", reader, map[string]any{"orgId": orgID.String()})
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var out struct {
			Attributes map[string]any `json:"attributes"`
		}
		Expect(json.NewDecoder(resp.Body).Decode(&out)).To(Succeed())
		return out.Attributes
	}

	It("offers only cluster and role, with their values, when both matching flags are off", func() {
		attrs := attributes()
		Expect(attrs).To(HaveLen(2))
		Expect(attrs).To(HaveKeyWithValue("cluster", ConsistOf("attrs-cluster")))
		Expect(attrs).To(HaveKeyWithValue("role", ConsistOf("metrics")))
	})

	It("adds admin label keys only when allow_label_matching is on", func() {
		setFlags(true, false)
		attrs := attributes()
		Expect(attrs).To(HaveKeyWithValue("team", ConsistOf("payments")))
		Expect(attrs).NotTo(HaveKey("zone"))
		Expect(attrs).NotTo(HaveKey("os"), "a reserved key must never be suggested")
	})

	It("adds agent-reported keys, lowercased as matching sees them, only when allow_local_attribute_matching is on", func() {
		setFlags(false, true)
		attrs := attributes()
		Expect(attrs).To(HaveKeyWithValue("zone", ConsistOf("eu-1")))
		Expect(attrs).NotTo(HaveKey("Zone"))
		Expect(attrs).NotTo(HaveKey("team"))
		Expect(attrs).NotTo(HaveKey("collector.id"), "a reserved key must never be suggested")
	})
})
