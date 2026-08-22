package grafana

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/testutil"
)

// sharedPG is a single Postgres container shared across every spec in this
// suite, matching internal/agentapi's and internal/gitsync's established
// convention (service_test.go, gitsync_suite_test.go) rather than starting
// one per spec.
var sharedPG *testutil.SharedPostgres

func TestGrafana(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Grafana Integration Suite")
}

var _ = SynchronizedBeforeSuite(func() []byte {
	var err error
	sharedPG, err = testutil.StartSharedPostgres(context.Background())
	Expect(err).NotTo(HaveOccurred())
	Expect(store.MigrateUp(context.Background(), sharedPG.RootURL)).To(Succeed())
	return nil
}, func(_ []byte) {})

var _ = SynchronizedAfterSuite(func() {}, func() {
	if sharedPG != nil {
		Expect(sharedPG.Terminate(context.Background())).To(Succeed())
	}
})

// testEncryptor returns a crypto.Encryptor with a fixed, valid 32-byte key
// — deterministic across runs (unlike a random key) so a failing
// assertion's ciphertext is reproducible if ever pasted into a bug report.
func testEncryptor() *crypto.Encryptor {
	key := base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")[:32])
	enc, err := crypto.NewEncryptor(key)
	Expect(err).NotTo(HaveOccurred())
	return enc
}

