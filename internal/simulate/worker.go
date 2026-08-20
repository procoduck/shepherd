package simulate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"shepherd/internal/config"
	"shepherd/internal/schema"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
	"shepherd/internal/visual"
)

// RunWorker executes S3 sandbox runs (VB-1 §6.4/§13 M7): it claims queued
// simulate_runs rows, runs the render -> transform -> render -> validate ->
// simulator lifecycle, and writes the terminal result back. It is
// self-contained (its own tickers, its own DB connections) and deliberately
// not folded into agentapi.Sweeper — see the run-API spec's FILES section
// for why.
//
// Cross-replica concurrency (design doc: "queued (1-2 concurrent)") is
// enforced with Postgres session-level advisory locks, one dedicated pooled
// connection per configured slot: a slot's connection holds the lock only
// while it has claimed and is running a row, so MaxConcurrentRuns is a
// cluster-wide cap regardless of how many Shepherd replicas are running —
// a plain in-process semaphore would instead allow replicas*MaxConcurrentRuns
// concurrent sandbox runs.
type RunWorker struct {
	store     *store.Store
	schema    *schema.Registry
	validator *validate.Validator
	client    *Client
	cfg       config.SimulatorConfig
	logger    *slog.Logger
}

// NewRunWorker builds a RunWorker. It does not start anything — call Start.
func NewRunWorker(st *store.Store, schemaReg *schema.Registry, v *validate.Validator, cfg config.SimulatorConfig, logger *slog.Logger) *RunWorker {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &RunWorker{
		store: st, schema: schemaReg, validator: v, cfg: cfg,
		client: NewClient(cfg.ControlURL, cfg.Token, &http.Client{Timeout: cfg.RunTTL + 30*time.Second}),
		logger: logger.With("component", "simulate.worker"),
	}
}

// simulateAdvisoryLockNamespace is the classid half of the two-int
// pg_try_advisory_lock(classid, objid) key this worker uses. Picked
// arbitrarily but fixed, so it never collides with another feature's use of
// the same global advisory-lock keyspace (agentapi.Sweeper does not use
// advisory locks today; if one is added later it must pick a different
// namespace).
const simulateAdvisoryLockNamespace = 0x53494d31 // "SIM1" packed into an int32

// Start launches the worker's slot goroutines (claim loop) and its janitor
// goroutine. It returns immediately; everything runs until ctx is done.
func (w *RunWorker) Start(ctx context.Context) {
	slots := w.cfg.MaxConcurrentRuns
	if slots < 1 {
		slots = 1
	}
	for slot := 1; slot <= slots; slot++ {
		go w.runSlot(ctx, slot)
	}
	go w.runJanitor(ctx)
}

func (w *RunWorker) pollInterval() time.Duration {
	if w.cfg.PollInterval > 0 {
		return w.cfg.PollInterval
	}
	return 2 * time.Second
}

func (w *RunWorker) janitorInterval() time.Duration {
	if w.cfg.JanitorInterval > 0 {
		return w.cfg.JanitorInterval
	}
	return 30 * time.Second
}

func (w *RunWorker) runTTLSeconds() int32 {
	if w.cfg.RunTTL > 0 {
		return int32(w.cfg.RunTTL / time.Second) //nolint:gosec // config-bounded duration, never near int32 overflow
	}
	return 300
}

func (w *RunWorker) retentionSeconds() int32 {
	if w.cfg.RetentionTTL > 0 {
		return int32(w.cfg.RetentionTTL / time.Second) //nolint:gosec // config-bounded duration, never near int32 overflow
	}
	return 3600
}

// runSlot holds one dedicated pooled connection for the lifetime of ctx,
// using it both for the advisory lock and for every sqlc call made while
// this slot owns a run. A dedicated connection is required: the advisory
// lock is session-scoped, so releasing it back to the pool between calls
// would silently drop it.
func (w *RunWorker) runSlot(ctx context.Context, slot int) {
	ticker := time.NewTicker(w.pollInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tryClaimAndRun(ctx, slot)
		}
	}
}

