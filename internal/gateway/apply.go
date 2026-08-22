package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// This file is the R1 obligation the plan names: RenderHTTPRoute (route.go)
// produces an object; until this file, nothing applied it to a cluster or
// checked it took effect. An HTTPRoute can be created successfully and still
// route nothing — the target Gateway may not exist, may have no listener the
// route can attach to, or its listeners' allowedRoutes may not admit routes
// from our namespace (D8's operator-owned mode). That is silent non-routing,
// exactly the failure D3 exists to prevent. So ApplyRoute below verifies
// ATTACHMENT (status.parents[].conditions), never just creation, and a
// deadline expiry is reported as a FAILURE — never an assumed success.
//
// Both D8 ownership modes are served by the SAME code path, with no branch:
// a Gateway Shepherd manages and one an operator runs out of band are both
// just a name+namespace ApplyRoute polls status for. The only thing that
// differs between the modes is who created the Gateway, which is invisible
// from here.

// ErrNotFound is the sentinel an Applier.Get implementation must return
// (wrapped, so errors.Is(err, ErrNotFound) succeeds) when the named
// HTTPRoute does not exist yet. ApplyRoute uses this to decide whether
// Apply is creating a new object or converging an existing one; every other
// Get failure is treated as fatal.
var ErrNotFound = errors.New("gateway: object not found")

// Applier is the minimal Kubernetes surface ApplyRoute needs. Deliberately
// not tied to a specific client library — client-go, controller-runtime and
// the e2e-framework's own resources.Resources all shape these operations
// differently, and this package has no need to pick one. A caller adapts
// whichever client it already holds to this interface. That keeps this
// package's dependency footprint at exactly what route.go already needed
// (gatewayv1's typed structs) and makes the load-bearing logic
// (CheckAttachment) testable against a trivial in-memory fake rather than a
// real or fake cluster.
type Applier interface {
	// Apply creates route if absent, or updates it in place to converge its
	// spec if it already exists. An operator-owned Gateway is out of
	// Shepherd's control, but the HTTPRoute itself is Shepherd's to keep
	// converged across repeated calls (e.g. re-applying after a partial
	// failure, or a segment rotation that reuses the same object name).
	// How "converge" is implemented — server-side apply, get-then-update,
	// etc. — is the adapter's concern, not this package's.
	Apply(ctx context.Context, route *gatewayv1.HTTPRoute) error

	// Get reads back namespace/name's current state, including
	// status.parents — the only field ApplyRoute's polling loop reads.
	// Implementations MUST return an error matching
	// errors.Is(err, ErrNotFound) when the object does not exist.
	Get(ctx context.Context, namespace, name string) (*gatewayv1.HTTPRoute, error)

	// HTTPRouteCRDAnnotations reads the bundle-version/channel annotations
	// off the cluster's installed HTTPRoute CustomResourceDefinition — the
	// same discovery mechanism version.go's CheckSupport documents,
	// resolved once per ApplyRoute call so a cluster is never applied to on
	// the strength of a version it does not actually run (D3).
	HTTPRouteCRDAnnotations(ctx context.Context) (map[string]string, error)
}

// ErrPending means status has not yet been written for the parentRef
// ApplyRoute cares about — the controller writes status.parents
// asynchronously after accepting a route (point 2 of the plan's R1 note), so
// this is the expected state immediately after Create and is not itself a
// failure. ApplyRoute polls through it; only deadline expiry while still
// ErrPending is reported as a failure.
type ErrPending struct{ Reason string }

func (e *ErrPending) Error() string { return e.Reason }

// ErrRefused means the controller has definitively answered, and the answer
// is "this route does not attach" — Accepted=False, or Accepted=True but
// ResolvedRefs=False. ApplyRoute stops polling immediately on this: waiting
// longer will not change a definitive answer, and folding it into a generic
// timeout message would hide exactly the actionable detail (which condition,
// which reason, which message) this type exists to carry to the caller.
type ErrRefused struct{ Reason string }

func (e *ErrRefused) Error() string { return e.Reason }

