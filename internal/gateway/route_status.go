package gateway

import (
	"errors"
	"fmt"
)

// RouteStatus is a tenant route's lifecycle, as tenant_routes.status stores
// it (0009_tenant_routes).
type RouteStatus string

const (
	// RouteActive routes traffic.
	RouteActive RouteStatus = "active"
	// RouteDeprecated still routes, mid-rotation, until its valid_until.
	RouteDeprecated RouteStatus = "deprecated"
	// RouteRevoked routes nothing.
	RouteRevoked RouteStatus = "revoked"
)

// ApplyStatus is whether a tenant route's HTTPRoute is in the cluster, as the
// tenant-route reconciler (internal/routeapply) last found it —
// tenant_routes.apply_status (0027).
type ApplyStatus string

const (
	// ApplyPending is not yet reconciled.
	ApplyPending ApplyStatus = "pending"
	// ApplyApplied has its attachment verified.
	ApplyApplied ApplyStatus = "applied"
	// ApplyRefused was refused attachment by the gateway.
	ApplyRefused ApplyStatus = "refused"
	// ApplyError failed and is retried with backoff.
	ApplyError ApplyStatus = "error"
	// ApplyRemoved was revoked or expired; its HTTPRoute was deleted.
	ApplyRemoved ApplyStatus = "removed"
	// ApplyNotApplicable is a Faro route, or route apply is disabled.
	ApplyNotApplicable ApplyStatus = "not_applicable"
)

// ErrUnknownStatus is returned by ParseRouteStatus and ParseApplyStatus.
var ErrUnknownStatus = errors.New("gateway: unknown tenant route status")

// ParseRouteStatus is the one conversion from a stored status to a
// RouteStatus. It fails closed: anything else is an error, never a default.
func ParseRouteStatus(s string) (RouteStatus, error) {
	switch st := RouteStatus(s); st {
	case RouteActive, RouteDeprecated, RouteRevoked:
		return st, nil
	}
	return "", fmt.Errorf("%w %q", ErrUnknownStatus, s)
}

// ParseApplyStatus is the one conversion from a stored apply_status to an
// ApplyStatus. It fails closed like ParseRouteStatus.
func ParseApplyStatus(s string) (ApplyStatus, error) {
	switch st := ApplyStatus(s); st {
	case ApplyPending, ApplyApplied, ApplyRefused, ApplyError, ApplyRemoved, ApplyNotApplicable:
		return st, nil
	}
	return "", fmt.Errorf("%w %q", ErrUnknownStatus, s)
}