func (w *RunWorker) tryClaimAndRun(ctx context.Context, slot int) {
	conn, err := w.store.Pool().Acquire(ctx)
	if err != nil {
		w.logger.Warn("could not acquire a connection for slot", "slot", slot, "err", err)
		return
	}
	defer conn.Release()

	got, err := tryAdvisoryLock(ctx, conn, slot)
	if err != nil {
		w.logger.Warn("advisory lock attempt failed", "slot", slot, "err", err)
		return
	}
	if !got {
		return // another replica (or another local slot) holds this slot right now
	}
	defer advisoryUnlock(ctx, conn, slot)

	q := sqlc.New(conn)
	run, err := q.ClaimQueuedSimulateRun(ctx)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			w.logger.Warn("claim queued run failed", "slot", slot, "err", err)
		}
		return // nothing queued
	}

	runCtx, cancel := context.WithTimeout(ctx, w.cfg.RunTTL)
	defer cancel()
	w.execute(runCtx, q, run)
}

// tryAdvisoryLock and advisoryUnlock are the one deliberate escape from sqlc:
// pg_try_advisory_lock/pg_advisory_unlock are session-scoped primitives with
// no row shape to generate a query for.
func tryAdvisoryLock(ctx context.Context, conn *pgxpool.Conn, slot int) (bool, error) {
	var got bool
	// RAW-SQL-OK: session-scoped advisory lock primitive, not representable as a sqlc row query
	err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, $2)", int32(simulateAdvisoryLockNamespace), int32(slot)).Scan(&got) //nolint:gosec // slot is a small config-bounded loop index
	return got, err
}

func advisoryUnlock(ctx context.Context, conn *pgxpool.Conn, slot int) {
	// RAW-SQL-OK: session-scoped advisory lock primitive, not representable as a sqlc row query
	if _, err := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1, $2)", int32(simulateAdvisoryLockNamespace), int32(slot)); err != nil { //nolint:gosec // slot is a small config-bounded loop index
		// Not fatal: an unreleased session-level lock is freed automatically
		// when the connection closes (pool churn or process exit) — the
		// worst case is this slot sitting idle until that happens, not a
		// stuck run (the row itself is already terminal by this point).
		slog.Default().Warn("simulate: advisory unlock failed", "slot", slot, "err", err)
	}
}

