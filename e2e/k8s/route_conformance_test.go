//go:build e2ek8s

package k8s_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/utils"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"shepherd/internal/gateway"
)

// TestGatewayRoutesAndTenantIsolation closes G3 and G4
// (docs/gateway-tier-plan.md §6), the two gates W3 exists to prove:
//
//	G3  a rendered route ACTUALLY routes: POST through the gateway arrives at
//	    the backend with the prefix rewritten and X-Scope-OrgID injected.
//	G4  tenant isolation, kill-probe style: a request to tenant A's prefix
//	    never arrives tagged as tenant B, and deleting the
//	    RequestHeaderModifier filter must make both go RED.
//
// Every assertion below reads the echo backend's own record of what request
// it received (path, headers) — never the rendered YAML or the controller's
// Accepted/Programmed status alone, which prove a route was PARSED, not that
// it ROUTES (D3's whole distinction).
//
// Uses gateway.RenderHTTPRoute directly rather than hand-written YAML: this
// is the same integration point W4's receiver tier will call, so a passing
// G3/G4 here is proof about the renderer, not about a route this suite wrote
// by hand.
func TestGatewayRoutesAndTenantIsolation(t *testing.T) {
	var (
		f        *fixture
		nginxSvc string
	)
	const (
		gwName      = "shepherd-gw"
		tenantA     = "acme"
		tenantB     = "globex"
		segA        = "acme-a1b2c3"
		segB        = "globex-z9y8x7"
		backendName = "echo"
		backendPort = 8080
	)

	feat := features.New("gateway routing and tenant isolation").
		WithLabel("suite", "gateway").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// Gateway routing depends on ordinary pod-to-pod networking working
			// at all; a failure here on an unverified CNI would be
			// indistinguishable from a real routing defect. requireCNIVerified
			// runs the negative control on demand (sync.Once-guarded) rather
			// than trusting filename-sort ordering, so this also works under
			// `go test -run TestGatewayRoutesAndTenantIsolation` alone.
			requireCNIVerified(t)

			// The e2e-framework client builds its own runtime.Scheme, which knows
			// only the built-in Kubernetes types. Gateway API types are CRDs, so
			// without registering them here every typed Create/Get below fails
			// with "no kind is registered for the type v1.Gateway" — a scheme
			// error, not a cluster or routing problem, which is exactly the kind
			// of failure that reads like a real defect if you skim it.
			if err := gatewayv1.Install(cfg.Client().Resources().GetScheme()); err != nil {
				t.Fatalf("registering Gateway API types with the client scheme: %v", err)
			}

			f = newFixture(ctx, t, cfg, "gateway")

			// The backend both tenants' routes point at. It reflects the
			// request path and headers it actually received, so every
			// assertion below reads what arrived, not what was configured.
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

			gw := &gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{Name: gwName, Namespace: f.ns},
				Spec: gatewayv1.GatewaySpec{
					GatewayClassName: ngfGatewayClassName,
					Listeners: []gatewayv1.Listener{{
						Name:     "http",
						Port:     80,
						Protocol: gatewayv1.HTTPProtocolType,
						AllowedRoutes: &gatewayv1.AllowedRoutes{
							Namespaces: &gatewayv1.RouteNamespaces{From: ptr.To(gatewayv1.NamespacesFromSame)},
						},
					}},
				},
			}
			if err := cfg.Client().Resources().Create(ctx, gw); err != nil {
				t.Fatalf("creating Gateway %s/%s: %v", f.ns, gwName, err)
			}
			waitGatewayProgrammed(t, ctx, cfg, f.ns, gwName, gatewayReadyDeadline)

			// The controller provisions its own data-plane Deployment+Service
			// for this Gateway, labeled with the Gateway's own name — found by
			// that label rather than assuming the controller's internal naming
			// scheme, the same discovery-over-guessing approach
			// simulatorPodNameAndIP uses in fixtures_test.go.
			nginxSvc = waitProvisionedServiceName(t, ctx, cfg, f.ns, gwName, gatewayReadyDeadline)
			waitDeploymentAvailable(t, cfg, f.ns, nginxSvc)

			for _, spec := range []gateway.RouteSpec{
				{
					Name: "route-" + tenantA, Namespace: f.ns, TenantID: tenantA, Kind: gateway.KindOTLP,
					RouteSegment: segA, GatewayName: gwName, BackendName: backendName, BackendPort: backendPort,
				},
				{
					Name: "route-" + tenantB, Namespace: f.ns, TenantID: tenantB, Kind: gateway.KindOTLP,
					RouteSegment: segB, GatewayName: gwName, BackendName: backendName, BackendPort: backendPort,
				},
			} {
				route, err := gateway.RenderHTTPRoute(spec)
				if err != nil {
					t.Fatalf("RenderHTTPRoute(tenant=%s): %v", spec.TenantID, err)
				}
				if err := cfg.Client().Resources().Create(ctx, route); err != nil {
					t.Fatalf("creating HTTPRoute %s: %v", route.Name, err)
				}
			}
			waitHTTPRouteAccepted(t, ctx, cfg, f.ns, "route-"+tenantA, gatewayReadyDeadline)
			waitHTTPRouteAccepted(t, ctx, cfg, f.ns, "route-"+tenantB, gatewayReadyDeadline)
			return ctx
		}).
		Assess("G3: the backend receives the rewritten path and the injected tenant header",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				resp, ok := curlThroughGatewayUntil(cfg, f.ns, "g3", nginxSvc, "/otlp/"+segA+"/v1/traces",
					map[string]string{gateway.TenantHeader: "evil-client-value"},
					func(r echoResponse) bool { return r.Path == "/v1/traces" && r.Headers["x-scope-orgid"] == tenantA },
					gatewayProbeDeadline)
				if !ok {
					t.Fatalf("G3 FAILED: never observed the backend receiving both the rewritten path "+
						"\"/v1/traces\" and %s=%q within %s. Last observed: path=%q headers=%v",
						gateway.TenantHeader, tenantA, gatewayProbeDeadline, resp.Path, resp.Headers)
				}
				t.Logf("G3: backend saw path=%q %s=%q (client sent \"evil-client-value\")",
					resp.Path, gateway.TenantHeader, resp.Headers["x-scope-orgid"])
				return ctx
			}).
		Assess("G4: tenant B's prefix never arrives tagged as tenant A, even when the client claims to be A",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				resp, ok := curlThroughGatewayUntil(cfg, f.ns, "g4-cross", nginxSvc, "/otlp/"+segB+"/v1/traces",
					map[string]string{gateway.TenantHeader: tenantA},
					func(r echoResponse) bool { return r.Headers["x-scope-orgid"] == tenantB },
					gatewayProbeDeadline)
				if !ok {
					t.Fatalf("G4 FAILED: a request to tenant B's prefix (segment %q), with the client "+
						"claiming to be tenant %q, never arrived at the backend tagged %q within %s — "+
						"last observed %s=%q. Cross-tenant isolation is broken: tenant B's path must always "+
						"and only be tagged tenant B, regardless of what the client claims.",
						segB, tenantA, tenantB, gatewayProbeDeadline, gateway.TenantHeader, resp.Headers["x-scope-orgid"])
				}
				t.Logf("G4: request to tenant B's prefix arrived tagged %q despite the client claiming %q",
					resp.Headers["x-scope-orgid"], tenantA)
				return ctx
			}).
		Assess("kill probe: deleting the RequestHeaderModifier filter makes G3/G4 go RED",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				var route gatewayv1.HTTPRoute
				if err := cfg.Client().Resources().Get(ctx, "route-"+tenantA, f.ns, &route); err != nil {
					t.Fatalf("re-reading route-%s to remove its filter: %v", tenantA, err)
				}
				before := len(route.Spec.Rules[0].Filters)
				var kept []gatewayv1.HTTPRouteFilter
				for _, filt := range route.Spec.Rules[0].Filters {
					if filt.Type != gatewayv1.HTTPRouteFilterRequestHeaderModifier {
						kept = append(kept, filt)
					}
				}
				if len(kept) == before {
					t.Fatalf("route-%s had no RequestHeaderModifier filter to remove — the kill probe "+
						"would prove nothing about it", tenantA)
				}
				route.Spec.Rules[0].Filters = kept
				if err := cfg.Client().Resources().Update(ctx, &route); err != nil {
					t.Fatalf("removing RequestHeaderModifier filter from route-%s: %v", tenantA, err)
				}
				// Confirm the mutated route was itself accepted before trusting
				// what comes back through it below.
				waitHTTPRouteAccepted(t, ctx, cfg, f.ns, "route-"+tenantA, gatewayReadyDeadline)

				const spoofed = "evil-client-value"
				resp, ok := curlThroughGatewayUntil(cfg, f.ns, "g3-kill", nginxSvc, "/otlp/"+segA+"/v1/traces",
					map[string]string{gateway.TenantHeader: spoofed},
					func(r echoResponse) bool { return r.Headers["x-scope-orgid"] == spoofed },
					gatewayProbeDeadline)
				if !ok {
					t.Fatalf("WITH THE REQUESTHEADERMODIFIER FILTER REMOVED, THE TENANT HEADER IS STILL "+
						"BEING OVERWRITTEN to %q instead of letting the client-supplied value %q survive. "+
						"If G3/G4 stay green after this filter is gone, they were never proving what they "+
						"claim to prove (plan §8 rule 3). Last observed headers: %v",
						resp.Headers["x-scope-orgid"], spoofed, resp.Headers)
				}
				t.Logf("KILL PROBE CONFIRMED: with the RequestHeaderModifier filter removed, a "+
					"client-supplied %s (%q) now passes straight through to the backend — this is exactly "+
					"what the filter exists to prevent, and G3/G4 above only mean something because this "+
					"goes red without it.", gateway.TenantHeader, resp.Headers["x-scope-orgid"])

				// The kill probe should flip ONLY the property tied to the
				// filter it removed — URLRewrite is still on the route, so path
				// rewriting must still be correct.
				if resp.Path != "/v1/traces" {
					t.Fatalf("removing RequestHeaderModifier unexpectedly also broke URLRewrite: "+
						"backend saw path %q, want \"/v1/traces\"", resp.Path)
				}
				return ctx
			}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			f.cleanup(cfg)
			return ctx
		}).
		Feature()

	testenv.Test(t, feat)
}