// CheckAttachment reads route's current status.parents and reports whether
// the parent identified by (gatewayName, gatewayNamespace) shows the route
// as actually attached — not merely created or parsed (D3's distinction).
//
// gatewayNamespace may be "" to mean "same namespace as route", mirroring
// RouteSpec.GatewayNamespace's own convention in route.go — resolved against
// route.Namespace, the one namespace ApplyRoute's caller and the controller
// both agree on without an explicit value.
//
// Three outcomes:
//   - nil: a parent status entry exists for this parentRef, Accepted=True,
//     and ResolvedRefs is either True or absent (some controllers do not
//     emit ResolvedRefs at all; absence is not evidence of a broken
//     backendRef, only that this controller does not report it).
//   - *ErrPending: no parent status entry exists yet for this parentRef, or
//     one exists but has not yet reported an Accepted condition. Retry.
//   - *ErrRefused: a parent status entry exists and definitively says no —
//     Accepted=False, or Accepted=True with ResolvedRefs=False. Do not
//     retry; report to the caller as a refusal.
func CheckAttachment(route *gatewayv1.HTTPRoute, gatewayName, gatewayNamespace string) error {
	wantNS := gatewayNamespace
	if wantNS == "" {
		wantNS = route.Namespace
	}

	var parent *gatewayv1.RouteParentStatus
	for i := range route.Status.Parents {
		p := &route.Status.Parents[i]
		if string(p.ParentRef.Name) != gatewayName {
			continue
		}
		ns := route.Namespace
		if p.ParentRef.Namespace != nil {
			ns = string(*p.ParentRef.Namespace)
		}
		if ns != wantNS {
			continue
		}
		parent = p
		break
	}
	if parent == nil {
		return &ErrPending{Reason: fmt.Sprintf(
			"HTTPRoute %s/%s has no status.parents entry yet for Gateway %s/%s — the controller has not "+
				"reported status for this parentRef yet",
			route.Namespace, route.Name, wantNS, gatewayName)}
	}

	accepted := findCondition(parent.Conditions, string(gatewayv1.RouteConditionAccepted))
	if accepted == nil {
		return &ErrPending{Reason: fmt.Sprintf(
			"HTTPRoute %s/%s has a status.parents entry for Gateway %s/%s but no %s condition on it yet",
			route.Namespace, route.Name, wantNS, gatewayName, gatewayv1.RouteConditionAccepted)}
	}
	if accepted.Status != metav1.ConditionTrue {
		return &ErrRefused{Reason: fmt.Sprintf(
			"HTTPRoute %s/%s was refused attachment by Gateway %s/%s: %s=%s (reason %s: %s). The route "+
				"was created but the Gateway is not routing it — check the Gateway's listeners and their "+
				"allowedRoutes.",
			route.Namespace, route.Name, wantNS, gatewayName, gatewayv1.RouteConditionAccepted, accepted.Status,
			accepted.Reason, accepted.Message)}
	}

	resolved := findCondition(parent.Conditions, string(gatewayv1.RouteConditionResolvedRefs))
	if resolved != nil && resolved.Status != metav1.ConditionTrue {
		return &ErrRefused{Reason: fmt.Sprintf(
			"HTTPRoute %s/%s is Accepted by Gateway %s/%s but its backend does not resolve: %s=%s "+
				"(reason %s: %s). The route attaches but sends traffic nowhere.",
			route.Namespace, route.Name, wantNS, gatewayName, gatewayv1.RouteConditionResolvedRefs,
			resolved.Status, resolved.Reason, resolved.Message)}
	}
	return nil
}

func findCondition(conds []metav1.Condition, condType string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == condType {
			return &conds[i]
		}
	}
	return nil
}

// ApplyOptions bounds ApplyRoute's polling loop.
type ApplyOptions struct {
	// PollInterval between attachment checks. Defaults to 2s if zero.
	PollInterval time.Duration
	// Deadline for the whole apply-and-verify operation, measured from when
	// Apply succeeds. Defaults to 60s if zero. Expiry is a FAILURE — see
	// ApplyRoute's doc comment.
	Deadline time.Duration
}

const (
	defaultPollInterval = 2 * time.Second
	defaultDeadline     = 60 * time.Second
)

// ApplyRoute is the R1 obligation in one call: refuse if the cluster's
// Gateway API is not new enough or on the wrong channel (D3, checked against
// the cluster's ACTUAL installed CRD annotations, not an assumption), render
// spec (route.go), apply the HTTPRoute, then poll status.parents until the
// route is verified ATTACHED — never merely created.
//
// A bounded deadline, not indefinite polling, because status is written
// asynchronously by the controller — but deadline expiry returns an error,
// never a nil/success: this function has exactly one way to report success
// (CheckAttachment returning nil for the freshly-read object), and "we
// waited a while and nothing said no" is not it.
//
// An *ErrRefused from CheckAttachment short-circuits the poll loop
// immediately rather than waiting out the deadline: a definitive refusal is
// not going to become an acceptance by waiting longer, and reporting it
// promptly with its actual reason is strictly more useful than a generic
// timeout.
func ApplyRoute(ctx context.Context, cl Applier, spec RouteSpec, opts ApplyOptions) (*gatewayv1.HTTPRoute, error) {
	if opts.PollInterval <= 0 {
		opts.PollInterval = defaultPollInterval
	}
	if opts.Deadline <= 0 {
		opts.Deadline = defaultDeadline
	}

	ann, err := cl.HTTPRouteCRDAnnotations(ctx)
	if err != nil {
		return nil, fmt.Errorf("gateway: reading the cluster's installed HTTPRoute CRD annotations: %w", err)
	}
	if err := CheckSupport(ann[BundleVersionAnnotation], ann[ChannelAnnotation]); err != nil {
		return nil, err
	}

	route, err := RenderHTTPRoute(spec)
	if err != nil {
		return nil, err
	}
	if err := cl.Apply(ctx, route); err != nil {
		return nil, fmt.Errorf("gateway: applying HTTPRoute %s/%s: %w", spec.Namespace, spec.Name, err)
	}

	deadline := time.Now().Add(opts.Deadline)
	var lastErr error
	for {
		got, err := cl.Get(ctx, spec.Namespace, spec.Name)
		switch {
		case err != nil:
			lastErr = fmt.Errorf("reading back HTTPRoute %s/%s: %w", spec.Namespace, spec.Name, err)
		default:
			attachErr := CheckAttachment(got, spec.GatewayName, spec.GatewayNamespace)
			if attachErr == nil {
				return got, nil
			}
			var refused *ErrRefused
			if errors.As(attachErr, &refused) {
				return nil, fmt.Errorf("gateway: HTTPRoute %s/%s was applied but refused attachment: %w",
					spec.Namespace, spec.Name, attachErr)
			}
			lastErr = attachErr
		}

		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf(
				"gateway: HTTPRoute %s/%s did not verify as attached within %s — treating this as a "+
					"FAILURE, not an assumed success. Last observed: %w",
				spec.Namespace, spec.Name, opts.Deadline, lastErr)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("gateway: waiting for HTTPRoute %s/%s to attach: %w", spec.Namespace, spec.Name, ctx.Err())
		case <-time.After(opts.PollInterval):
		}
	}
}
