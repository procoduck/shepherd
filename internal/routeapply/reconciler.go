// Package routeapply puts tenant routes into the cluster: a background loop
// that applies each wanted route's HTTPRoute through gateway.ApplyRoute
// (verified attachment, never mere creation), deletes the ones no route wants,
// and records the outcome on the route's row
// (docs/archive/plans/2026-09-29-tenant-route-apply.md). The RPCs only ever change the
// database; nothing here blocks a request.
//
// Desired state per row:
//
//	active                                 present, verified attached
//	deprecated, valid_until in the future  present (the rotation overlap)
//	deprecated, valid_until passed         revoked by this loop, then absent
//	revoked                                absent
//	kind faro                              absent — no Faro receiver exists (D10)
//
// The Gateway itself is never created or modified, whichever D8 mode the
// route names.
package routeapply

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"shepherd/internal/config"
	"shepherd/internal/gateway"
	"shepherd/internal/store/sqlc"
)

// RouteIDLabel carries the tenant route's id on the HTTPRoute it owns, and is
// how orphans are found.
const RouteIDLabel = "shepherd.io/tenant-route-id"

// Apply statuses, as migration 0027 lists them.
const (
	StatusPending       = "pending"
	StatusApplied       = "applied"
	StatusRefused       = "refused"
	StatusError         = "error"
	StatusRemoved       = "removed"
	StatusNotApplicable = "not_applicable"
)

const (
	// maxMessageLen caps apply_message: an apiserver error can be long.
	maxMessageLen = 1000
	// maxBackoff caps the wait before retrying a failing route.
	maxBackoff  = 30 * time.Minute
	faroMessage = "not applied: Shepherd deploys no Faro receiver (D10)"
)

// Cluster is what the reconciler needs from Kubernetes: gateway.ApplyRoute's
// surface plus delete and a listing of the objects it owns.
type Cluster interface {
	gateway.Applier
	Delete(ctx context.Context, namespace, name string) error
	ListManaged(ctx context.Context, namespace string) ([]string, error)
}

// Store is the slice of sqlc.Queries the reconciler uses.
type Store interface {
	RevokeExpiredTenantRoutes(ctx context.Context) ([]sqlc.TenantRoute, error)
	ListTenantRoutesForReconcile(ctx context.Context) ([]sqlc.TenantRoute, error)
	SetTenantRouteApplyStatus(ctx context.Context, arg sqlc.SetTenantRouteApplyStatusParams) (sqlc.TenantRoute, error)
}

type backoff struct {
	failures int
	next     time.Time
}

// Reconciler converges the cluster's HTTPRoutes on the tenant_routes table.
type Reconciler struct {
	store   Store
	cluster Cluster
	cfg     config.RouteApplyConfig
	logger  *slog.Logger
	// locker, when set, limits passes to one replica at a time (see Locker).
	locker Locker
	now    func() time.Time
	// retry holds per-route backoff after a refusal or error; a route with no
	// entry is attempted every pass. Memory only: a restart retries at once.
	retry map[string]*backoff
}

// Option configures a Reconciler.
type Option func(*Reconciler)

// WithLocker makes each pass run only while holding l, so replicas take turns
// instead of racing on the same HTTPRoutes.
func WithLocker(l Locker) Option {
	return func(r *Reconciler) { r.locker = l }
}