var _ = Describe("ConnectionStore", Label("integration"), func() {
	var (
		ctx   context.Context
		st    *store.Store
		pool  *pgxpool.Pool
		orgID pgtype.UUID
	)

	BeforeEach(func() {
		ctx = context.Background()
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { st.Close() })

		pool, err = pgxpool.New(ctx, dbURL)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(pool.Close)

		org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name:         "test-org",
			DisplayName:  "Test Org",
			AdminGroupID: "test-org-admins",
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = org.ID
	})

	Describe("Set then Client", func() {
		It("round-trips the token: the built Client authenticates with exactly what was stored", func() {
			cs := NewConnectionStore(st, testEncryptor())

			var gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				writeDSQueryResponse(w, map[string][][]any{})
			}))
			defer srv.Close()

			Expect(cs.Set(ctx, orgID, srv.URL, "the-service-account-token")).To(Succeed())

			client, err := cs.Client(ctx, orgID, time.Second)
			Expect(err).NotTo(HaveOccurred())

			_, err = client.QueryDatasource(ctx, QueryDatasourceRequest{DatasourceUID: "ds1", From: "now-5m", To: "now"})
			Expect(err).NotTo(HaveOccurred())
			Expect(gotAuth).To(Equal("Bearer the-service-account-token"))
		})

		It("Set is an upsert: a second Set for the same org replaces base_url and token rather than erroring or duplicating", func() {
			cs := NewConnectionStore(st, testEncryptor())

			srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeDSQueryResponse(w, nil) }))
			defer srv1.Close()
			var gotAuth2 string
			srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth2 = r.Header.Get("Authorization")
				writeDSQueryResponse(w, nil)
			}))
			defer srv2.Close()

			Expect(cs.Set(ctx, orgID, srv1.URL, "token-1")).To(Succeed())
			Expect(cs.Set(ctx, orgID, srv2.URL, "token-2")).To(Succeed())

			info, err := cs.Info(ctx, orgID)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.BaseURL).To(Equal(srv2.URL), "second Set should have replaced base_url, not added a row Info can't see")

			client, err := cs.Client(ctx, orgID, time.Second)
			Expect(err).NotTo(HaveOccurred())
			_, err = client.QueryDatasource(ctx, QueryDatasourceRequest{DatasourceUID: "ds1", From: "now-5m", To: "now"})
			Expect(err).NotTo(HaveOccurred())
			Expect(gotAuth2).To(Equal("Bearer token-2"), "the client built after the second Set should authenticate with the second token")
		})
	})

	// Plan §5's must-not, D7's own
	// "Grafana absent means no outcome verification, never reduced
	// function" — proven directly against a real database with NO
	// grafana_connections row for this org at all, not a mock standing in
	// for "not configured".
	//
	// Red run: in ConnectionStore.row, delete the
	// `if errors.Is(err, pgx.ErrNoRows) { return connectionRow{}, ErrNotConfigured }`
	// branch, falling through to the generic wrap
	// (`fmt.Errorf("grafana: reading connection: %w", err)`) for every
	// error including "no rows". Observed failure: this spec's
	// `errors.Is(err, ErrNotConfigured)` expectation fails — Client's
	// error no longer satisfies ErrNotConfigured — AND, one level up,
	// VerifyForOrg's `errors.Is(err, ErrNotConfigured)` branch (verify.go)
	// stops matching too, so its final fallback branch still reaches
	// OutcomeUnknown but with the wrong Reason text ("resolving Grafana
	// connection: ..." instead of "no Grafana connection configured for
	// this org") — a real behavior change this spec is written to catch
	// even though OutcomeUnknown happens to survive it.
	Describe("Grafana absent (no configured connection)", func() {
		It("ConnectionStore.Client reports ErrNotConfigured, not a generic error", func() {
			cs := NewConnectionStore(st, testEncryptor())
			_, err := cs.Client(ctx, orgID, time.Second)
			Expect(errors.Is(err, ErrNotConfigured)).To(BeTrue(), "err = %v", err)
		})

		It("VerifyForOrg reports OutcomeUnknown with an explanatory reason, never an error and never OutcomeNotArrived", func() {
			cs := NewConnectionStore(st, testEncryptor())
			result := VerifyForOrg(ctx, cs, orgID, QueryDatasourceRequest{DatasourceUID: "ds1", From: "now-5m", To: "now"}, time.Second)
			Expect(result.Outcome).To(Equal(OutcomeUnknown))
			Expect(result.Reason).To(ContainSubstring("no Grafana connection configured"))
		})

		It("ExploreURL still works with no connection configured at all — it needs no stored connection, let alone a token", func() {
			url, err := ExploreURL("https://grafana.example.com", "ds1", nil, "now-5m", "now")
			Expect(err).NotTo(HaveOccurred())
			Expect(url).To(ContainSubstring("/explore"))
		})
	})

	Describe("Encryption not configured", func() {
		It("Set refuses with ErrEncryptionUnavailable rather than storing a plaintext token", func() {
			cs := NewConnectionStore(st, nil)
			err := cs.Set(ctx, orgID, "https://grafana.example.com", "some-token")
			Expect(errors.Is(err, ErrEncryptionUnavailable)).To(BeTrue(), "err = %v", err)
		})

		It("Client refuses with ErrEncryptionUnavailable even if a row already exists", func() {
			// Write a row directly (bypassing Set, which itself would
			// refuse) to isolate exactly the Client-with-nil-crypto case.
			encCS := NewConnectionStore(st, testEncryptor())
			Expect(encCS.Set(ctx, orgID, "https://grafana.example.com", "some-token")).To(Succeed())

			noCryptoCS := NewConnectionStore(st, nil)
			_, err := noCryptoCS.Client(ctx, orgID, time.Second)
			Expect(errors.Is(err, ErrEncryptionUnavailable)).To(BeTrue(), "err = %v", err)
		})
	})

	// The base_url CHECK constraint (0011_grafana_connections.up.sql) is
	// defense in depth for exactly the Go-level check validateBaseURL
	// already performs — this spec proves the DB-level copy independently
	// enforces the same rule by writing directly with raw SQL, bypassing
	// ConnectionStore.Set (and therefore validateBaseURL) entirely.
	//
	// Red run: comment out the CHECK clause in
	// 0011_grafana_connections.up.sql, re-run `make e2e` (migrations are
	// baked into the shared template at suite start, so a schema change
	// needs a fresh container) — this spec's `Expect(err).To(HaveOccurred())`
	// then fails because the raw INSERT below succeeds.
	Describe("base_url CHECK constraint (defense in depth)", func() {
		It("rejects a scheme-less base_url even when written directly, bypassing Go-level validation", func() {
			_, err := pool.Exec(ctx,
				`INSERT INTO grafana_connections (org_id, base_url, token_enc) VALUES ($1, $2, $3)`,
				orgID, "not-a-url", []byte("irrelevant"))
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("Delete", func() {
		It("is idempotent and returns Grafana-absent behavior for the org afterward", func() {
			cs := NewConnectionStore(st, testEncryptor())
			Expect(cs.Set(ctx, orgID, "https://grafana.example.com", "tok")).To(Succeed())

			Expect(cs.Delete(ctx, orgID)).To(Succeed())
			Expect(cs.Delete(ctx, orgID)).To(Succeed(), "deleting an already-absent connection must not error")

			_, err := cs.Client(ctx, orgID, time.Second)
			Expect(errors.Is(err, ErrNotConfigured)).To(BeTrue())
		})
	})
})
