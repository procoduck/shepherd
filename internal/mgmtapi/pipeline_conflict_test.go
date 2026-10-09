package mgmtapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// F1 (2026-10-09 walkthrough): a stale editor tab saved its old copy over a
// newer server state with a 200 — a destination change had re-rendered the
// wizard pipeline, and the untouched tab's Save turned it into a hand edit
// shipping to the old URL. UpdatePipeline's expected_revision makes the save
// conditional on the revision the editor loaded; these specs drive it over
// the real Connect wire, against the real Postgres row lock.
var _ = Describe("F1: UpdatePipeline expected_revision (optimistic concurrency)", Label("integration"), func() {
	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		server      *httptest.Server
		orgID       pgtype.UUID
		adminCookie *http.Cookie
		pipelineID  string
	)

	update := func(body map[string]any) (*http.Response, map[string]any) {
		body["orgId"] = orgID.String()
		body["id"] = pipelineID
		if _, ok := body["name"]; !ok {
			body["name"] = "f1-pipeline"
		}
		if _, ok := body["matchers"]; !ok {
			body["matchers"] = []string{`env="prod"`}
		}
		resp := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline", body, adminCookie)
		return resp, g11DecodeBody(resp)
	}
	revisionCount := func() int {
		id, err := scanTestUUID(pipelineID)
		Expect(err).NotTo(HaveOccurred())
		revs, err := st.Queries.ListPipelineRevisions(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		return len(revs)
	}
	storedContents := func() string {
		id, err := scanTestUUID(pipelineID)
		Expect(err).NotTo(HaveOccurred())
		p, err := st.Queries.GetPipelineByID(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		return p.Contents
	}
	updateAudits := func() int {
		n, err := st.Queries.CountAuditLog(ctx, sqlc.CountAuditLogParams{Column1: orgID, Column3: "pipeline.update"})
		Expect(err).NotTo(HaveOccurred())
		return int(n)
	}

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 10}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "f1-org", DisplayName: "F1 Org", AdminGroupID: "f1-admin-group",
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
		adminCookie = g11Session(ctx, st, "f1-admin", []string{"f1-admin-group"})

		// Created through the RPC, so it carries revision 1 like any
		// pipeline a person makes.
		resp := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/CreatePipeline", map[string]any{
			"orgId": orgID.String(), "name": "f1-pipeline", "contents": "// v1",
			"matchers": []string{`env="prod"`}, "source": "ui",
		}, adminCookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		created := g11DecodeBody(resp)
		id, ok := created["id"].(string)
		Expect(ok).To(BeTrue(), "%v", created)
		pipelineID = id
		Expect(revisionCount()).To(Equal(1))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	It("reports the current revision on GetPipeline", func() {
		resp := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/GetPipeline", map[string]any{
			"orgId": orgID.String(), "id": pipelineID,
		}, adminCookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(g11DecodeBody(resp)["revision"]).To(BeEquivalentTo(1))
	})

	It("applies an update whose expected_revision is current and returns the new revision", func() {
		resp, body := update(map[string]any{"contents": "// v2", "expectedRevision": 1})
		Expect(resp.StatusCode).To(Equal(http.StatusOK), "%v", body)
		Expect(body["revision"]).To(BeEquivalentTo(2))
		Expect(storedContents()).To(Equal("// v2"))
	})

	It("refuses the second of two updates carrying the same expected_revision, writing nothing", func() {
		first, _ := update(map[string]any{"contents": "// tab A", "expectedRevision": 1})
		Expect(first.StatusCode).To(Equal(http.StatusOK))
		auditsBefore := updateAudits()

		second, body := update(map[string]any{"contents": "// stale tab B", "expectedRevision": 1})
		Expect(second.StatusCode).To(Equal(http.StatusConflict))
		Expect(body["code"]).To(Equal("aborted"))
		Expect(body["message"]).To(ContainSubstring("revision 1, now 2"))
		Expect(body["message"]).To(ContainSubstring("reload"))

		Expect(storedContents()).To(Equal("// tab A"))
		Expect(revisionCount()).To(Equal(2))
		Expect(updateAudits()).To(Equal(auditsBefore))
	})

	It("keeps today's behaviour when expected_revision is omitted", func() {
		first, _ := update(map[string]any{"contents": "// tab A", "expectedRevision": 1})
		Expect(first.StatusCode).To(Equal(http.StatusOK))

		resp, body := update(map[string]any{"contents": "// unconditional"})
		Expect(resp.StatusCode).To(Equal(http.StatusOK), "%v", body)
		Expect(storedContents()).To(Equal("// unconditional"))
		Expect(revisionCount()).To(Equal(3))
	})

	It("lets exactly one of several concurrent updates from the same revision win", func() {
		const writers = 6
		var wg sync.WaitGroup
		statuses := make([]int, writers)
		for i := range writers {
			wg.Go(func() {
				defer GinkgoRecover()
				buf, err := json.Marshal(map[string]any{
					"orgId": orgID.String(), "id": pipelineID, "name": "f1-pipeline",
					"matchers": []string{`env="prod"`},
					"contents": fmt.Sprintf("// writer %d", i), "expectedRevision": 1,
				})
				Expect(err).NotTo(HaveOccurred())
				var body map[string]any
				Expect(json.Unmarshal(buf, &body)).To(Succeed())
				resp := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline", body, adminCookie)
				statuses[i] = resp.StatusCode
				_ = g11DecodeBody(resp)
			})
		}
		wg.Wait()

		wins := 0
		winner := -1
		for i, code := range statuses {
			switch code {
			case http.StatusOK:
				wins++
				winner = i
			case http.StatusConflict:
			default:
				Fail(fmt.Sprintf("writer %d: unexpected status %d", i, code))
			}
		}
		Expect(wins).To(Equal(1), "statuses: %v", statuses)
		Expect(storedContents()).To(Equal(fmt.Sprintf("// writer %d", winner)))
		Expect(revisionCount()).To(Equal(2))
	})

	It("writes no revision and no audit row for a save that changes nothing", func() {
		auditsBefore := updateAudits()
		resp, body := update(map[string]any{"contents": "// v1", "expectedRevision": 1})
		Expect(resp.StatusCode).To(Equal(http.StatusOK), "%v", body)
		Expect(body["revision"]).To(BeEquivalentTo(1))
		Expect(revisionCount()).To(Equal(1))
		Expect(updateAudits()).To(Equal(auditsBefore))

		// A real change after it still lands as revision 2.
		resp, body = update(map[string]any{"contents": "// v2", "expectedRevision": 1})
		Expect(resp.StatusCode).To(Equal(http.StatusOK), "%v", body)
		Expect(body["revision"]).To(BeEquivalentTo(2))
	})

	// The handler decides whether Stage 3 applies from the row it read before
	// the lock. A pipeline enabled in between must not take the update's
	// content into the served config unchecked.
	It("refuses an update whose pipeline was enabled after Stage 3 was skipped", func() {
		id, err := scanTestUUID(pipelineID)
		Expect(err).NotTo(HaveOccurred())

		// Hold the row lock, so the update reads the (disabled) row, skips
		// Stage 3, and then waits at GetPipelineForUpdate.
		tx, err := st.Pool().Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // explicit Commit below is the real end
		txQ := st.Queries.WithTx(tx)
		_, err = txQ.GetPipelineForUpdate(ctx, id)
		Expect(err).NotTo(HaveOccurred())

		type result struct {
			status int
			body   map[string]any
		}
		done := make(chan result, 1)
		go func() {
			defer GinkgoRecover()
			resp, body := update(map[string]any{"contents": "// unchecked", "expectedRevision": 1})
			done <- result{resp.StatusCode, body}
		}()
		Eventually(func() error {
			var pid int
			return st.Pool().QueryRow(ctx,
				`SELECT pid FROM pg_stat_activity
				 WHERE datname = current_database() AND wait_event_type = 'Lock' AND query ILIKE '%GetPipelineForUpdate%'`,
			).Scan(&pid)
		}, "5s", "20ms").Should(Succeed(), "the update never waited on the row lock")

		_, err = txQ.SetPipelineEnabled(ctx, sqlc.SetPipelineEnabledParams{ID: id, Enabled: true, UpdatedBy: "other"})
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit(ctx)).To(Succeed())

		r := <-done
		Expect(r.status).To(Equal(http.StatusConflict), "%v", r.body)
		Expect(r.body["code"]).To(Equal("aborted"))
		Expect(r.body["message"]).To(ContainSubstring("enabled while you were saving"))
		Expect(storedContents()).To(Equal("// v1"))
		Expect(revisionCount()).To(Equal(1))
	})
})

func scanTestUUID(s string) (pgtype.UUID, error) {
	var id pgtype.UUID
	err := id.Scan(s)
	return id, err
}