const (
	// gatewayReadyDeadline bounds how long this suite waits for the Gateway
	// API controller to program a Gateway/HTTPRoute or provision the
	// data-plane Deployment+Service behind it.
	gatewayReadyDeadline = 2 * time.Minute
	// gatewayProbeDeadline bounds how long curlThroughGatewayUntil retries
	// before concluding the expected observable effect never happened. Like
	// probeMustDenyDeadline in simulator_containment_test.go, this is a
	// convergence budget, not a per-run cost: a converged route answers on
	// the first or second attempt, and the full deadline only burns while the
	// controller is still reprogramming nginx after a route change (observed
	// during development: the kill probe's mutated route needed a few seconds
	// to actually take effect on the data plane, same as a fresh CNI policy
	// needing time to program in the containment probes).
	gatewayProbeDeadline = 90 * time.Second

	// ngfChartVersion is the NGINX Gateway Fabric release this suite installs
	// as the Gateway API controller under test.
	//
	// NGINX Gateway Fabric (NGF), not Envoy Gateway (the plan's stated
	// default), for three reasons found while building this suite:
	//
	//  1. NGF does not bundle or manage Gateway API CRDs itself — its own
	//     install docs say to apply the Standard channel CRDs BEFORE
	//     deploying NGF, and its Helm chart carries none. That means the CRDs
	//     actually present in the cluster during G3/G4 are exactly the ones
	//     installGatewayAPI installs from deploy/versions.env — not whatever
	//     channel a controller's chart happens to default to. Envoy Gateway's
	//     Helm chart, by contrast, bundles and installs Experimental-channel
	//     CRDs by default, which would silently broaden what the API server
	//     accepts past the Standard-channel contract D2 depends on, and
	//     fighting that (disabling its CRD management, hoping nothing
	//     reconciles over the pinned ones) is exactly the kind of
	//     "I believe this would work" this repo's rule 2 rejects without a
	//     red run to prove it.
	//  2. Chart version compatibility: NGF's chart pins
	//     `kubeVersion: ">= 1.31.0-0"` at v2.6.0, matching this suite's
	//     kindest/node:v1.31.4 exactly (verified against the chart's own
	//     Chart.yaml at each tag, not recalled). v2.6.7 (latest at the time
	//     of writing) already requires >= 1.32.0-0 and would refuse to
	//     install here.
	//  3. Verified live end-to-end while building this suite, on a throwaway
	//     kind cluster with this exact CRD pin: CRD install -> `helm install`
	//     -> Gateway -> HTTPRoute (by hand, then via RenderHTTPRoute) -> a
	//     real routed, header-injected request -> a live kill probe that
	//     reverted to passing a spoofed header straight through -- the whole
	//     cycle in well under a minute once images were warm, no
	//     cert-manager or other control-plane dependency.
	//
	// If NGF ever proves too heavy or flaky here, Contour is the next
	// candidate for the same reason (2): check its chart's Gateway API CRD
	// management story before Envoy Gateway's.
	ngfChartVersion = "2.6.0"
	ngfChartRef     = "oci://ghcr.io/nginx/charts/nginx-gateway-fabric"
	ngfNamespace    = "nginx-gateway-system"
	ngfRelease      = "ngf"
	// ngfGatewayClassName is the chart's own default
	// (values.yaml nginxGateway.gatewayClassName) — left un-overridden, so
	// this constant documents what the chart actually creates rather than
	// asking it to create something else.
	ngfGatewayClassName = "nginx"
	ngfInstallTimeout   = 4 * time.Minute
)

