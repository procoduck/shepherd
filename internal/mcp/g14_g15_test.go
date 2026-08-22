package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/mgmtapi"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/testutil"
)

// This file proves G14 and G15 (docs/gateway-tier-plan.md §6) against a
// REAL shepherd.mgmt.v1 Connect server (internal/mgmtapi.MountRPC — the
// exact production wiring) and a REAL propose-capability service account —
// not a mock. It deliberately lives in `package mcp` (white-box) so it can
// reuse registry_test.go's live-descriptor enumeration and call this
// package's own tool handlers directly, the same way NewServer's
// registered tools would.

var sharedPG *testutil.SharedPostgres

func TestMain(m *testing.M) {
	ctx := context.Background()
	pg, err := testutil.StartSharedPostgres(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "starting shared postgres:", err)
		os.Exit(1)
	}
	sharedPG = pg
	if err := store.MigrateUp(ctx, sharedPG.RootURL); err != nil {
		fmt.Fprintln(os.Stderr, "migrating:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = sharedPG.Terminate(ctx) //nolint:errcheck // best-effort teardown after tests already ran
	os.Exit(code)
}

// newRPCRouter mirrors internal/mgmtapi/rpc_wiring_test.go's
// newRPCWiringRouter exactly (session + CSRF middleware, then MountRPC) —
// restated here because that helper is unexported in a different package.
// This is the same production wiring server.go uses, so a propose-scoped
// service account calling through it is exercising the real thing, not a
// stand-in.
func newRPCRouter(st *store.Store, authHandler *auth.Handler, cfg *config.Config) http.Handler {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(authHandler.SessionMiddleware)
		r.Use(auth.CSRFMiddleware)
		mgmtapi.MountRPC(r, st, cfg, nil, slog.Default())
	})
	return r
}

func mcpTestSetup(t *testing.T) (st *store.Store, server *httptest.Server, orgID pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	dbURL := sharedPG.IsolatedDB(ctx, t)

	var err error
	st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(st.Close)

	o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
		Name: "w11-org", DisplayName: "W11 Org", AdminGroupID: "w11-admin-group",
	})
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	cfg := &config.Config{
		Auth:      config.AuthConfig{InsecureCookies: true},
		Simulator: config.SimulatorConfig{Enabled: true},
	}
	authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
	server = httptest.NewServer(newRPCRouter(st, authHandler, cfg))
	t.Cleanup(server.Close)
	return st, server, o.ID
}

// makeServiceAccount mirrors internal/mgmtapi/capability_test.go's
// g12MakeServiceAccount: mint a credential the way an admin issuing one
// through ServiceAccountService.CreateServiceAccount would, minus the RPC
// round-trip. delegatedHuman becomes service_accounts.created_by — the
// identity requireWriteAuthorized/machine_auth.go's on-behalf-of check
// verifies against.
func makeServiceAccount(t *testing.T, st *store.Store, orgID pgtype.UUID, name, capability, delegatedHuman string) (id, secret string) {
	t.Helper()
	secret = "w11-secret-" + name
	hash := sha256.Sum256([]byte(secret))
	sa, err := st.Queries.CreateServiceAccount(context.Background(), sqlc.CreateServiceAccountParams{
		OrgID: orgID, Name: name, Capability: capability, TokenHash: hash[:], CreatedBy: delegatedHuman,
	})
	if err != nil {
		t.Fatalf("CreateServiceAccount: %v", err)
	}
	return sa.ID.String(), secret
}

