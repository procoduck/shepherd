package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// --- CheckAttachment: the load-bearing logic, tested against fabricated
// status objects rather than a cluster. Covers exactly the five scenarios
// the R1 obligation names.

func TestCheckAttachment(t *testing.T) {
	const (
		gwName    = "shepherd-gw"
		gwNS      = "shepherd-receiver"
		routeName = "route-acme"
		routeNS   = "shepherd-receiver"
	)
	ourParentRef := gatewayv1.ParentReference{Name: gwName, Namespace: ptr.To(gatewayv1.Namespace(gwNS))}

	tests := []struct {
		name       string
		parents    []gatewayv1.RouteParentStatus
		wantKind   string // "attached", "pending", "refused"
		wantSubstr string
	}{
		{
			name: "Accepted=True and ResolvedRefs=True -> attached",
			parents: []gatewayv1.RouteParentStatus{{
				ParentRef: ourParentRef,
				Conditions: []metav1.Condition{
					{Type: string(gatewayv1.RouteConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
					{Type: string(gatewayv1.RouteConditionResolvedRefs), Status: metav1.ConditionTrue, Reason: "ResolvedRefs"},
				},
			}},
			wantKind: "attached",
		},
		{
			name: "Accepted=True with ResolvedRefs absent -> attached (not every controller emits it)",
			parents: []gatewayv1.RouteParentStatus{{
				ParentRef: ourParentRef,
				Conditions: []metav1.Condition{
					{Type: string(gatewayv1.RouteConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
				},
			}},
			wantKind: "attached",
		},
		{
			name: "Accepted=False with a reason -> refused",
			parents: []gatewayv1.RouteParentStatus{{
				ParentRef: ourParentRef,
				Conditions: []metav1.Condition{
					{
						Type: string(gatewayv1.RouteConditionAccepted), Status: metav1.ConditionFalse,
						Reason: "NotAllowedByListeners", Message: "listener does not admit routes from this namespace",
					},
				},
			}},
			wantKind:   "refused",
			wantSubstr: "NotAllowedByListeners",
		},
		{
			name: "Accepted=True but ResolvedRefs=False -> refused (accepted but backend does not resolve)",
			parents: []gatewayv1.RouteParentStatus{{
				ParentRef: ourParentRef,
				Conditions: []metav1.Condition{
					{Type: string(gatewayv1.RouteConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
					{
						Type: string(gatewayv1.RouteConditionResolvedRefs), Status: metav1.ConditionFalse,
						Reason: "BackendNotFound", Message: "service \"echo\" not found",
					},
				},
			}},
			wantKind:   "refused",
			wantSubstr: "BackendNotFound",
		},
		{
			name:     "no parents at all -> pending",
			parents:  nil,
			wantKind: "pending",
		},
		{
			name: "parents present but for a different parentRef -> pending",
			parents: []gatewayv1.RouteParentStatus{{
				ParentRef: gatewayv1.ParentReference{Name: "some-other-gw", Namespace: ptr.To(gatewayv1.Namespace(gwNS))},
				Conditions: []metav1.Condition{
					{Type: string(gatewayv1.RouteConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
				},
			}},
			wantKind: "pending",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: routeName, Namespace: routeNS},
				Status:     gatewayv1.HTTPRouteStatus{RouteStatus: gatewayv1.RouteStatus{Parents: tc.parents}},
			}
			err := CheckAttachment(route, gwName, gwNS)

			switch tc.wantKind {
			case "attached":
				if err != nil {
					t.Fatalf("want attached (nil error), got %v", err)
				}
			case "pending":
				if err == nil {
					t.Fatalf("want *ErrPending, got nil")
				}
				var pending *ErrPending
				if !errors.As(err, &pending) {
					t.Fatalf("want *ErrPending, got %T: %v", err, err)
				}
			case "refused":
				if err == nil {
					t.Fatalf("want *ErrRefused, got nil")
				}
				var refused *ErrRefused
				if !errors.As(err, &refused) {
					t.Fatalf("want *ErrRefused, got %T: %v", err, err)
				}
				if tc.wantSubstr != "" && !strings.Contains(err.Error(), tc.wantSubstr) {
					t.Fatalf("error %q missing expected substring %q", err.Error(), tc.wantSubstr)
				}
			default:
				t.Fatalf("bad test case: unknown wantKind %q", tc.wantKind)
			}
		})
	}
}

// TestCheckAttachment_GatewayNamespaceDefaultsToRouteNamespace pins that an
// empty gatewayNamespace argument (RouteSpec.GatewayNamespace's own "same
// namespace as the route" convention) is resolved correctly on BOTH sides of
// the match: the argument, and a parentRef whose own Namespace is nil.
func TestCheckAttachment_GatewayNamespaceDefaultsToRouteNamespace(t *testing.T) {
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route-acme", Namespace: "shepherd-receiver"},
		Status: gatewayv1.HTTPRouteStatus{RouteStatus: gatewayv1.RouteStatus{
			Parents: []gatewayv1.RouteParentStatus{{
				// Namespace nil: the controller echoes back exactly what the
				// spec carried, and RenderHTTPRoute leaves it nil for
				// same-namespace gateways (route.go's optionalNamespace).
				ParentRef: gatewayv1.ParentReference{Name: "shepherd-gw"},
				Conditions: []metav1.Condition{
					{Type: string(gatewayv1.RouteConditionAccepted), Status: metav1.ConditionTrue},
				},
			}},
		}},
	}
	if err := CheckAttachment(route, "shepherd-gw", ""); err != nil {
		t.Fatalf("expected attached with empty gatewayNamespace resolving to route's own namespace, got %v", err)
	}
}

// --- ApplyRoute: the poll loop wired to a fake, in-memory Applier.

type fakeApplier struct {
	mu sync.Mutex

	route      *gatewayv1.HTTPRoute
	applyCalls int
	getCalls   int

	ann    map[string]string
	annErr error

	applyErr error

	// statusAt, if set, is called on every Get to decide what
	// status.Parents should be for that call — call numbers are 0-indexed.
	// Simulates a controller writing status asynchronously over time.
	statusAt func(call int) []gatewayv1.RouteParentStatus

	// getErrAlways, if set, makes every Get fail with this error (simulating
	// a transport failure distinct from "not found").
	getErrAlways error
}

func (f *fakeApplier) Apply(_ context.Context, route *gatewayv1.HTTPRoute) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applyCalls++
	if f.applyErr != nil {
		return f.applyErr
	}
	cp := route.DeepCopy()
	f.route = cp
	return nil
}

func (f *fakeApplier) Get(_ context.Context, namespace, name string) (*gatewayv1.HTTPRoute, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := f.getCalls
	f.getCalls++
	if f.getErrAlways != nil {
		return nil, f.getErrAlways
	}
	if f.route == nil {
		return nil, fmt.Errorf("route %s/%s: %w", namespace, name, ErrNotFound)
	}
	out := f.route.DeepCopy()
	if f.statusAt != nil {
		out.Status.Parents = f.statusAt(call)
	}
	return out, nil
}

func (f *fakeApplier) HTTPRouteCRDAnnotations(_ context.Context) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.annErr != nil {
		return nil, f.annErr
	}
	return f.ann, nil
}

// supportedAnnotations is a fake cluster's CRD annotations that pass
// CheckSupport, so tests below exercise ApplyRoute's own logic rather than
// tripping over D3's refusal.
func supportedAnnotations() map[string]string {
	return map[string]string{
		BundleVersionAnnotation: "v" + MinBundleVersion + ".0",
		ChannelAnnotation:       RequiredChannel,
	}
}

func testSpec() RouteSpec {
	return RouteSpec{
		Name: "route-acme", Namespace: "shepherd-receiver", TenantID: "acme", Kind: KindOTLP,
		RouteSegment: "acme-a1b2c3", GatewayName: "shepherd-gw", BackendName: "receiver", BackendPort: 4318,
	}
}

func acceptedParents(gwName string) []gatewayv1.RouteParentStatus {
	return []gatewayv1.RouteParentStatus{{
		ParentRef: gatewayv1.ParentReference{Name: gatewayv1.ObjectName(gwName)},
		Conditions: []metav1.Condition{
			{Type: string(gatewayv1.RouteConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
		},
	}}
}

func TestApplyRoute_AttachesImmediately(t *testing.T) {
	spec := testSpec()
	fa := &fakeApplier{
		ann:      supportedAnnotations(),
		statusAt: func(int) []gatewayv1.RouteParentStatus { return acceptedParents(spec.GatewayName) },
	}
	route, err := ApplyRoute(context.Background(), fa, spec, ApplyOptions{PollInterval: 5 * time.Millisecond, Deadline: time.Second})
	if err != nil {
		t.Fatalf("ApplyRoute: %v", err)
	}
	if route == nil {
		t.Fatalf("ApplyRoute returned nil route with nil error")
	}
	if fa.applyCalls != 1 {
		t.Fatalf("want exactly 1 Apply call, got %d", fa.applyCalls)
	}
}

// TestApplyRoute_PollsThroughPending simulates a real controller: no status
// for the first two Gets, Accepted=True from the third. ApplyRoute must
// retry rather than fail on the first pending read.
func TestApplyRoute_PollsThroughPending(t *testing.T) {
	spec := testSpec()
	fa := &fakeApplier{
		ann: supportedAnnotations(),
		statusAt: func(call int) []gatewayv1.RouteParentStatus {
			if call < 2 {
				return nil
			}
			return acceptedParents(spec.GatewayName)
		},
	}
	route, err := ApplyRoute(context.Background(), fa, spec, ApplyOptions{PollInterval: 5 * time.Millisecond, Deadline: 2 * time.Second})
	if err != nil {
		t.Fatalf("ApplyRoute: %v", err)
	}
	if route == nil {
		t.Fatalf("ApplyRoute returned nil route with nil error")
	}
	if fa.getCalls < 3 {
		t.Fatalf("want ApplyRoute to have polled at least 3 times before attaching, got %d", fa.getCalls)
	}
}

// TestApplyRoute_RefusalStopsPollingEarly asserts the load-bearing behaviour
// from the plan's R1 note: a definitive refusal (Accepted=False) is reported
// promptly, not after burning the whole deadline. Deadline is set far larger
// than the test's own timeout budget so a passing run proves early exit,
// not luck.
func TestApplyRoute_RefusalStopsPollingEarly(t *testing.T) {
	spec := testSpec()
	fa := &fakeApplier{
		ann: supportedAnnotations(),
		statusAt: func(int) []gatewayv1.RouteParentStatus {
			return []gatewayv1.RouteParentStatus{{
				ParentRef: gatewayv1.ParentReference{Name: gatewayv1.ObjectName(spec.GatewayName)},
				Conditions: []metav1.Condition{
					{
						Type: string(gatewayv1.RouteConditionAccepted), Status: metav1.ConditionFalse,
						Reason: "NotAllowedByListeners", Message: "namespace not in allowedRoutes",
					},
				},
			}}
		},
	}
	start := time.Now()
	_, err := ApplyRoute(context.Background(), fa, spec, ApplyOptions{PollInterval: 5 * time.Millisecond, Deadline: 10 * time.Second})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("want a refusal error, got nil")
	}
	if !strings.Contains(err.Error(), "refused attachment") || !strings.Contains(err.Error(), "NotAllowedByListeners") {
		t.Fatalf("error does not name the refusal reason: %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("ApplyRoute took %s to report a definitive refusal — it should stop polling immediately, "+
			"not wait out a 10s deadline", elapsed)
	}
}

// TestApplyRoute_DeadlineExpiryIsFailure is the point 2 obligation made
// explicit: status that never arrives must end in an error, never a nil/
// success.
//
// Red run, executed: replacing ApplyRoute's deadline branch with
// `return nil, nil` (plus `_ = lastErr` so it still compiles — a revert that
// only breaks the build proves nothing about this test) fails here with
//
//	apply_test.go:348: want a deadline-expiry error, got nil (route=<nil>) —
//	this must never be treated as success
//
// which is precisely the silent non-routing R1 exists to prevent: a route
// that never attached, reported as applied.
func TestApplyRoute_DeadlineExpiryIsFailure(t *testing.T) {
	spec := testSpec()
	fa := &fakeApplier{
		ann:      supportedAnnotations(),
		statusAt: func(int) []gatewayv1.RouteParentStatus { return nil }, // never reports status
	}
	route, err := ApplyRoute(context.Background(), fa, spec, ApplyOptions{PollInterval: 5 * time.Millisecond, Deadline: 40 * time.Millisecond})
	if err == nil {
		t.Fatalf("want a deadline-expiry error, got nil (route=%v) — this must never be treated as success", route)
	}
	if route != nil {
		t.Fatalf("want nil route on failure, got %v", route)
	}
	if !strings.Contains(err.Error(), "did not verify as attached") || !strings.Contains(err.Error(), "FAILURE") {
		t.Fatalf("error does not read as an explicit failure, not an assumed success: %v", err)
	}
}

// TestApplyRoute_ResolvedRefsFalseIsRefused pins the second half of point 1:
// Accepted=True is NOT sufficient on its own — a route can be accepted while
// its backendRef does not resolve, and that must be surfaced as a refusal.
func TestApplyRoute_ResolvedRefsFalseIsRefused(t *testing.T) {
	spec := testSpec()
	fa := &fakeApplier{
		ann: supportedAnnotations(),
		statusAt: func(int) []gatewayv1.RouteParentStatus {
			return []gatewayv1.RouteParentStatus{{
				ParentRef: gatewayv1.ParentReference{Name: gatewayv1.ObjectName(spec.GatewayName)},
				Conditions: []metav1.Condition{
					{Type: string(gatewayv1.RouteConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
					{
						Type: string(gatewayv1.RouteConditionResolvedRefs), Status: metav1.ConditionFalse,
						Reason: "BackendNotFound", Message: "service \"receiver\" not found",
					},
				},
			}}
		},
	}
	_, err := ApplyRoute(context.Background(), fa, spec, ApplyOptions{PollInterval: 5 * time.Millisecond, Deadline: time.Second})
	if err == nil {
		t.Fatalf("want a refusal error for ResolvedRefs=False, got nil")
	}
	if !strings.Contains(err.Error(), "BackendNotFound") {
		t.Fatalf("error does not name the ResolvedRefs failure reason: %v", err)
	}
}

// TestApplyRoute_RefusesUnsupportedClusterBeforeApplying is D3/point 3: an
// old or wrong-channel cluster must be refused BEFORE anything is applied,
// not discovered only after a route silently routes nothing.
func TestApplyRoute_RefusesUnsupportedClusterBeforeApplying(t *testing.T) {
	spec := testSpec()
	fa := &fakeApplier{
		ann: map[string]string{
			BundleVersionAnnotation: "v1.2", // below MinBundleVersion
			ChannelAnnotation:       RequiredChannel,
		},
	}
	_, err := ApplyRoute(context.Background(), fa, spec, ApplyOptions{})
	if err == nil {
		t.Fatalf("want a D3 refusal for an unsupported cluster, got nil")
	}
	var unsupported *ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("want *ErrUnsupported, got %T: %v", err, err)
	}
	if fa.applyCalls != 0 {
		t.Fatalf("want zero Apply calls when the cluster is refused up front, got %d", fa.applyCalls)
	}
}

// TestApplyRoute_TransientGetErrorsAreRetried covers the "reading status
// itself fails" path (distinct from ErrNotFound, which Get never returns
// here since Apply already created the object) — ApplyRoute must keep
// polling rather than treating a transient read failure as a refusal.
func TestApplyRoute_TransientGetErrorsAreRetried(t *testing.T) {
	spec := testSpec()
	fa := &fakeApplier{
		ann:          supportedAnnotations(),
		getErrAlways: errors.New("connection reset by peer"),
	}
	_, err := ApplyRoute(context.Background(), fa, spec, ApplyOptions{PollInterval: 5 * time.Millisecond, Deadline: 30 * time.Millisecond})
	if err == nil {
		t.Fatalf("want an eventual deadline error, got nil")
	}
	if fa.getCalls < 2 {
		t.Fatalf("want ApplyRoute to retry past a transient Get error, got only %d Get calls", fa.getCalls)
	}
	if !strings.Contains(err.Error(), "connection reset by peer") {
		t.Fatalf("deadline error should surface the last transient failure, got: %v", err)
	}
}