// installGatewayAPI applies the PINNED Gateway API CRDs — version and channel
// read from deploy/versions.env at runtime, never hardcoded here, so this
// suite proves conformance against the exact floor internal/gateway.CheckSupport
// requires rather than a version chosen independently by this file (plan §8
// rule 6).
//
// After applying, it reads back the installed CRD's own bundle-version/channel
// annotations — the same signal CheckSupport reads in production — and calls
// CheckSupport on them directly. That closes the loop between D3's unit-tested
// refusal logic (internal/gateway/version_test.go) and what a real cluster
// reports, which G2 alone does not: G2 proves CheckSupport is correct given
// some annotations, not that this suite's own install produces the
// annotations it thinks it does.
func installGatewayAPI(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
	version, err := readVersionsEnvValue("GATEWAY_API_VERSION")
	if err != nil {
		return ctx, err
	}
	channel, err := readVersionsEnvValue("GATEWAY_API_CHANNEL")
	if err != nil {
		return ctx, err
	}

	url := fmt.Sprintf("https://github.com/kubernetes-sigs/gateway-api/releases/download/%s/%s-install.yaml",
		version, channel)
	log.Printf("installing Gateway API CRDs %s (%s channel) from %s", version, channel, url)
	if p := utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s apply -f %s", cfg.KubeconfigFile(), url)); p.Err() != nil {
		return ctx, fmt.Errorf("applying Gateway API CRDs from %s: %w: %s", url, p.Err(), p.Result())
	}

	gotVersion, gotChannel, err := readInstalledGatewayAPIAnnotations(cfg)
	if err != nil {
		return ctx, err
	}
	if err := gateway.CheckSupport(gotVersion, gotChannel); err != nil {
		return ctx, fmt.Errorf("the Gateway API CRDs this suite just installed do not pass Shepherd's own "+
			"CheckSupport — the kind suite's install and the D3 refusal logic have drifted apart: %w", err)
	}
	log.Printf("Gateway API CRDs installed and self-verified: bundle-version=%s channel=%s", gotVersion, gotChannel)
	return ctx, nil
}

