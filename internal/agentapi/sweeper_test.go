package agentapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/testutil"
)

var _ = Describe("Sweeper", Label("integration"), func() {
	var (
		ctx      context.Context
		st       *store.Store
		sharedPG *testutil.SharedPostgres
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		sharedPG, err = testutil.StartSharedPostgres(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(store.MigrateUp(ctx, sharedPG.RootURL)).To(Succeed())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		st.Close()
		Expect(sharedPG.Terminate(ctx)).To(Succeed())
	})

	createSession := func(id string, expiresAt time.Time) {
		_, err := st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID:             id,
			UserOid:        "user-" + id,
			Email:          id + "@example.com",
			DisplayName:    id,
			GroupIds:       json.RawMessage(`[]`),
			ExpiresAt:      pgtype.Timestamptz{Time: expiresAt, Valid: true},
			IDTokenExpires: pgtype.Timestamptz{Time: expiresAt, Valid: true},
			Source:         "test",
		})
		Expect(err).NotTo(HaveOccurred())
	}

	It("sweeper deletes expired sessions and preserves live ones", func() {
		now := time.Now()
		createSession("test-session-a", now.Add(-time.Hour))
		createSession("test-session-b", now.Add(time.Hour))
		createSession("test-session-c", now.Add(-time.Second))

		n, err := st.Queries.DeleteExpiredSessions(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(int64(2)))

		_, err = st.Queries.GetSessionByID(ctx, "test-session-a")
		Expect(err).To(HaveOccurred())
		_, err = st.Queries.GetSessionByID(ctx, "test-session-c")
		Expect(err).To(HaveOccurred())
		_, err = st.Queries.GetSessionByID(ctx, "test-session-b")
		Expect(err).NotTo(HaveOccurred())
	})

	It("sweeper deletes expired beacon inventory and preserves fresh rows", func() {
		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		hash := sha256.Sum256(raw)
		tok, err := st.Queries.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{
			Name: "sweeper-beacon-token", TokenHash: hash[:], CreatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		insertAt := func(instance string, ts time.Time) {
			// RAW-SQL-OK: backdating last_seen for a sweep test — no sqlc
			// query exposes an explicit timestamp (UpsertBeaconComponent
			// always writes now()), and this is a _test.go file anyway.
			_, err := st.Pool().Exec(ctx,
				`INSERT INTO beacon_inventory (token_id, instance_label, component_name, healthy, last_seen)
				 VALUES ($1, $2, 'pipe_x', true, $3)`, tok.ID, instance, ts)
			Expect(err).NotTo(HaveOccurred())
		}
		now := time.Now()
		insertAt("stale-instance", now.Add(-time.Hour))
		insertAt("fresh-instance", now)

		sw := NewSweeper(st, &config.AgentConfig{InactiveAfter: time.Hour, DeleteAfter: 24 * time.Hour, SweepInterval: time.Minute}, slog.Default())
		sw.sweep(ctx)

		rows, err := st.Queries.ListBeaconInventoryByToken(ctx, tok.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].InstanceLabel).To(Equal("fresh-instance"))
	})

	It("an expired but undeleted session is rejected by middleware", func() {
		createSession("test-session-expired", time.Now().Add(-time.Second))
		h := auth.NewLocalAdmin(&config.Config{}, st, slog.Default())
		var session *auth.Session
		next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			session = auth.SessionFromCtx(r.Context())
		})

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: "shepherd_session", Value: "test-session-expired"})
		h.SessionMiddleware(next).ServeHTTP(httptest.NewRecorder(), req)

		Expect(session).To(BeNil())
	})
})