// New creates a Reconciler.
func New(st Store, cl Cluster, cfg config.RouteApplyConfig, logger *slog.Logger, opts ...Option) *Reconciler {
	r := &Reconciler{
		store:   st,
		cluster: cl,
		cfg:     cfg,
		logger:  logger.With("component", "routeapply"),
		now:     time.Now,
		retry:   map[string]*backoff{},
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Start runs a pass immediately and then every cfg.Interval until ctx ends.
func (r *Reconciler) Start(ctx context.Context) {
	go func() {
		r.Reconcile(ctx)
		t := time.NewTicker(r.cfg.Interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				r.Reconcile(ctx)
			}
		}
	}()
}

// ObjectName is the HTTPRoute name for a route id — stable across restarts.
func ObjectName(id string) string {
	return "shepherd-tenant-route-" + id
}

// Reconcile runs one pass. Failures are logged and recorded per route; a pass
// never stops at the first one.
func (r *Reconciler) Reconcile(ctx context.Context) {
	if r.locker != nil {
		unlock, ok, err := r.locker.TryLock(ctx)
		if err != nil {
			r.logger.Error("route-apply lock", "err", err)
			return
		}
		if !ok {
			// Another replica is reconciling; it records the outcomes.
			return
		}
		defer unlock()
	}
	if revoked, err := r.store.RevokeExpiredTenantRoutes(ctx); err != nil {
		r.logger.Error("revoking expired deprecated routes", "err", err)
	} else {
		for i := range revoked {
			r.logger.Info("rotation overlap ended; route revoked", "route_id", revoked[i].ID.String())
		}
	}

	rows, err := r.store.ListTenantRoutesForReconcile(ctx)
	if err != nil {
		r.logger.Error("listing tenant routes", "err", err)
		return
	}

	wanted := map[string]bool{}
	for i := range rows {
		if wantsRoute(rows[i]) {
			wanted[ObjectName(rows[i].ID.String())] = true
		}
	}

	// Deletions first, so a route that has just been rotated away stops
	// routing even if an apply below is slow. A route whose HTTPRoute could
	// not be listed or deleted is not marked removed.
	failedDelete := map[string]bool{}
	names, listErr := r.cluster.ListManaged(ctx, r.cfg.Namespace)
	if listErr != nil {
		r.logger.Error("listing managed HTTPRoutes", "err", listErr)
	}
	for _, name := range names {
		if wanted[name] {
			continue
		}
		if err := r.cluster.Delete(ctx, r.cfg.Namespace, name); err != nil {
			r.logger.Error("deleting unwanted HTTPRoute", "name", name, "err", err)
			failedDelete[name] = true
			continue
		}
		r.logger.Info("deleted unwanted HTTPRoute", "name", name)
	}

	for i := range rows {
		row := rows[i]
		name := ObjectName(row.ID.String())
		switch {
		case row.Kind == string(gateway.KindFaro):
			r.record(ctx, row, StatusNotApplicable, faroMessage)
		case !wantsRoute(row):
			if listErr == nil && !failedDelete[name] {
				r.record(ctx, row, StatusRemoved, "")
			}
		default:
			r.apply(ctx, row)
		}
	}
}

// wantsRoute reports whether row's HTTPRoute should be in the cluster.
// Expired deprecated rows were revoked at the top of the pass, but a row that
// expired since then is still treated as unwanted.
func wantsRoute(row sqlc.TenantRoute) bool {
	if row.Kind == string(gateway.KindFaro) {
		return false
	}
	switch row.Status {
	case "active":
		return true
	case "deprecated":
		return !row.ValidUntil.Valid || row.ValidUntil.Time.After(time.Now())
	default:
		return false
	}
}

func (r *Reconciler) apply(ctx context.Context, row sqlc.TenantRoute) {
	id := row.ID.String()
	if b := r.retry[id]; b != nil && r.now().Before(b.next) {
		return
	}

	_, err := gateway.ApplyRoute(ctx, r.cluster, gateway.RouteSpec{
		Name:             ObjectName(id),
		Namespace:        r.cfg.Namespace,
		TenantID:         row.TenantID,
		Kind:             gateway.RouteKind(row.Kind),
		RouteSegment:     row.Segment,
		GatewayName:      row.GatewayName,
		GatewayNamespace: row.GatewayNamespace,
		BackendName:      r.cfg.BackendService,
		BackendPort:      r.cfg.BackendPort,
		Labels:           map[string]string{RouteIDLabel: id},
	}, gateway.ApplyOptions{Deadline: r.cfg.AttachTimeout})
	if ctx.Err() != nil {
		// Shutdown mid-apply is not the route's failure.
		return
	}

	var refused *gateway.ErrRefused
	switch {
	case err == nil:
		delete(r.retry, id)
		r.record(ctx, row, StatusApplied, "")
	case errors.As(err, &refused):
		r.backOff(id)
		r.record(ctx, row, StatusRefused, err.Error())
	default:
		r.backOff(id)
		r.record(ctx, row, StatusError, err.Error())
	}
}

// backOff doubles the wait before id's next attempt, from one pass interval up
// to maxBackoff, with ±20% jitter so failing routes do not retry in lockstep.
func (r *Reconciler) backOff(id string) {
	b := r.retry[id]
	if b == nil {
		b = &backoff{}
		r.retry[id] = b
	}
	b.failures++
	wait := r.cfg.Interval << min(b.failures-1, 16)
	if wait <= 0 || wait > maxBackoff {
		wait = maxBackoff
	}
	jitter := time.Duration((rand.Float64()*0.4 - 0.2) * float64(wait)) //nolint:gosec // jitter, not security
	b.next = r.now().Add(wait + jitter)
}

// record writes the outcome when it changed. An applied route is rewritten
// every pass so applied_at stays the last verified attachment.
func (r *Reconciler) record(ctx context.Context, row sqlc.TenantRoute, status, message string) {
	if len(message) > maxMessageLen {
		message = message[:maxMessageLen]
	}
	if status != StatusApplied && row.ApplyStatus == status && row.ApplyMessage == message {
		return
	}
	if _, err := r.store.SetTenantRouteApplyStatus(ctx, sqlc.SetTenantRouteApplyStatusParams{
		ID: row.ID, ApplyStatus: status, ApplyMessage: message,
	}); err != nil {
		r.logger.Error("recording apply status", "route_id", row.ID.String(), "status", status, "err", err)
		return
	}
	if status != StatusApplied && row.ApplyStatus != status {
		r.logger.Info("route apply status changed", "route_id", row.ID.String(),
			"from", row.ApplyStatus, "to", status, "message", message)
	}
}