// readInstalledGatewayAPIAnnotations reads the HTTPRoute CRD's own
// bundle-version/channel annotations — the canonical discovery mechanism
// internal/gateway.CheckSupport documents — from the live cluster, rather
// than assuming the apply above produced what was asked for.
func readInstalledGatewayAPIAnnotations(cfg *envconf.Config) (version, channel string, err error) {
	p := utils.RunCommand(fmt.Sprintf(
		"kubectl --kubeconfig %s get crd httproutes.gateway.networking.k8s.io -o jsonpath={.metadata.annotations}",
		cfg.KubeconfigFile()))
	if p.Err() != nil {
		return "", "", fmt.Errorf("reading installed HTTPRoute CRD annotations: %w: %s", p.Err(), p.Result())
	}
	var ann map[string]string
	if err := json.Unmarshal([]byte(p.Result()), &ann); err != nil {
		return "", "", fmt.Errorf("parsing HTTPRoute CRD annotations %q: %w", p.Result(), err)
	}
	return ann[gateway.BundleVersionAnnotation], ann[gateway.ChannelAnnotation], nil
}

// installGatewayController installs NGINX Gateway Fabric — see ngfChartVersion
// for why this controller. `helm install --wait` blocks until its Deployment
// is Available, but not until the GatewayClass it creates is Accepted or any
// per-Gateway data plane exists (there is none yet at cluster setup time,
// before any feature creates a Gateway) — those are checked per-feature by
// waitGatewayProgrammed/waitProvisionedServiceName.
func installGatewayController(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
	log.Printf("installing NGINX Gateway Fabric %s (Gateway API controller under test)", ngfChartVersion)
	cmd := fmt.Sprintf(
		"helm install %s %s --version %s --kubeconfig %s -n %s --create-namespace --wait --timeout %s",
		ngfRelease, ngfChartRef, ngfChartVersion, cfg.KubeconfigFile(), ngfNamespace, ngfInstallTimeout,
	)
	if p := utils.RunCommand(cmd); p.Err() != nil {
		return ctx, fmt.Errorf("installing nginx-gateway-fabric %s: %w: %s", ngfChartVersion, p.Err(), p.Result())
	}
	return ctx, nil
}

