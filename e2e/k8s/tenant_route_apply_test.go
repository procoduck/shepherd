//go:build e2ek8s

package k8s_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/utils"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/routeapply"
)

// TestTenantRouteApply is the full loop of docs/archive/plans/2026-09-29-tenant-route-
// apply.md (PR 5): a tenant route created through Shepherd's Connect API — no
// hand-written YAML anywhere — is applied to the cluster by the chart-deployed
// Shepherd's own reconciler, under exactly the RBAC the chart grants, and OTLP
// sent to its path reaches the backend carrying the org's tenant. Rotation
// keeps both segments routing through the overlap; revocation removes the
// HTTPRoute and the path stops routing; a Gateway that does not admit
// Shepherd's namespace shows as refused with the gateway's reason.
//
// Each of those is a claim the unit specs (internal/routeapply, the chart's
// RBAC specs) make against fakes. This is where the reconciler meets a real
// apiserver, a real gateway controller and the chart's real grants — the
// "verify a control at the layer it is consumed at" rule.
func TestTenantRouteApply(t *testing.T) {
	var (
		f      *fixture
		gwSvc  string
		api    *shepherdAPI
		orgID  string
		first  *mgmtv1.TenantRoute
		second *mgmtv1.TenantRoute
	)
	const (
		release  = "shepherd-rta"
		gwNS     = "shepherd-rta-gw"
		gwName   = "rta-gw"
		closedGW = "rta-closed-gw"
		sinkName = "rta-sink"
		sinkPort = 8080
		tenant   = "initech"
		password = "e2e-route-apply-admin-pass"
	)
	receiver := release + "-receiver"

	feat := features.New("tenant-route apply: Connect API → reconciler → HTTPRoute → gateway → receiver").
		WithLabel("suite", "receiver").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			requireCNIVerified(t)
			if err := gatewayv1.Install(cfg.Client().Resources().GetScheme()); err != nil {
				t.Fatalf("registering Gateway API types with the client scheme: %v", err)
			}
			f = newFixture(ctx, t, cfg, "route-apply")

			// A chosen bootstrap password: the default admin/admin must be
			// changed before anything else works.
			out := kubectlArgv(cfg, "-n", f.ns, "patch", "secret", chartSecretName, "--type=merge",
				"-p", fmt.Sprintf(`{"stringData":{"SHEPHERD_BOOTSTRAP_ADMIN_PASSWORD":%q}}`, password))
			if !strings.Contains(out, "patched") {
				t.Fatalf("setting the bootstrap admin password: %s", out)
			}

			_ = utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s delete namespace %s --ignore-not-found --wait --timeout=2m",
				cfg.KubeconfigFile(), gwNS))
			if err := cfg.Client().Resources().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: gwNS}}); err != nil {
				t.Fatalf("creating gateway namespace %s: %v", gwNS, err)
			}

			sink := receiverSinkPod(f.ns, sinkName)
			if err := cfg.Client().Resources().Create(ctx, sink); err != nil {
				t.Fatalf("creating sink pod: %v", err)
			}
			if err := cfg.Client().Resources().Create(ctx, receiverSinkService(f.ns, sinkName, sinkPort)); err != nil {
				t.Fatalf("creating sink service: %v", err)
			}
			if err := wait.For(conditions.New(cfg.Client().Resources()).PodReady(sink),
				wait.WithTimeout(2*time.Minute), wait.WithInterval(2*time.Second)); err != nil {
				t.Fatalf("sink pod never became ready: %v", err)
			}

			// The receiver with tenant-route apply at its chart default (on).
			// A short interval so each step below converges in seconds.
			values := fmt.Sprintf(`
extraEnv:
  - {name: SHEPHERD_GATEWAY_ROUTES_APPLY_INTERVAL, value: "5s"}
receiver:
  enabled: true
  mode: pass_through
  batch: {timeout: "1s", sendBatchSize: 8192, sendBatchMaxSize: 0}
  exporters:
    traces: {enabled: true, protocol: http, endpoint: "http://%[1]s.%[2]s.svc.cluster.local:%[3]d"}
  networkPolicy:
    gatewayFrom:
      - namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: %[4]s}}
        podSelector: {matchLabels: {gateway.networking.k8s.io/gateway-name: %[5]s}}
    egress:
      - to:
          - namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: %[2]s}}
            podSelector: {matchLabels: {app: %[1]s}}
        ports: [{port: %[3]d, protocol: TCP}]
`, sinkName, f.ns, sinkPort, gwNS, gwName)
			valuesFile := filepath.Join(t.TempDir(), "route-apply-values.yaml")
			if err := os.WriteFile(valuesFile, []byte(values), 0o600); err != nil {
				t.Fatalf("writing values: %v", err)
			}
			helmRun(t, cfg, f, "install", release, "--set replicas=1", "-f "+valuesFile)
			waitDeploymentAvailable(t, cfg, f.ns, release)
			waitDeploymentAvailable(t, cfg, f.ns, receiver)

			// One Gateway that admits routes from any namespace, and one that
			// admits only its own — Shepherd's routes cannot attach to it.
			for _, gw := range []*gatewayv1.Gateway{
				operatorGateway(gwNS, gwName, gatewayv1.NamespacesFromAll),
				operatorGateway(gwNS, closedGW, gatewayv1.NamespacesFromSame),
			} {
				if err := cfg.Client().Resources().Create(ctx, gw); err != nil {
					t.Fatalf("creating Gateway %s/%s: %v", gw.Namespace, gw.Name, err)
				}
			}
			waitGatewayProgrammed(t, ctx, cfg, gwNS, gwName, gatewayReadyDeadline)
			gwSvc = waitProvisionedServiceName(t, ctx, cfg, gwNS, gwName, gatewayReadyDeadline)
			waitDeploymentAvailable(t, cfg, gwNS, gwSvc)

			api = newShepherdAPI(t, cfg, f.ns, release)
			api.login(t, "admin", password)
			org, err := api.admin.CreateOrg(ctx, connect.NewRequest(&mgmtv1.CreateOrgRequest{
				Name: "route-apply", DisplayName: "Route apply", AdminGroupId: "route-apply-admins", TenantId: tenant,
			}))
			if err != nil {
				t.Fatalf("CreateOrg: %v", err)
			}
			orgID = org.Msg.GetId()
			return ctx
		}).
		Assess("a route created through the API is applied, attached, and carries the org's tenant to the backend",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				r, err := api.routes.CreateTenantRoute(ctx, connect.NewRequest(&mgmtv1.CreateTenantRouteRequest{
					OrgId: orgID, Kind: "otlp", GatewayMode: "operator", GatewayName: gwName, GatewayNamespace: gwNS,
				}))
				if err != nil {
					t.Fatalf("CreateTenantRoute: %v", err)
				}
				first = r.Msg
				if first.GetApplyStatus() != routeapply.StatusPending {
					t.Fatalf("a new route reads %q, want pending — Create must only write the row", first.GetApplyStatus())
				}
				got := api.waitApplyStatus(ctx, t, orgID, first.GetId(), routeapply.StatusApplied)
				if got.GetAppliedAt() == nil {
					t.Fatal("applied without an applied_at")
				}

				var hr gatewayv1.HTTPRoute
				if err := cfg.Client().Resources().Get(ctx, routeapply.ObjectName(first.GetId()), f.ns, &hr); err != nil {
					t.Fatalf("the applied route's HTTPRoute is not in %s: %v", f.ns, err)
				}
				if hr.Labels[routeapply.RouteIDLabel] != first.GetId() {
					t.Fatalf("HTTPRoute labels %v do not carry the route id", hr.Labels)
				}

				postSpanUntil(t, cfg, f.ns, gwNS, gwSvc, "/otlp/"+first.GetSegment()+"/v1/traces", "rta-first", nil)
				waitSinkTenants(t, cfg, f.ns, sinkName, []string{tenant}, gatewayProbeDeadline)
				return ctx
			}).
		Assess("rotation keeps both segments routing through the overlap",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				r, err := api.routes.RotateTenantRoute(ctx, connect.NewRequest(&mgmtv1.RotateTenantRouteRequest{
					OrgId: orgID, Id: first.GetId(), OverlapSeconds: 3600,
				}))
				if err != nil {
					t.Fatalf("RotateTenantRoute: %v", err)
				}
				second = r.Msg.GetActive()
				api.waitApplyStatus(ctx, t, orgID, second.GetId(), routeapply.StatusApplied)
				for _, seg := range []string{first.GetSegment(), second.GetSegment()} {
					before := sinkTenantCounts(t, cfg, f.ns, sinkName)[tenant]
					postSpanUntil(t, cfg, f.ns, gwNS, gwSvc, "/otlp/"+seg+"/v1/traces", "rta-rot-"+seg, nil)
					waitSinkTenantAbove(t, cfg, f.ns, sinkName, tenant, before, gatewayProbeDeadline)
				}
				return ctx
			}).
		Assess("revoking removes the HTTPRoute and the path stops routing",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				if _, err := api.routes.RevokeTenantRoute(ctx, connect.NewRequest(&mgmtv1.RevokeTenantRouteRequest{
					OrgId: orgID, Id: first.GetId(),
				})); err != nil {
					t.Fatalf("RevokeTenantRoute: %v", err)
				}
				api.waitApplyStatus(ctx, t, orgID, first.GetId(), routeapply.StatusRemoved)
				var hr gatewayv1.HTTPRoute
				if err := cfg.Client().Resources().Get(ctx, routeapply.ObjectName(first.GetId()), f.ns, &hr); err == nil {
					t.Fatal("the revoked route is marked removed but its HTTPRoute is still in the cluster")
				}
				// nginx takes a moment to drop the route; then the old path 404s
				// while the new one still routes.
				url := fmt.Sprintf("http://%s.%s.svc.cluster.local/otlp/%s/v1/traces", gwSvc, gwNS, first.GetSegment())
				end := time.Now().Add(gatewayProbeDeadline)
				for i := 0; ; i++ {
					if code := postSpanOnce(cfg, f.ns, fmt.Sprintf("rta-revoked-%d", i), url, nil); code == "404" {
						break
					} else if time.Now().After(end) {
						t.Fatalf("the revoked segment still answers HTTP %s through the gateway after %s", code, gatewayProbeDeadline)
					}
					time.Sleep(3 * time.Second)
				}
				postSpanUntil(t, cfg, f.ns, gwNS, gwSvc, "/otlp/"+second.GetSegment()+"/v1/traces", "rta-still", nil)
				return ctx
			}).
		Assess("a Gateway that does not admit Shepherd's namespace shows the route as refused, with the reason",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// The org's otlp route is already taken; a faro route would be
				// not_applicable. A second org gets the refused one.
				org, err := api.admin.CreateOrg(ctx, connect.NewRequest(&mgmtv1.CreateOrgRequest{
					Name: "route-apply-closed", DisplayName: "Closed", AdminGroupId: "closed-admins", TenantId: "umbrella",
				}))
				if err != nil {
					t.Fatalf("CreateOrg: %v", err)
				}
				r, err := api.routes.CreateTenantRoute(ctx, connect.NewRequest(&mgmtv1.CreateTenantRouteRequest{
					OrgId: org.Msg.GetId(), Kind: "otlp", GatewayMode: "operator", GatewayName: closedGW, GatewayNamespace: gwNS,
				}))
				if err != nil {
					t.Fatalf("CreateTenantRoute: %v", err)
				}
				got := api.waitApplyStatus(ctx, t, org.Msg.GetId(), r.Msg.GetId(), routeapply.StatusRefused)
				if !strings.Contains(got.GetApplyMessage(), "NotAllowedByListeners") {
					t.Fatalf("refused, but the message does not carry the gateway's reason: %q", got.GetApplyMessage())
				}
				return ctx
			}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if api != nil {
				api.close()
			}
			f.cleanup(cfg)
			if !keepCluster() {
				_ = utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s delete namespace %s --ignore-not-found --wait=false",
					cfg.KubeconfigFile(), gwNS))
			}
			return ctx
		}).
		Feature()

	testenv.Test(t, feat)
}

