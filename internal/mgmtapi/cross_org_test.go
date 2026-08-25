package mgmtapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Cross-org isolation for the by-id handlers.
//
// Neither the Connect interceptor nor the REST middleware can enforce this:
// both authorize against the org NAMED IN THE REQUEST. That proves the caller
// holds a role in that org and says nothing about the id they passed alongside
// it, so a by-id handler that does not recheck ownership is a cross-tenant
// read or write for any authenticated member of any org.
//
// Every case below is a caller who is legitimately privileged in org A naming
// org A, and passing an id belonging to org B. The correct answer is always
// NotFound -- not PermissionDenied, which would confirm the id exists.
var _ = Describe("cross-org isolation of by-id handlers", Label("integration"), func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
		st     *store.Store
		server *httptest.Server
		orgA   pgtype.UUID
		orgB   pgtype.UUID
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		a, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "org-a", DisplayName: "Org A", AdminGroupID: "a-admin",
			ReaderGroupID: pgtype.Text{String: "a-reader", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgA = a.ID

		b, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "org-b", DisplayName: "Org B", AdminGroupID: "b-admin",
		})
		Expect(err).NotTo(HaveOccurred())
		orgB = b.ID

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		server = httptest.NewServer(newRPCWiringRouter(st, auth.NewLocalAdmin(cfg, st, slog.Default()), cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	sessionIn := func(groups ...string) *http.Cookie {
		groupsJSON, err := json.Marshal(groups)
		Expect(err).NotTo(HaveOccurred())
		id := fmt.Sprintf("cross-org-%d", time.Now().UnixNano())
		_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: id, UserOid: "oid", Email: "u@example.com", DisplayName: "U",
			GroupIds:  groupsJSON,
			ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
			Source:    "test",
		})
		Expect(err).NotTo(HaveOccurred())
		return &http.Cookie{Name: "shepherd_session", Value: id}
	}

	post := func(procedure string, body map[string]any, cookie *http.Cookie) (int, string) {
		buf, err := json.Marshal(body)
		Expect(err).NotTo(HaveOccurred())
		req, err := http.NewRequest(http.MethodPost, server.URL+procedure, strings.NewReader(string(buf)))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		raw, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		return resp.StatusCode, string(raw)
	}

	// collectorInB returns a collector owned by org B.
	collectorInB := func(name string) pgtype.UUID {
		cluster, err := st.Queries.UpsertCluster(ctx, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgB})).To(Succeed())
		coll, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())
		return coll.ID
	}

	It("refuses to read another org's collector", func() {
		victim := collectorInB("b-cluster-get")
		code, body := post("/shepherd.mgmt.v1.FleetService/GetCollector",
			map[string]any{"orgId": orgA.String(), "id": victim.String()}, sessionIn("a-reader"))
		Expect(code).NotTo(Equal(http.StatusOK), "a reader in org A read org B's collector: %s", body)
	})

	It("refuses to read another org's served config", func() {
		// The served config is the highest-value target: it carries destination
		// URLs, tenant ids and any credentials embedded in pipeline contents.
		victim := collectorInB("b-cluster-served")
		code, body := post("/shepherd.mgmt.v1.FleetService/GetServedConfig",
			map[string]any{"orgId": orgA.String(), "id": victim.String()}, sessionIn("a-reader"))
		Expect(code).NotTo(Equal(http.StatusOK), "a reader in org A read org B's served config: %s", body)
	})

	It("refuses to grant its own group access to another org's collector", func() {
		// A cross-tenant WRITE: group_assignments feed the reader-floor
		// fallback, so this would be org A granting itself standing access.
		victim := collectorInB("b-cluster-assign")
		code, body := post("/shepherd.mgmt.v1.FleetService/CreateAssignment",
			map[string]any{"orgId": orgA.String(), "collectorId": victim.String(), "groupId": "a-admin"},
			sessionIn("a-admin"))
		Expect(code).NotTo(Equal(http.StatusOK), "org A assigned a group to org B's collector: %s", body)
	})

	It("refuses to delete another org's git credential", func() {
		cred, err := st.Queries.CreateGitCredential(ctx, sqlc.CreateGitCredentialParams{
			OrgID: orgB, Name: "b-cred", Kind: "pat",
			Username: pgtype.Text{String: "u", Valid: true}, ClientSecretEnc: []byte("x"),
			ProviderConfig: json.RawMessage(`{}`),
		})
		Expect(err).NotTo(HaveOccurred())

		code, body := post("/shepherd.mgmt.v1.GitOpsService/DeleteCredential",
			map[string]any{"orgId": orgA.String(), "id": cred.ID.String()}, sessionIn("a-admin"))
		Expect(code).NotTo(Equal(http.StatusOK), "org A deleted org B's credential: %s", body)

		// And it must still be there -- a refusal that already deleted the row
		// is not a refusal.
		_, err = st.Queries.GetGitCredentialByID(ctx, cred.ID)
		Expect(err).NotTo(HaveOccurred(), "the credential was deleted despite the request being refused")
	})
})
