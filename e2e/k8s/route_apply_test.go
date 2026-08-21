//go:build e2ek8s

package k8s_test

import (
	"context"
	"fmt"
	"shepherd/internal/gateway"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/utils"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// TestOperatorOwnedGatewayAttachment closes the R1 obligation for D8's
// operator-owned mode specifically: gateway.ApplyRoute (internal/gateway/apply.go)
// must verify ATTACHMENT — status.parents[].conditions — not creation, and a
// route to a Gateway whose allowedRoutes does not admit our namespace must
// come back as a clear refusal rather than a silently-created, silently-not-
// routing object. route_conformance_test.go already proves RenderHTTPRoute
// itself routes (G3/G4) for a Shepherd-managed Gateway in the SAME namespace
// as the route; this test is the missing half: a Gateway Shepherd did not
// create, living in a DIFFERENT namespace, standing in for "an operator runs
// this out of band".
//
// Both Gateways below are created by this test only to give ApplyRoute
// something real to attach to — in production, D8's whole point is that
// Shepherd's code path (gateway.ApplyRoute via cfgApplier) never creates or
// touches the Gateway object, only the HTTPRoute. That asymmetry is exactly
// what cfgApplier reflects: it has no method that could create a Gateway.
func TestOperatorOwnedGatewayAttachment(t *testing.T) {
	var (
		f        *fixture
		nginxSvc string
	)
	const (
		operatorNS  = "shepherd-route-apply-operator"
		gwDenyName  = "operator-gw-deny"
		gwAdmitName = "operator-gw-admit"
		backendName = "echo"
		backendPort = 8080
		tenant      = "acme"
		segDeny     = "acme-opdeny1"
		segAdmit    = "acme-opadmit1"
	)

	feat := features.New("operator-owned gateway attachment verification").
		WithLabel("suite", "gateway").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			requireCNIVerified(t)
			if err := gatewayv1.Install(cfg.Client().Resources().GetScheme()); err != nil {
				t.Fatalf("registering Gateway API types with the client scheme: %v", err)
			}

			f = newFixture(ctx, t, cfg, "route-apply")

			// operatorNS stands in for infrastructure an operator runs out of
			// band (D8). Idempotent create, same pattern newFixture uses for f.ns.
			_ = utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s delete namespace %s --ignore-not-found --wait --timeout=2m",
				cfg.KubeconfigFile(), operatorNS))
			if err := cfg.Client().Resources().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: operatorNS}}); err != nil {
				t.Fatalf("creating operator namespace %s: %v", operatorNS, err)
			}

			// The backend our route's backendRef points at. Left in f.ns
			// (same namespace as the HTTPRoute) deliberately: a cross-namespace
			// backendRef needs a ReferenceGrant, which is an orthogonal Gateway
			// API mechanism this test is not about.
			pod := echoBackendPod(f.ns)
			if err := cfg.Client().Resources().Create(ctx, pod); err != nil {
				t.Fatalf("creating echo backend pod: %v", err)
			}
			if err := cfg.Client().Resources().Create(ctx, echoBackendService(f.ns, backendName, backendPort)); err != nil {
				t.Fatalf("creating echo backend service: %v", err)
			}
			if err := wait.For(
				conditions.New(cfg.Client().Resources()).PodReady(pod),
				wait.WithTimeout(2*time.Minute), wait.WithInterval(2*time.Second),
			); err != nil {
				t.Fatalf("echo backend pod never became ready: %v", err)
			}

			// gwDeny admits routes from its OWN namespace only — our HTTPRoute
			// lives in f.ns, so it must be refused.
			gwDeny := operatorGateway(operatorNS, gwDenyName, gatewayv1.NamespacesFromSame)
			if err := cfg.Client().Resources().Create(ctx, gwDeny); err != nil {
				t.Fatalf("creating deny-case Gateway %s/%s: %v", operatorNS, gwDenyName, err)
			}
			// gwAdmit admits routes from ANY namespace — our HTTPRoute must attach.
			gwAdmit := operatorGateway(operatorNS, gwAdmitName, gatewayv1.NamespacesFromAll)
			if err := cfg.Client().Resources().Create(ctx, gwAdmit); err != nil {
				t.Fatalf("creating admit-case Gateway %s/%s: %v", operatorNS, gwAdmitName, err)
			}
			waitGatewayProgrammed(t, ctx, cfg, operatorNS, gwDenyName, gatewayReadyDeadline)
			waitGatewayProgrammed(t, ctx, cfg, operatorNS, gwAdmitName, gatewayReadyDeadline)

			nginxSvc = waitProvisionedServiceName(t, ctx, cfg, operatorNS, gwAdmitName, gatewayReadyDeadline)
			waitDeploymentAvailable(t, cfg, operatorNS, nginxSvc)
			return ctx
		}).
		Assess("a route to a Gateway whose allowedRoutes does not admit our namespace is reported as a refusal",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				spec := gateway.RouteSpec{
					Name: "route-deny", Namespace: f.ns, TenantID: tenant, Kind: gateway.KindOTLP,
					RouteSegment: segDeny, GatewayName: gwDenyName, GatewayNamespace: operatorNS,
					BackendName: backendName, BackendPort: backendPort,
				}
				applier := cfgApplier{cfg: cfg}
				_, err := gateway.ApplyRoute(ctx, applier, spec, gateway.ApplyOptions{
					PollInterval: 3 * time.Second, Deadline: gatewayReadyDeadline,
				})
				if err == nil {
					t.Fatalf("ApplyRoute succeeded for a route to a Gateway that does not admit our namespace — " +
						"this is exactly the silent non-routing D3/R1 exist to prevent")
				}
				if !strings.Contains(err.Error(), "refused attachment") {
					t.Fatalf("ApplyRoute failed, but not with the expected refusal shape (want \"refused "+
						"attachment\" in the error): %v", err)
				}
				t.Logf("ApplyRoute correctly refused: %v", err)

				// Prove this was a DETECTED refusal, not a creation failure: the
				// HTTPRoute object itself exists in the cluster, with Accepted
				// something other than True.
				var route gatewayv1.HTTPRoute
				if getErr := cfg.Client().Resources().Get(ctx, spec.Name, spec.Namespace, &route); getErr != nil {
					t.Fatalf("the deny-case HTTPRoute was not actually created — ApplyRoute's error masks a "+
						"different failure than the one this test claims to prove: %v", getErr)
				}
				for _, parent := range route.Status.Parents {
					for _, c := range parent.Conditions {
						if c.Type == string(gatewayv1.RouteConditionAccepted) && c.Status == metav1.ConditionTrue {
							t.Fatalf("HTTPRoute %s/%s shows Accepted=True in the cluster despite ApplyRoute "+
								"reporting a refusal — the object was created (as expected) but ApplyRoute's "+
								"verdict does not match the cluster's own status", spec.Namespace, spec.Name)
						}
					}
				}
				t.Logf("confirmed: HTTPRoute %s/%s exists in the cluster (creation succeeded) but is not "+
					"Accepted=True by any parent (attachment correctly refused)", spec.Namespace, spec.Name)
				return ctx
			}).
		Assess("a route to a Gateway whose allowedRoutes admits our namespace attaches and actually routes",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				spec := gateway.RouteSpec{
					Name: "route-admit", Namespace: f.ns, TenantID: tenant, Kind: gateway.KindOTLP,
					RouteSegment: segAdmit, GatewayName: gwAdmitName, GatewayNamespace: operatorNS,
					BackendName: backendName, BackendPort: backendPort,
				}
				applier := cfgApplier{cfg: cfg}
				route, err := gateway.ApplyRoute(ctx, applier, spec, gateway.ApplyOptions{
					PollInterval: 3 * time.Second, Deadline: gatewayReadyDeadline,
				})
				if err != nil {
					t.Fatalf("ApplyRoute failed for a route to a Gateway that DOES admit our namespace: %v", err)
				}
				if route == nil {
					t.Fatalf("ApplyRoute returned a nil route alongside a nil error")
				}
				t.Logf("ApplyRoute verified attachment: %s/%s", route.Namespace, route.Name)

				// Verified != routes. Prove the observable effect through the
				// SAME operator-owned Gateway's data plane, the same way G3 does.
				resp, ok := curlThroughGatewayUntil(ctx, t, cfg, f.ns, operatorNS, "opadmit", nginxSvc, "/otlp/"+segAdmit+"/v1/traces",
					map[string]string{gateway.TenantHeader: "evil-client-value"},
					func(r echoResponse) bool { return r.Path == "/v1/traces" && r.Headers["x-scope-orgid"] == tenant },
					gatewayProbeDeadline)
				if !ok {
					t.Fatalf("attachment was verified but the route never actually delivered traffic within "+
						"%s. Last observed: path=%q headers=%v", gatewayProbeDeadline, resp.Path, resp.Headers)
				}
				t.Logf("operator-owned route actually routes: backend saw path=%q %s=%q",
					resp.Path, gateway.TenantHeader, resp.Headers["x-scope-orgid"])
				return ctx
			}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			f.cleanup(cfg)
			if !keepCluster() {
				_ = utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s delete namespace %s --ignore-not-found --wait=false",
					cfg.KubeconfigFile(), operatorNS))
			}
			return ctx
		}).
		Feature()

	testenv.Test(t, feat)
}