// shepherdAPI is a signed-in Connect client for a chart-installed Shepherd,
// reached through `kubectl port-forward` from the test process.
type shepherdAPI struct {
	cfg       *envconf.Config
	ns        string
	svc       string
	base      string
	pf        *exec.Cmd
	mu        sync.Mutex
	cookie    string
	admin     mgmtv1connect.AdminServiceClient
	routes    mgmtv1connect.TenantRouteServiceClient
	fleet     mgmtv1connect.FleetServiceClient
	pipelines mgmtv1connect.PipelineServiceClient
}

var forwardingRE = regexp.MustCompile(`Forwarding from 127\.0\.0\.1:(\d+)`)

func newShepherdAPI(t *testing.T, cfg *envconf.Config, ns, svc string) *shepherdAPI {
	t.Helper()
	pf := exec.Command("kubectl", "--kubeconfig", cfg.KubeconfigFile(), "-n", ns,
		"port-forward", "svc/"+svc, ":8080")
	stdout, err := pf.StdoutPipe()
	if err != nil {
		t.Fatalf("port-forward stdout: %v", err)
	}
	pf.Stderr = io.Discard
	if err := pf.Start(); err != nil {
		t.Fatalf("starting port-forward: %v", err)
	}
	port := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if m := forwardingRE.FindStringSubmatch(sc.Text()); m != nil {
				port <- m[1]
			}
		}
	}()
	a := &shepherdAPI{cfg: cfg, ns: ns, svc: svc, pf: pf}
	select {
	case p := <-port:
		a.base = "http://127.0.0.1:" + p
	case <-time.After(30 * time.Second):
		_ = pf.Process.Kill() //nolint:errcheck // best effort; the test is failing anyway
		t.Fatal("kubectl port-forward never reported a local port")
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: a}
	a.admin = mgmtv1connect.NewAdminServiceClient(client, a.base)
	a.routes = mgmtv1connect.NewTenantRouteServiceClient(client, a.base)
	a.fleet = mgmtv1connect.NewFleetServiceClient(client, a.base)
	a.pipelines = mgmtv1connect.NewPipelineServiceClient(client, a.base)
	return a
}