// postConnect issues a raw Connect-JSON POST, mirroring
// internal/mgmtapi/capability_test.go's g12PostConnect. Used both for the
// G14 enumeration (every mutating procedure, propose credential) and for
// the G15 human-apply step (session cookie, no service-account headers).
func postConnect(t *testing.T, server *httptest.Server, procedure string, body map[string]any, basicID, basicSecret, onBehalfOf string, cookie *http.Cookie) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, server.URL+procedure, bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if basicID != "" {
		req.SetBasicAuth(basicID, basicSecret)
	}
	if onBehalfOf != "" {
		req.Header.Set(onBehalfOfHeader, onBehalfOf)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func newHumanSession(t *testing.T, st *store.Store, email string) *http.Cookie {
	t.Helper()
	id := fmt.Sprintf("w11-sess-%d", time.Now().UnixNano())
	groupsJSON, err := json.Marshal([]string{})
	if err != nil {
		t.Fatalf("marshal groups: %v", err)
	}
	// IsAppAdmin: true keeps this fixture focused on G15's round trip
	// (which actor performed which half) rather than re-proving G11's
	// team-ownership authorization, already covered by W10's own suite.
	if _, err := st.Queries.CreateSession(context.Background(), sqlc.CreateSessionParams{
		ID: id, UserOid: "user-" + id, Email: email, DisplayName: "W11 Human",
		GroupIds: groupsJSON, IsAppAdmin: true,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		Source:    "test",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return &http.Cookie{Name: "shepherd_session", Value: id}
}

// TestG14_ProposeCredentialRefusedOnEveryLiveMutatingProcedure is G14's
// runtime half (docs/gateway-tier-plan.md: "every mutating procedure
// refused with a propose-scoped token, asserted per procedure"). It does
// not go through this package's tool handlers at all — it calls every
// mutating shepherd.mgmt.v1 procedure DIRECTLY, enumerated from the live
// proto descriptors (allShepherdMgmtV1Procedures/looksLikeAWrite,
// registry_test.go), the same source TestNoToolReachesAMutatingProcedure
// uses. The point is deliberately broader than "the tools this server
// happens to expose today": it proves the CREDENTIAL this server runs as
// cannot apply ANYTHING, so a future tool added without updating
// toolProcedures is still refused at the wire, not just uncaught by a
// static table.
func TestG14_ProposeCredentialRefusedOnEveryLiveMutatingProcedure(t *testing.T) {
	st, server, orgID := mcpTestSetup(t)
	proposeID, proposeSecret := makeServiceAccount(t, st, orgID, "w11-mcp-agent", "propose", "agent-owner@example.com")

	var mutating []string
	for _, proc := range allShepherdMgmtV1Procedures(t) {
		if looksLikeAWrite(proc) {
			mutating = append(mutating, proc)
		}
	}
	if len(mutating) == 0 {
		t.Fatal("zero mutating procedures enumerated — heuristic or descriptor registration broken")
	}

	for _, proc := range mutating {
		t.Run(strings.TrimPrefix(proc, "/shepherd.mgmt.v1."), func(t *testing.T) {
			resp := postConnect(t, server, proc, map[string]any{"orgId": orgID.String()},
				proposeID, proposeSecret, "agent-owner@example.com", nil)
			defer resp.Body.Close() //nolint:errcheck // test cleanup; body content already read below when it matters
			if resp.StatusCode != http.StatusForbidden {
				body := new(bytes.Buffer)
				_, _ = body.ReadFrom(resp.Body) //nolint:errcheck // best-effort diagnostic text for the failure message
				t.Errorf("%s: propose-scoped credential got HTTP %d, want 403 Forbidden (PermissionDenied); body: %s",
					proc, resp.StatusCode, body.String())
			}
		})
	}
}

// TestG14_RedRun documents an EXECUTED red run (docs/gateway-tier-plan.md
// §8 rule 2), not an asserted one. It is a no-op test (there is nothing to
// automate — the revert is in a package outside this one's territory) whose
// body is the transcript of what was actually run during this session:
//
// Revert: internal/mgmtapi/machine_auth.go, requireWriteAuthorized —
//
//	if sa.Capability != capabilityApply {
//
// changed to:
//
//	if false && sa.Capability != capabilityApply { // RED-RUN: G14 capability check disabled
//
// Command: go test ./internal/mcp/... -run TestG14_ProposeCredentialRefusedOnEveryLiveMutatingProcedure -v
//
// Observed (re-measured 2026-08-22 against the merged tree; an earlier
// draft of this comment said "20 of 27", which review caught as stale —
// evidence recorded from memory rather than re-run is exactly the kind of
// claim §8 rule 2 exists to prevent): **23 of the 30** enumerated subtests
// flip from PASS to FAIL, each now returning the handler's ordinary
// validation error instead of a refusal, e.g.:
//
//	--- FAIL: .../PipelineService/CreatePipeline
//	    propose-scoped credential got HTTP 400, want 403 Forbidden (PermissionDenied);
//	    body: {"code":"invalid_argument","message":"name is required"}
//	--- FAIL: .../GitOpsService/CreateCredential
//	    propose-scoped credential got HTTP 503, want 403 Forbidden (PermissionDenied);
//	    body: {"code":"unavailable","message":"encryption not configured"}
//
// The remaining 7 (all AdminService) stay green even with the capability
// check disabled — not a test defect: AdminService requires RoleAppAdmin in
// procedureRequirements, and authorizeServiceAccountProcedure
// (rpc_interceptor.go) refuses RoleAppAdmin to every service account
// unconditionally, a SEPARATE, earlier gate than requireWriteAuthorized.
// That is a genuine, useful finding in its own right: those 7 procedures
// are protected from a propose-scoped (or even apply-scoped) service
// account by the org-reach gate, not by capability scoping — worth knowing
// if that earlier gate is ever refactored.
//
// Revert applied: `diff` against the pre-mutation copy confirmed byte-
// identical after restoring, and the full suite (go test ./internal/...)
// was re-run green afterward — see the workstream report's verification
// section.
func TestG14_RedRun(t *testing.T) {
	t.Skip("this test's body is documentation of an already-executed, already-reverted red run — see the doc comment above")
}

// TestG15_RedRun documents a second EXECUTED red run, for G15's "audit
// shows both actors" half specifically (the round-trip's content half is
// already exercised structurally by every write path's own tests).
//
// Revert: internal/mgmtapi/rpc_simulate.go, SimulateService.CreateRun — the
// auditLogDetail(...) call recording "simulate.run.create" was replaced
// with `_ = actor` (a no-op keeping the variable used).
//
// Command: go test ./internal/mcp/... -run TestG15_ProposalRoundTrips -v
//
// Observed (verbatim):
//
//	--- FAIL: TestG15_ProposalRoundTrips
//	    g14_g15_test.go:377: no audit row found for the propose step
//	    (simulate.run.create by the service account) — G15 requires the
//	    proposal be visible
//
// Revert applied and confirmed byte-identical to the pre-mutation copy via
// diff; go build ./internal/mgmtapi/... and the full TestG15_ProposalRoundTrips
// run were re-confirmed green afterward.
func TestG15_RedRun(t *testing.T) {
	t.Skip("this test's body is documentation of an already-executed, already-reverted red run — see the doc comment above")
}

// TestG15_ProposalRoundTrips is G15 (docs/gateway-tier-plan.md): proposed
// (this server's create_sandbox_run tool, called under the propose
// credential with a fixed on-behalf-of) -> visible as a revision with its
// simulation result (the simulate_runs row, and — once a human applies it —
// the ordinary pipeline_revisions row PipelineService.UpdatePipeline
// already writes) -> a human applies it (an ordinary UpdatePipeline call
// through a real session cookie, not this server's credential) -> audit
// shows both actors.
func TestG15_ProposalRoundTrips(t *testing.T) {
	ctx := context.Background()
	st, server, orgID := mcpTestSetup(t)

	const delegatedHuman = "agent-owner@example.com"
	proposeID, proposeSecret := makeServiceAccount(t, st, orgID, "w11-mcp-agent", "propose", delegatedHuman)

	// Seed an existing pipeline a human will update — mirrors the common
	// case an agent proposes an edit to, rather than a brand-new pipeline.
	seeded, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
		OrgID: orgID, Name: "g15-pipeline", Contents: "// v1", Source: "ui",
		Matchers: json.RawMessage("[]"), CreatedBy: delegatedHuman, UpdatedBy: delegatedHuman,
	})
	if err != nil {
		t.Fatalf("seed CreatePipeline: %v", err)
	}

	// -- Step 1: PROPOSE, via this package's own tool handler, exactly as
	// NewServer's registered create_sandbox_run tool would be invoked by an
	// MCP client. This is the one propose-safe procedure that leaves an
	// audited record (SimulateService.CreateRun — see propose.go's doc
	// comment for why the composite propose_pipeline_revision tool itself
	// is not separately audited today, a limitation stated there and in
	// the workstream report rather than hidden).
	backend := NewBackend(Config{
		BaseURL: server.URL, ServiceAccountID: proposeID, ServiceAccountSecret: proposeSecret,
		OnBehalfOf: delegatedHuman, HTTPClient: server.Client(),
	})
	graph := json.RawMessage(`{"kind":"alloy-graph/v1","schema_version":"v1","nodes":[{"id":"n1","component":"prometheus.exporter.self","label":"self"}]}`)
	_, runOut, err := backend.createSandboxRun(ctx, nil, createSandboxRunIn{OrgID: orgID.String(), Graph: graph})
	if err != nil {
		t.Fatalf("create_sandbox_run (propose step): %v", err)
	}
	if runOut.RunID == "" {
		t.Fatal("create_sandbox_run returned an empty run id")
	}

	// The propose step must be REFUSED to apply anything: same credential,
	// direct attempt at the actual write this proposal is about.
	applyAttempt := postConnect(t, server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline",
		map[string]any{"orgId": orgID.String(), "id": seeded.ID.String(), "name": "g15-pipeline", "contents": "// agent-applied, should be refused"},
		proposeID, proposeSecret, delegatedHuman, nil)
	defer applyAttempt.Body.Close() //nolint:errcheck // test cleanup
	if applyAttempt.StatusCode != http.StatusForbidden {
		t.Fatalf("propose credential's own direct apply attempt got HTTP %d, want 403 — the credential must never be able to apply its own proposal", applyAttempt.StatusCode)
	}

	// -- Step 2: a HUMAN applies it — an ordinary write through a real
	// session cookie, this server's credential nowhere involved.
	humanCookie := newHumanSession(t, st, delegatedHuman)
	const appliedContents = "// v2, applied by a human from the agent's proposal"
	applyResp := postConnect(t, server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline",
		map[string]any{"orgId": orgID.String(), "id": seeded.ID.String(), "name": "g15-pipeline", "contents": appliedContents},
		"", "", "", humanCookie)
	defer applyResp.Body.Close() //nolint:errcheck // test cleanup
	if applyResp.StatusCode != http.StatusOK {
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(applyResp.Body) //nolint:errcheck // best-effort diagnostic text for the failure message
		t.Fatalf("human apply (UpdatePipeline) got HTTP %d, want 200; body: %s", applyResp.StatusCode, body.String())
	}

	// -- Step 3: the round trip's content half — the pipeline now carries
	// exactly what was applied, readable back through this package's own
	// get_pipeline tool (org-reader is enough; still uses the propose
	// credential, proving reads stay open per G12's "capability only gates
	// writes").
	_, got, err := backend.getPipeline(ctx, nil, orgIDArg{OrgID: orgID.String(), ID: seeded.ID.String()})
	if err != nil {
		t.Fatalf("get_pipeline: %v", err)
	}
	if got.Contents != appliedContents {
		t.Fatalf("pipeline contents = %q, want %q (the human's apply)", got.Contents, appliedContents)
	}
	// Only 1, not 2: the seed pipeline above was inserted directly via
	// st.Queries.CreatePipeline (bypassing the RPC layer, which is what
	// actually writes a pipeline_revisions row on save — see
	// rpc_pipeline.go's createRevision) precisely so this fixture starts
	// from a known baseline without the seed step itself becoming a third,
	// confusing audit-log actor.
	if len(got.Revisions) != 1 {
		t.Fatalf("expected exactly 1 revision (the human's apply — the seed pipeline was inserted directly, bypassing the RPC layer's revision write), got %d", len(got.Revisions))
	}

	// -- Step 4: the audit half — BOTH actors present. The propose step's
	// audit row is a machine actor with the on-behalf-of claim attached;
	// the apply step's is the human, unattributed-machine-wise, because it
	// is not a machine write at all.
	rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Column1: orgID, Limit: 50})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}

	var sawProposeActor, sawApplyActor bool
	for _, r := range rows {
		switch {
		case r.Action == "simulate.run.create" && r.ActorType == "service_account":
			sawProposeActor = true
			if !strings.Contains(r.Actor, "w11-mcp-agent") {
				t.Errorf("propose audit row actor = %q, want it to name the mcp agent service account", r.Actor)
			}
			if !r.OnBehalfOf.Valid || r.OnBehalfOf.String != delegatedHuman {
				t.Errorf("propose audit row on_behalf_of = %+v, want %q", r.OnBehalfOf, delegatedHuman)
			}
		case r.Action == "pipeline.update" && r.ActorType == "user":
			sawApplyActor = true
			if r.Actor != delegatedHuman {
				t.Errorf("apply audit row actor = %q, want the human %q", r.Actor, delegatedHuman)
			}
			if r.OnBehalfOf.Valid {
				t.Errorf("apply audit row on_behalf_of = %+v, want NULL — this was the human's own write, not a delegated one", r.OnBehalfOf)
			}
		}
	}
	if !sawProposeActor {
		t.Error("no audit row found for the propose step (simulate.run.create by the service account) — G15 requires the proposal be visible")
	}
	if !sawApplyActor {
		t.Error("no audit row found for the apply step (pipeline.update by the human) — G15 requires audit show the human actor")
	}
}