// versionsEnvPath is deploy/versions.env, resolved relative to this package.
const versionsEnvPath = "../../deploy/versions.env"

var versionsEnvLineRE = regexp.MustCompile(`(?m)^([A-Z_]+)=(.*)$`)

// readVersionsEnvValue reads one KEY=value line from deploy/versions.env —
// the one place GATEWAY_API_VERSION/GATEWAY_API_CHANNEL are pinned, per D1.
func readVersionsEnvValue(key string) (string, error) {
	data, err := os.ReadFile(versionsEnvPath)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", versionsEnvPath, err)
	}
	for _, m := range versionsEnvLineRE.FindAllStringSubmatch(string(data), -1) {
		if m[1] == key {
			return strings.TrimSpace(m[2]), nil
		}
	}
	return "", fmt.Errorf("%s not found in %s", key, versionsEnvPath)
}

// waitGatewayProgrammed polls a Gateway's status until its Programmed
// condition is True — the controller has assigned it a data plane and
// accepted its listeners — or the deadline expires.
func waitGatewayProgrammed(t *testing.T, ctx context.Context, cfg *envconf.Config, ns, name string, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	var last []metav1.Condition
	for time.Now().Before(end) {
		var gw gatewayv1.Gateway
		if err := cfg.Client().Resources().Get(ctx, name, ns, &gw); err == nil {
			last = gw.Status.Conditions
			for _, c := range gw.Status.Conditions {
				if c.Type == string(gatewayv1.GatewayConditionProgrammed) && c.Status == metav1.ConditionTrue {
					return
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("Gateway %s/%s never reported Programmed=True within %s (last status.conditions: %+v)",
		ns, name, deadline, last)
}

// waitHTTPRouteAccepted polls an HTTPRoute's status until at least one parent
// reports Accepted=True, or the deadline expires. Accepted is necessary but
// not sufficient for actually routing (D3's distinction) — the curl-through
// assertions are what prove the rest.
func waitHTTPRouteAccepted(t *testing.T, ctx context.Context, cfg *envconf.Config, ns, name string, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	var last []gatewayv1.RouteParentStatus
	for time.Now().Before(end) {
		var route gatewayv1.HTTPRoute
		if err := cfg.Client().Resources().Get(ctx, name, ns, &route); err == nil {
			last = route.Status.Parents
			for _, parent := range route.Status.Parents {
				for _, c := range parent.Conditions {
					if c.Type == string(gatewayv1.RouteConditionAccepted) && c.Status == metav1.ConditionTrue {
						return
					}
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("HTTPRoute %s/%s never reported Accepted=True on any parent within %s (last status.parents: %+v)",
		ns, name, deadline, last)
}

// waitProvisionedServiceName finds the data-plane Service the controller
// provisions for one Gateway, discovered by the label the Gateway API project
// itself defines for this (gateway.networking.k8s.io/gateway-name) rather
// than by guessing the controller's internal naming scheme — the same
// discovery-over-guessing approach simulatorPodNameAndIP takes in
// fixtures_test.go. Retries because the Service can briefly lag Programmed=True.
func waitProvisionedServiceName(t *testing.T, ctx context.Context, cfg *envconf.Config, ns, gwName string, deadline time.Duration) string {
	t.Helper()
	sel := fmt.Sprintf("gateway.networking.k8s.io/gateway-name=%s", gwName)
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		var svcs corev1.ServiceList
		if err := cfg.Client().Resources(ns).List(ctx, &svcs, resources.WithLabelSelector(sel)); err == nil && len(svcs.Items) == 1 {
			return svcs.Items[0].Name
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("no single Service labeled %q ever appeared in %s within %s — the controller did not "+
		"provision a data-plane Service for Gateway %s", sel, ns, deadline, gwName)
	return ""
}

// echoResponse is the subset of mendhak/http-https-echo's JSON response body
// this suite cares about: the request path and headers the backend actually
// received. That image reflects the request rather than answering it, which
// is exactly what "assert the observable effect, never that the YAML
// rendered" needs here — a plain 200 OK would prove routing happened but say
// nothing about what arrived.
type echoResponse struct {
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
}

// curlThroughGatewayUntil retries a POST through the gateway Service until
// accept(response) is true or the deadline expires, returning the last
// observed response either way. A retry loop, not a single dial, for the same
// reason dialUntilIn and probeFromPodUntil retry elsewhere in this suite:
// nginx needs a moment to reprogram after a route is created or changed, and
// a single early probe fails for a reason that has nothing to do with what is
// being tested.
func curlThroughGatewayUntil(
	cfg *envconf.Config, ns, base, svcName, path string, headers map[string]string,
	accept func(echoResponse) bool, deadline time.Duration,
) (echoResponse, bool) {
	end := time.Now().Add(deadline)
	var last echoResponse
	for i := 0; time.Now().Before(end); i++ {
		if resp, ok := curlThroughGatewayOnce(cfg, ns, fmt.Sprintf("%s-%d", base, i), svcName, path, headers); ok {
			last = resp
			if accept(resp) {
				return resp, true
			}
		}
		time.Sleep(3 * time.Second)
	}
	return last, false
}

// curlThroughGatewayOnce runs a one-shot Pod (kubectl run --rm --attach, the
// same pattern dialIn uses in negative_control_test.go — unlike
// probeFromPod's kubectl-debug-into-an-existing-pod, plain `kubectl run`
// propagates its exit code reliably) that POSTs to the gateway Service's
// in-cluster DNS name and captures the echo backend's JSON response. Success
// is judged by whether the output PARSES, matching this suite's rule to judge
// by observed output rather than exit status.
func curlThroughGatewayOnce(cfg *envconf.Config, ns, name, svcName, path string, headers map[string]string) (echoResponse, bool) {
	var hdrArgs strings.Builder
	for k, v := range headers {
		fmt.Fprintf(&hdrArgs, " -H %s", strconv.Quote(fmt.Sprintf("%s: %s", k, v)))
	}
	url := fmt.Sprintf("http://%s.%s.svc.cluster.local%s", svcName, ns, path)
	cmd := fmt.Sprintf(
		"kubectl --kubeconfig %s -n %s run %s --image=curlimages/curl:8.11.1 --restart=Never --rm --attach "+
			"--quiet --pod-running-timeout=2m --command -- curl -s -X POST%s %s",
		cfg.KubeconfigFile(), ns, name, hdrArgs.String(), strconv.Quote(url),
	)
	p := utils.RunCommand(cmd)
	var resp echoResponse
	if err := json.Unmarshal([]byte(p.Result()), &resp); err != nil {
		return echoResponse{}, false
	}
	return resp, true
}

// echoBackendPod is the tenant routes' shared backend: mendhak/http-https-echo
// reflects the request path and headers it received in its JSON response body
// (see echoResponse), which is what lets G3/G4 assert the observable effect
// of a route rather than its rendered YAML.
func echoBackendPod(ns string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "echo", Namespace: ns, Labels: map[string]string{"app": "echo"}},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "echo",
				Image: "mendhak/http-https-echo:32",
				Ports: []corev1.ContainerPort{{ContainerPort: 8080}},
			}},
		},
	}
}

func echoBackendService(ns, name string, port int32) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "echo"},
			Ports: []corev1.ServicePort{{
				Port: port, TargetPort: intstr.FromInt32(port), Protocol: corev1.ProtocolTCP,
			}},
		},
	}
}