// RoundTrip adds the session cookie and the CSRF header Shepherd requires on
// every state-changing request. The cookie is carried by hand: it is Secure
// by default, which a cookie jar would never send over the plain-HTTP
// port-forward.
func (a *shepherdAPI) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	a.mu.Lock()
	if a.cookie != "" {
		req.Header.Set("Cookie", a.cookie)
	}
	a.mu.Unlock()
	return http.DefaultTransport.RoundTrip(req)
}

func (a *shepherdAPI) login(t *testing.T, user, password string) {
	t.Helper()
	body := fmt.Sprintf(`{"username":%q,"password":%q}`, user, password)
	req, err := http.NewRequest(http.MethodPost, a.base+"/api/auth/local/login", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second, Transport: a}).Do(req)
	if err != nil {
		t.Fatalf("signing in: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body) //nolint:errcheck // diagnostics only
		t.Fatalf("signing in: HTTP %d: %s", resp.StatusCode, b)
	}
	var parts []string
	for _, c := range resp.Cookies() {
		parts = append(parts, c.Name+"="+c.Value)
	}
	if len(parts) == 0 {
		t.Fatal("signing in set no cookie")
	}
	a.mu.Lock()
	a.cookie = strings.Join(parts, "; ")
	a.mu.Unlock()
}