// execute runs one claimed row's full lifecycle and writes its terminal
// state. It never returns an error: every failure mode ends in a
// CompleteSimulateRun call with status=failed, because a claimed row that
// the worker gives up on silently would sit at status=running until the
// janitor's TTL sweep, unnecessarily holding one of the org's
// MaxNonTerminalPerOrg slots for up to RunTTL.
func (w *RunWorker) execute(ctx context.Context, q *sqlc.Queries, run sqlc.SimulateRun) {
	logger := w.logger.With("run_id", run.ID.String(), "org_id", run.OrgID.String())

	var doc visual.GraphDocument
	if err := json.Unmarshal(run.Graph, &doc); err != nil {
		w.finish(ctx, q, run, runOutcome{errorCode: RunErrorInternal, errorMessage: "stored graph is not valid JSON: " + err.Error()})
		return
	}

	version := doc.SchemaVersion
	if version == "" {
		version = w.schema.CurrentVersion()
	}
	merged, _, err := w.schema.Get(version)
	if err != nil {
		w.finish(ctx, q, run, runOutcome{errorCode: RunErrorInternal, errorMessage: "schema unavailable: " + err.Error()})
		return
	}
	schemaPayload, err := decodeSchemaPayload(merged)
	if err != nil {
		w.finish(ctx, q, run, runOutcome{errorCode: RunErrorInternal, errorMessage: "schema decode failed: " + err.Error()})
		return
	}
	policy, err := LoadPolicy(merged)
	if err != nil {
		w.finish(ctx, q, run, runOutcome{errorCode: RunErrorInternal, errorMessage: "simulation policy decode failed: " + err.Error()})
		return
	}

	// P1: the authored graph must itself render cleanly before anything is
	// rewritten — a label collision in the user's own graph is the user's
	// problem, not a transform bug, but it still fails the run rather than
	// reaching the transform with an already-broken graph.
	if p1 := visual.Render(doc, schemaPayload); len(p1.Diagnostics) > 0 {
		w.finish(ctx, q, run, runOutcome{errorCode: RunErrorGateFailed, errorMessage: "graph failed to render", gateDiagnostics: renderDiagnosticsToGate(p1.Diagnostics)})
		return
	}

	harness := HarnessEndpoints{
		CaptureBaseURL: w.cfg.CaptureBaseURL, OTLPGRPCAddress: w.cfg.OTLPGRPCAddress,
		SyslogHost: w.cfg.SyslogHost, SyslogPort: w.cfg.SyslogPort,
		CaptureDir: w.cfg.CaptureDir, TargetAddress: w.cfg.TargetAddress, LogDir: w.cfg.LogDir,
	}
	result, err := Transform(TransformRequest{Graph: doc, Schema: schemaPayload, Policy: policy, Harness: harness})
	if err != nil {
		var terrs TransformErrors
		msg := err.Error()
		gate := []RunGateDiagnostic(nil)
		if errors.As(err, &terrs) {
			for _, te := range terrs {
				gate = append(gate, RunGateDiagnostic{Layer: "transform", NodeID: te.NodeID, Message: te.Message})
			}
		}
		w.finish(ctx, q, run, runOutcome{errorCode: RunErrorCannotStub, errorMessage: msg, gateDiagnostics: gate})
		return
	}

	// P2: render the transformed graph, then Stage1/2 validate it — the same
	// gate every other path through Shepherd runs authored content through
	// (VisualService.Validate), applied here to the rewritten graph.
	p2 := visual.Render(result.Graph, schemaPayload)
	if len(p2.Diagnostics) > 0 {
		w.finish(ctx, q, run, runOutcome{errorCode: RunErrorGateFailed, errorMessage: "transformed graph failed to render", gateDiagnostics: renderDiagnosticsToGate(p2.Diagnostics)})
		return
	}
	vr := w.validator.Stages12(ctx, p2.Content)
	if !vr.Valid {
		w.finish(ctx, q, run, runOutcome{errorCode: RunErrorGateFailed, errorMessage: "transformed graph failed Alloy validation", gateDiagnostics: validateDiagnosticsToGate(vr.Diagnostics, p2.NodeMap)})
		return
	}

	componentIndex, logFixtures := buildRunInputs(doc, result.Graph, policy, p2.NodeMap)

	clientRun, err := w.client.Start(ctx, ClientStartRequest{
		Config: p2.Content, DurationSeconds: int(run.RequestedDurationSeconds),
		LogFixtures: logFixtures, LogEmitInterval: 500, ComponentIndex: componentIndex,
	})
	if err != nil {
		code, msg := classifySimulatorError(err)
		logger.Warn("simulator start failed", "err", err)
		w.finish(ctx, q, run, runOutcome{errorCode: code, errorMessage: msg, rewrites: result.Rewrites})
		return
	}

	final, err := w.pollUntilTerminal(ctx, clientRun.ID)
	if err != nil {
		code, msg := classifySimulatorError(err)
		logger.Warn("simulator poll failed", "err", err)
		w.finish(ctx, q, run, runOutcome{errorCode: code, errorMessage: msg, rewrites: result.Rewrites})
		return
	}

	nodeInfo := indexOriginalNodes(doc)
	outcome := runOutcome{rewrites: result.Rewrites}
	if final.Results != nil {
		outcome.series = toRunSeries(final.Results.Series)
		outcome.logLines = toRunLogLines(final.Results.LogLines)
		outcome.componentHealth = toRunComponentHealth(final.Results.Components, nodeInfo)
		outcome.stderrTail = joinStderr(final.Results.StderrTail)
	}
	if final.State == "failed" {
		errCode, errMsg := RunErrorGateFailed, "the sandbox run failed"
		if final.Error != nil {
			errMsg = final.Error.Message
			if final.Error.Code != "" {
				errMsg = fmt.Sprintf("%s: %s", final.Error.Code, final.Error.Message)
			}
		}
		outcome.errorCode, outcome.errorMessage = errCode, errMsg
	}
	w.finish(ctx, q, run, outcome)
}

