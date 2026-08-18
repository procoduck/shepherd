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

var _ = Describe("shepherd.mgmt.v1.SimulateService", Label("integration"), func() {
	var (
		ctx          context.Context
		cancel       context.CancelFunc
		st           *store.Store
		server       *httptest.Server
		orgID        string
		adminCookie  *http.Cookie
		readerCookie *http.Cookie
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "simulate-rpc-org", DisplayName: "Simulate RPC Org",
			AdminGroupID: "simulate-admin-grp", ReaderGroupID: pgtype.Text{String: "simulate-reader-grp", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()

		adminCookie = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "simulate-admin-grp")}
		readerCookie = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "simulate-reader-grp")}

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	It("traces a relabel rule over the Connect handler (happy path)", func() {
		body := map[string]any{
			"org_id": orgID,
			"rules": []map[string]any{
				{"source_labels": []string{"__meta_kubernetes_namespace"}, "target_label": "namespace", "action": "replace", "regex": "(.*)"},
			},
			"sample_targets": []map[string]any{
				{"__meta_kubernetes_namespace": "production"},
			},
		}
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.SimulateService/SimulateRelabel", adminCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		var result map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
		traces, ok := result["traces"].([]any)
		Expect(ok).To(BeTrue())
		Expect(traces).To(HaveLen(1))
		trace := traces[0].(map[string]any)        //nolint:errcheck // asserted shape
		output := trace["output"].(map[string]any) //nolint:errcheck // asserted shape
		Expect(output["namespace"]).To(Equal("production"))
	})

	It("denies SimulateRelabel for a session without org-admin access", func() {
		body := map[string]any{"org_id": orgID, "rules": []any{}, "sample_targets": []any{}}
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.SimulateService/SimulateRelabel", readerCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
		Expect(connectErrorCode(resp)).To(Equal("permission_denied"))
	})

	It("maps an invalid relabel action to invalid_argument", func() {
		body := map[string]any{
			"org_id": orgID,
			"rules": []map[string]any{
				{"action": "not-a-real-action"},
			},
			"sample_targets": []map[string]any{{"foo": "bar"}},
		}
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.SimulateService/SimulateRelabel", adminCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(connectErrorCode(resp)).To(Equal("invalid_argument"))
	})
})