// waitApplyStatus polls ListTenantRoutes until route id reads want. Any other
// terminal-looking outcome is reported with its message and Shepherd's logs.
func (a *shepherdAPI) waitApplyStatus(ctx context.Context, t *testing.T, orgID, id, want string) *mgmtv1.TenantRoute {
	t.Helper()
	end := time.Now().Add(3 * time.Minute)
	var last *mgmtv1.TenantRoute
	for time.Now().Before(end) {
		resp, err := a.routes.ListTenantRoutes(ctx, connect.NewRequest(&mgmtv1.ListTenantRoutesRequest{OrgId: orgID}))
		if err == nil {
			for _, r := range resp.Msg.GetItems() {
				if r.GetId() == id {
					last = r
				}
			}
			if last != nil && last.GetApplyStatus() == want {
				return last
			}
		}
		time.Sleep(3 * time.Second)
	}
	status, msg := "(not listed)", ""
	if last != nil {
		status, msg = last.GetApplyStatus(), last.GetApplyMessage()
	}
	t.Fatalf("route %s never reached apply_status %q within 3m: last %q %q\n--- shepherd logs ---\n%s", id, want, status, msg,
		kubectlArgv(a.cfg, "-n", a.ns, "logs", "deploy/"+a.svc, "--tail=60"))
	return nil
}

func (a *shepherdAPI) close() {
	if a.pf != nil && a.pf.Process != nil {
		_ = a.pf.Process.Kill() //nolint:errcheck // best-effort teardown
		_ = a.pf.Wait()         //nolint:errcheck // exits non-zero after Kill by design
	}
}