// pollUntilTerminal polls the simulator until the run reaches a terminal
// state or ctx is done. The interval is fixed rather than config-driven: it
// bounds only how quickly a finished run's terminal state is observed, not
// any correctness property, so it does not need its own config key.
func (w *RunWorker) pollUntilTerminal(ctx context.Context, id string) (ClientRun, error) {
	const interval = 750 * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		run, err := w.client.Get(ctx, id)
		if err != nil {
			return ClientRun{}, err
		}
		if ClientTerminal(run.State) {
			return run, nil
		}
		select {
		case <-ctx.Done():
			return ClientRun{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// runOutcome is what execute hands to finish: either a terminal success
// (errorCode empty) or a terminal failure (errorCode set).
type runOutcome struct {
	rewrites        []Rewrite
	series          []RunSeries
	logLines        []RunLogLine
	componentHealth []RunComponentHealth
	gateDiagnostics []RunGateDiagnostic
	stderrTail      string
	errorCode       string
	errorMessage    string
}

const maxCapturedSeries = 500

const maxCapturedLogLines = 500

func (w *RunWorker) finish(ctx context.Context, q *sqlc.Queries, run sqlc.SimulateRun, o runOutcome) {
	status := RunStatusCompleted
	if o.errorCode != "" {
		status = RunStatusFailed
	}

	// Caps per the run-API spec's decision 15: an unbounded capture becomes
	// an unbounded JSONB row and response payload. fidelity_note itself is
	// the fixed §6.5 one-liner (rpc_simulate.go always returns it verbatim,
	// per decision 17) — it is not mutated here.
	if len(o.series) > maxCapturedSeries {
		o.series = o.series[:maxCapturedSeries]
	}
	if len(o.logLines) > maxCapturedLogLines {
		o.logLines = o.logLines[:maxCapturedLogLines]
	}

	updated, err := q.CompleteSimulateRun(ctx, sqlc.CompleteSimulateRunParams{
		ID: run.ID, Status: status,
		Rewrites:         marshalOrEmptyArray(o.rewrites),
		CapturedSeries:   marshalOrEmptyArray(o.series),
		CapturedLogLines: marshalOrEmptyArray(o.logLines),
		ComponentHealth:  marshalOrEmptyArray(o.componentHealth),
		GateDiagnostics:  marshalOrEmptyArray(o.gateDiagnostics),
		StderrTail:       capStderr(o.stderrTail),
		ErrorCode:        o.errorCode,
		ErrorMessage:     o.errorMessage,
	})
	if err != nil {
		w.logger.Error("complete simulate run failed", "run_id", run.ID.String(), "err", err)
		return
	}

	action := "simulate.run.complete"
	if status == RunStatusFailed {
		action = "simulate.run.fail"
	}
	w.auditSystem(ctx, q, updated.OrgID, action, updated.ID.String(), map[string]any{
		"status": status, "error_code": o.errorCode,
	})
}

// runJanitor sweeps for stale (TTL-expired) and old (retention-expired) runs
// on its own ticker, per the run-API spec's decision 7/8.
func (w *RunWorker) runJanitor(ctx context.Context) {
	ticker := time.NewTicker(w.janitorInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.sweepOnce(ctx)
		}
	}
}

func (w *RunWorker) sweepOnce(ctx context.Context) {
	expired, err := w.store.Queries.ExpireStaleSimulateRuns(ctx, w.runTTLSeconds())
	if err != nil {
		w.logger.Warn("expire stale simulate runs failed", "err", err)
	}
	for _, row := range expired {
		w.auditSystem(ctx, w.store.Queries, row.OrgID, "simulate.run.expire", row.ID.String(), map[string]any{"status": RunStatusExpired})
	}
	if err := w.store.Queries.DeleteOldSimulateRuns(ctx, w.retentionSeconds()); err != nil {
		w.logger.Warn("delete old simulate runs failed", "err", err)
	}
}

// auditSystem writes a terminal-state audit row with the background-actor
// shape gitsync/reconciler.go already uses (actor="simulate-worker",
// actor_type="system"), calling InsertAuditLog directly rather than through
// mgmtapi's auditLog helper (which hardcodes actor_type="user" and lives in a
// package that already imports internal/simulate).
func (w *RunWorker) auditSystem(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, action, resourceID string, detail map[string]any) {
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		detailJSON = []byte("{}")
	}
	if err := q.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		Actor: "simulate-worker", ActorType: "system", OrgID: orgID,
		Action: action, ResourceType: "simulate_run", ResourceID: resourceID,
		Detail: detailJSON,
	}); err != nil {
		w.logger.Warn("audit log write failed", "action", action, "err", err)
	}
}