// operatorGateway builds a Gateway in ns admitting routes per `from`. Used
// twice (Same / All) to produce this test's deny and admit cases from one
// definition, differing only in the one field the test is actually about.
func operatorGateway(ns, name string, from gatewayv1.FromNamespaces) *gatewayv1.Gateway {
	return &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: ngfGatewayClassName,
			Listeners: []gatewayv1.Listener{{
				Name:     "http",
				Port:     80,
				Protocol: gatewayv1.HTTPProtocolType,
				AllowedRoutes: &gatewayv1.AllowedRoutes{
					Namespaces: &gatewayv1.RouteNamespaces{From: ptr.To(from)},
				},
			}},
		},
	}
}

// cfgApplier adapts the e2e-framework's *envconf.Config into
// gateway.Applier — closing the loop between R1's apply-and-verify path
// (internal/gateway/apply.go, unit-tested against a fake in that package)
// and a real controller: this is the same integration point W3's
// route_conformance_test.go established for RenderHTTPRoute alone.
type cfgApplier struct {
	cfg *envconf.Config
}

func (a cfgApplier) Apply(ctx context.Context, route *gatewayv1.HTTPRoute) error {
	var existing gatewayv1.HTTPRoute
	err := a.cfg.Client().Resources().Get(ctx, route.Name, route.Namespace, &existing)
	switch {
	case err == nil:
		route.ResourceVersion = existing.ResourceVersion
		return a.cfg.Client().Resources().Update(ctx, route)
	case apierrors.IsNotFound(err):
		return a.cfg.Client().Resources().Create(ctx, route)
	default:
		return fmt.Errorf("checking whether HTTPRoute %s/%s already exists: %w", route.Namespace, route.Name, err)
	}
}

func (a cfgApplier) Get(ctx context.Context, namespace, name string) (*gatewayv1.HTTPRoute, error) {
	var route gatewayv1.HTTPRoute
	if err := a.cfg.Client().Resources().Get(ctx, name, namespace, &route); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("HTTPRoute %s/%s: %w", namespace, name, gateway.ErrNotFound)
		}
		return nil, err
	}
	return &route, nil
}

// HTTPRouteCRDAnnotations reuses readInstalledGatewayAPIAnnotations
// (route_conformance_test.go) — the exact same annotation read D3's
// CheckSupport is exercised against there, so this test's apply path is
// refused/allowed by the identical signal, not a second implementation of
// "read the CRD version" that could drift from the first.
func (a cfgApplier) HTTPRouteCRDAnnotations(ctx context.Context) (map[string]string, error) {
	version, channel, err := readInstalledGatewayAPIAnnotations(a.cfg)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		gateway.BundleVersionAnnotation: version,
		gateway.ChannelAnnotation:       channel,
	}, nil
}
