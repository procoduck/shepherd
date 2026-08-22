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

// G13 (docs/gateway-tier-plan.md): "Audit records both halves of a
// delegated action... a machine action with no on-behalf-of is rejected or
// recorded as such, never silently attributed to a human." Shepherd's
// decision (documented on requireWriteAuthorized, machine_auth.go): REJECT
// a machine write with no on-behalf-of, rather than record it as
// unattributed. This suite proves both the rejection and — for the
// success path — that the audit row actually carries both halves: the
// machine actor (actor_type "service_account") AND the human
// (on_behalf_of), never collapsed into one or silently attributed to the
// human alone.
var _ = Describe("G13: two-part attribution", Label("integration"), func() {
	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		server      *httptest.Server
		orgID       pgtype.UUID
		applyID     string
		applySecret string
		pipelineID  string
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "g13-org", DisplayName: "G13 Org", AdminGroupID: "g13-admin-group"})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID

		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgID, Name: "g13-pipeline", Source: "ui", Matchers: json.RawMessage("[]"),
		})
		Expect(err).NotTo(HaveOccurred())
		pipelineID = p.ID.String()

		applySecret, applyID = g12MakeServiceAccount(ctx, st, orgID, "g13-apply", "apply")

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	It("REJECTS a machine write with no on-behalf-of, rather than recording it unattributed (the named G13 spec)", func() {
		resp := g12PostConnect(server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline", map[string]any{
			"orgId": orgID.String(), "id": pipelineID, "name": "g13-pipeline", "contents": "// no on-behalf-of",
		}, applyID, applySecret, "" /* no on-behalf-of */)
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		payload := g11DecodeBody(resp)
		Expect(payload["code"]).To(Equal("invalid_argument"))

		// The write must not have happened either — a rejected request is
		// not a partially-applied one.
		stored, err := st.Queries.GetPipelineByID(ctx, mustUUID(pipelineID))
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.Contents).NotTo(ContainSubstring("no on-behalf-of"))

		// No audit row for this rejected attempt — a rejected write leaves
		// no ambiguous trail to misread later.
		rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Limit: 100})
		Expect(err).NotTo(HaveOccurred())
		for _, r := range rows {
			Expect(r.Action).NotTo(Equal("pipeline.update"), "a rejected write must not still produce an audit row")
		}
	})

	It("records BOTH halves of a delegated write: the machine actor and the human it acted for", func() {
		resp := g12PostConnect(server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline", map[string]any{
			"orgId": orgID.String(), "id": pipelineID, "name": "g13-pipeline", "contents": "// delegated",
		}, applyID, applySecret, g12DelegatedPrincipal)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Limit: 100})
		Expect(err).NotTo(HaveOccurred())
		var found *sqlc.AuditLog
		for i := range rows {
			if rows[i].Action == "pipeline.update" {
				found = &rows[i]
				break
			}
		}
		Expect(found).NotTo(BeNil(), "expected an audit row for the successful delegated write")
		Expect(found.ActorType).To(Equal("service_account"))
		Expect(found.Actor).To(ContainSubstring("g13-apply"))
		Expect(found.OnBehalfOf.Valid).To(BeTrue())
		Expect(found.OnBehalfOf.String).To(Equal(g12DelegatedPrincipal))
	})

	// Rejecting an ABSENT on-behalf-of and verifying a PRESENT one are
	// different guarantees. Only the first existed originally: the header was
	// copied verbatim into the audit row, so any credential holder could
	// attribute a write to any human they cared to name — an org admin who
	// never touched the system, say — and that name became the permanent
	// record of who authorized the change. An unverified claim in a field
	// that reads as authoritative is worse than no attribution at all.
	//
	// Red run, executed: removing the EqualFold check against
	// DelegatedPrincipal in requireWriteAuthorized fails this spec with
	// `Expected <int>: 200 to equal <int>: 403` — the forged write succeeds
	// and is durably attributed to the impersonated human.
	It("REFUSES a machine write claiming to act for someone other than its own delegating principal", func() {
		const impersonated = "org-admin-who-never-touched-this@example.com"
		resp := g12PostConnect(server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline", map[string]any{
			"orgId": orgID.String(), "id": pipelineID, "name": "g13-pipeline", "contents": "// forged",
		}, applyID, applySecret, impersonated)
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden),
			"a service account named a human it was never delegated by, and the write was allowed — "+
				"that claim would have become the audit record of who authorized it")

		// And nothing was written or attributed under the forged name.
		rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Limit: 100})
		Expect(err).NotTo(HaveOccurred())
		for _, r := range rows {
			Expect(r.OnBehalfOf.String).NotTo(Equal(impersonated),
				"a refused write still produced an audit row naming the impersonated human")
		}
	})

	// The write gate is not the only place a claimed principal gets recorded.
	// requireWriteAuthorized runs on apply-gated writes; propose-safe
	// procedures never call it, yet some still stamp on_behalf_of into an
	// audit row via auditLogDetail. Verifying only at the write gate left
	// SimulateService.CreateRun able to record an unverified human — the same
	// impersonation, on a quieter procedure — which is why the check now runs
	// in the auth interceptor for every request instead.
	//
	// Red run, executed: moving the verification back inside
	// requireWriteAuthorized (so the interceptor only copies the header) fails
	// this spec — the forged claim is accepted and reaches CreateRun.
	It("REFUSES a forged on-behalf-of even on a propose-safe procedure that never reaches the write gate", func() {
		const impersonated = "someone-else@example.com"
		resp := g12PostConnect(server, "/shepherd.mgmt.v1.SimulateService/CreateRun", map[string]any{
			"orgId": orgID.String(),
		}, applyID, applySecret, impersonated)
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden),
			"a propose-safe procedure accepted a forged on-behalf-of; it can still write an audit row, "+
				"so the claim must be verified where it enters, not only where writes are gated")
	})

	// R6 asked how an agent's proposal is attributed. It was not: the MCP
	// propose tool composes ValidatePipeline and PreviewMatches, both reads,
	// so an agent proposing a change to raw Alloy text left no trace it had.
	// A machine calling this endpoint is proposing; a human calling it is
	// typing — so only the machine case is recorded, which keeps every
	// keystroke-level UI validation out of the audit log.
	//
	// Red run, executed: removing the serviceAccountFromCtx audit block in
	// ValidatePipeline fails this spec with `no pipeline.propose audit row`.
	It("records a machine's proposal, with both actors, while a human's validation records nothing", func() {
		resp := g12PostConnect(server, "/shepherd.mgmt.v1.PipelineService/ValidatePipeline", map[string]any{
			"orgId": orgID.String(), "name": "proposed", "contents": "// proposed by an agent",
		}, applyID, applySecret, g12DelegatedPrincipal)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Limit: 100})
		Expect(err).NotTo(HaveOccurred())
		var proposal *sqlc.AuditLog
		for i := range rows {
			if rows[i].Action == "pipeline.propose" {
				proposal = &rows[i]
				break
			}
		}
		Expect(proposal).NotTo(BeNil(), "no pipeline.propose audit row — an agent proposed and left no trace")
		Expect(proposal.ActorType).To(Equal("service_account"))
		Expect(proposal.OnBehalfOf.Valid).To(BeTrue())
		Expect(proposal.OnBehalfOf.String).To(Equal(g12DelegatedPrincipal),
			"a proposal must carry both halves of the delegated action, same as a write (G13)")

		// A human validating in the authoring UI must NOT produce a row, or
		// the audit log fills with keystrokes and stops being readable.
		admin := newAppAdminSession(ctx, st)
		humanResp := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/ValidatePipeline", map[string]any{
			"orgId": orgID.String(), "name": "typing", "contents": "// a human editing",
		}, admin)
		Expect(humanResp.StatusCode).To(Equal(http.StatusOK))

		after, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Limit: 100})
		Expect(err).NotTo(HaveOccurred())
		count := 0
		for i := range after {
			if after[i].Action == "pipeline.propose" {
				count++
			}
		}
		Expect(count).To(Equal(1), "a human validating in the UI produced an audit row; only machine "+
			"callers are proposing, and logging every keystroke makes the trail unreadable")
	})

	It("leaves on_behalf_of empty for a human session's own action (no delegation to record)", func() {
		admin := newAppAdminSession(ctx, st)
		resp := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline", map[string]any{
			"orgId": orgID.String(), "id": pipelineID, "name": "g13-pipeline", "contents": "// human edit",
		}, admin)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Limit: 100})
		Expect(err).NotTo(HaveOccurred())
		var found *sqlc.AuditLog
		for i := range rows {
			if rows[i].Action == "pipeline.update" {
				found = &rows[i]
				break
			}
		}
		Expect(found).NotTo(BeNil())
		Expect(found.ActorType).To(Equal("user"))
		Expect(found.OnBehalfOf.Valid).To(BeFalse())
	})
})

func mustUUID(s string) pgtype.UUID {
	var id pgtype.UUID
	Expect(id.Scan(s)).To(Succeed())
	return id
}
