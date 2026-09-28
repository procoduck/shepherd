//go:build e2ek8s

package k8s_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/utils"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"shepherd/internal/gateway"
)

// TestReceiverPassThroughTenancy is review gate R3's end-to-end proof for the
// receiver tier (#109, docs/plans/2026-09-28-receiver-tier.md PR 3): the
// chart's own receiver — a REAL Alloy, rendered at pod start by `shepherd
// receiver render` — behind a real NGF gateway, with routes applied by the
// product's own gateway.ApplyRoute, forwarding to a sink that records the
// tenant header each request carried.
//
// A real Alloy is the point. D10's batch-processor defect (a missing
// metadata_keys drops the tenant header between listener and exporter, and
// the exporter then OMITS the header rather than erroring) passed alloy
// validate, the receiver's string tests, and the old kind suite, whose backend
// was an echo server rather than an Alloy pipeline (gateway plan §10,
// 2026-08-22). Only data actually flowing through a running receiver shows it.
//
// Red run, executed (docs/proofs/receiver-tier.md §1): removing the batch
// processor's `metadata_keys` line from the rendered config, the same two
// POSTs got HTTP 200 and Alloy logged no error, but the backend received ONE
// request with no X-Scope-OrgID — both tenants merged. The first assessment
// below fails on exactly that.
func TestReceiverPassThroughTenancy(t *testing.T) {
	var (
		f        *fixture
		gwSvc    string
		receiver string
	)
	const (
		release   = "shepherd-recv"
		gwNS      = "shepherd-receiver-gw"
		gwName    = "receiver-gw"
		sinkName  = "recv-sink"
		sinkPort  = 8080
		recvPort  = 4318
		tenantA   = "acme"
		tenantB   = "globex"
		segA      = "acme-recv1"
		segB      = "globex-recv1"
		forgedOrg = "evil-client-tenant"
	)
	receiver = release + "-receiver"

	feat := features.New("receiver tier: pass-through tenancy through a real gateway and a real Alloy").
		WithLabel("suite", "receiver").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			requireCNIVerified(t)
			if err := gatewayv1.Install(cfg.Client().Resources().GetScheme()); err != nil {
				t.Fatalf("registering Gateway API types with the client scheme: %v", err)
			}
			f = newFixture(ctx, t, cfg, "receiver")

			// The gateway lives in its own namespace, as an operator-run one
			// would (D8); its data-plane pods are the receiver's only ingress.
			_ = utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s delete namespace %s --ignore-not-found --wait --timeout=2m",
				cfg.KubeconfigFile(), gwNS))
			if err := cfg.Client().Resources().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: gwNS}}); err != nil {
				t.Fatalf("creating gateway namespace %s: %v", gwNS, err)
			}

			// The sink stands in for Mimir/Tempo: it records every request's
			// headers so the test can read back the tenant each one carried.
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

			// The chart's receiver, pass-through, forwarding traces to the sink.
			values := fmt.Sprintf(`
receiver:
  enabled: true
  mode: pass_through
  batch: {timeout: "1s", sendBatchSize: 8192, sendBatchMaxSize: 0}
  exporters:
    traces: {enabled: true, protocol: http, endpoint: "http://%[1]s.%[2]s.svc.cluster.local:%[3]d"}
  networkPolicy:
    gatewayFrom:
      - namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: %[4]s}}
    egress:
      - to:
          - namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: %[2]s}}
            podSelector: {matchLabels: {app: %[1]s}}
        ports: [{port: %[3]d, protocol: TCP}]
`, sinkName, f.ns, sinkPort, gwNS)
			valuesFile := filepath.Join(t.TempDir(), "receiver-values.yaml")
			if err := os.WriteFile(valuesFile, []byte(values), 0o600); err != nil {
				t.Fatalf("writing receiver values: %v", err)
			}
			helmRun(t, cfg, f, "install", release, "--set replicas=1", "-f "+valuesFile)
			waitDeploymentAvailable(t, cfg, f.ns, receiver)

			gw := operatorGateway(gwNS, gwName, gatewayv1.NamespacesFromAll)
			if err := cfg.Client().Resources().Create(ctx, gw); err != nil {
				t.Fatalf("creating Gateway %s/%s: %v", gwNS, gwName, err)
			}
			waitGatewayProgrammed(t, ctx, cfg, gwNS, gwName, gatewayReadyDeadline)
			gwSvc = waitProvisionedServiceName(t, ctx, cfg, gwNS, gwName, gatewayReadyDeadline)
			waitDeploymentAvailable(t, cfg, gwNS, gwSvc)

			// One route per tenant, rendered and applied by the product itself.
			for _, r := range []struct{ tenant, seg string }{{tenantA, segA}, {tenantB, segB}} {
				spec := gateway.RouteSpec{
					Name: "recv-" + r.tenant, Namespace: f.ns, TenantID: r.tenant, Kind: gateway.KindOTLP,
					RouteSegment: r.seg, GatewayName: gwName, GatewayNamespace: gwNS,
					BackendName: receiver, BackendPort: recvPort,
				}
				if _, err := gateway.ApplyRoute(ctx, cfgApplier{cfg: cfg}, spec, gateway.ApplyOptions{
					PollInterval: 3 * time.Second, Deadline: gatewayReadyDeadline,
				}); err != nil {
					t.Fatalf("ApplyRoute for tenant %s: %v", r.tenant, err)
				}
			}
			return ctx
		}).
		Assess("the gateway's namespace can reach the receiver directly (the NetworkPolicy's allowed side)",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// The positive half of the ingress rule, and the first thing to
				// know if the gateway path below fails: can traffic from gwNS
				// reach the receiver at all?
				direct := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d/v1/traces", receiver, f.ns, recvPort)
				end := time.Now().Add(gatewayProbeDeadline)
				var code string
				for i := 0; ; i++ {
					// A tenant header of its own: this request bypasses the
					// route, and pass-through forwards whatever it carries —
					// without one it would reach the backend untagged and trip
					// the next assessment's "nothing untagged" check.
					if code = postSpanOnce(cfg, gwNS, fmt.Sprintf("from-gw-ns-%d", i), direct, directProbeTenant); code == "200" {
						return ctx
					}
					if time.Now().After(end) {
						t.Fatalf("a pod in the gateway namespace %s could not reach the receiver directly (last HTTP %q) — "+
							"the NetworkPolicy's gatewayFrom does not admit it\n%s", gwNS, code, receiverDiagnostics(cfg, f.ns, gwNS, gwSvc))
					}
					time.Sleep(3 * time.Second)
				}
			}).
		Assess("each tenant's spans reach the backend carrying exactly that tenant, through the real receiver",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				for _, r := range []struct{ tenant, seg string }{{tenantA, segA}, {tenantB, segB}} {
					postSpanUntil(t, cfg, f.ns, gwNS, gwSvc, "/otlp/"+r.seg+"/v1/traces", "span-"+r.tenant, nil)
				}
				seen := waitSinkTenants(t, cfg, f.ns, sinkName, []string{tenantA, tenantB}, gatewayProbeDeadline)
				// Nothing reached the backend without a tenant: the D10 failure
				// mode is exactly an exporter that silently omits the header.
				if n := seen[""]; n > 0 {
					t.Fatalf("%d request(s) reached the backend with NO X-Scope-OrgID — the receiver dropped the "+
						"tenant between listener and exporter (D10). Seen: %v", n, seen)
				}
				t.Logf("backend saw, by tenant: %v", seen)
				return ctx
			}).
		Assess("a tenant header the client sets itself never reaches the backend",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				postSpanUntil(t, cfg, f.ns, gwNS, gwSvc, "/otlp/"+segA+"/v1/traces", "span-forged",
					map[string]string{gateway.TenantHeader: forgedOrg})
				// Wait until the forged request's batch has certainly flushed:
				// acme's count must rise past what it was before.
				before := sinkTenantCounts(t, cfg, f.ns, sinkName)[tenantA]
				postSpanUntil(t, cfg, f.ns, gwNS, gwSvc, "/otlp/"+segA+"/v1/traces", "span-after-forged", nil)
				waitSinkTenantAbove(t, cfg, f.ns, sinkName, tenantA, before, gatewayProbeDeadline)
				if n := sinkTenantCounts(t, cfg, f.ns, sinkName)[forgedOrg]; n > 0 {
					t.Fatalf("the client-asserted tenant %q reached the backend %d time(s) — the route must "+
						"replace the header, not pass the client's through", forgedOrg, n)
				}
				return ctx
			}).
		Assess("the gateway is the only ingress: a pod elsewhere cannot reach the receiver, until the NetworkPolicy is removed",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				direct := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d/v1/traces", receiver, f.ns, recvPort)
				// Several attempts, all refused: one timeout alone could be a
				// transient network hiccup that proves nothing.
				for i := 0; i < 3; i++ {
					if code := postSpanOnce(cfg, f.ns, fmt.Sprintf("direct-denied-%d", i), direct, directProbeTenant); code != "000" {
						t.Fatalf("a pod outside the gateway namespace reached the receiver directly (HTTP %s) — "+
							"it could assert any tenant; the NetworkPolicy does not confine ingress to the gateway", code)
					}
				}
				// The control: the SAME request succeeds once the policy is gone,
				// so the refusal above was the policy and not a broken probe.
				p := utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s -n %s delete networkpolicy %s --wait",
					cfg.KubeconfigFile(), f.ns, receiver))
				if p.Err() != nil {
					t.Fatalf("deleting the receiver NetworkPolicy for the control: %v\n%s", p.Err(), p.Result())
				}
				end := time.Now().Add(gatewayProbeDeadline)
				for i := 0; ; i++ {
					if code := postSpanOnce(cfg, f.ns, fmt.Sprintf("direct-open-%d", i), direct, directProbeTenant); code == "200" {
						t.Logf("control: without the NetworkPolicy the same direct request succeeds (HTTP 200)")
						break
					}
					if time.Now().After(end) {
						t.Fatalf("control failed: the direct request never succeeded even without the NetworkPolicy, " +
							"so the refusal above does not prove the policy works")
					}
					time.Sleep(3 * time.Second)
				}
				// Put the policy back for the rest of the feature.
				helmRun(t, cfg, f, "upgrade", release, "--reuse-values")
				return ctx
			}).
		Assess("a receiver config the renderer refuses stops the pod at init instead of starting Alloy",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// A batch cap below the batch size: receiver.Validate refuses it
				// (Alloy itself would refuse to start). No --wait: the new pod is
				// meant never to become ready.
				p := utils.RunCommand(fmt.Sprintf(
					"helm upgrade %s %s --kubeconfig %s --namespace %s %s --reuse-values --set receiver.batch.sendBatchMaxSize=100",
					release, chartPath, cfg.KubeconfigFile(), f.ns, strings.Join(chartImageArgs(), " ")))
				if p.Err() != nil {
					t.Fatalf("helm upgrade with the refused config failed to apply: %v\n%s", p.Err(), p.Result())
				}
				end := time.Now().Add(2 * time.Minute)
				for {
					out := utils.RunCommand(fmt.Sprintf(
						"kubectl --kubeconfig %s -n %s get pods -l app.kubernetes.io/component=receiver "+
							`-o jsonpath={range .items[*]}{.status.initContainerStatuses[0].state.terminated.exitCode}{"|"}{.status.initContainerStatuses[0].lastState.terminated.exitCode}{"\n"}{end}`,
						cfg.KubeconfigFile(), f.ns)).Result()
					if strings.Contains(out, "1|") || strings.Contains(out, "|1") {
						logs := utils.RunCommand(fmt.Sprintf(
							"kubectl --kubeconfig %s -n %s logs -l app.kubernetes.io/component=receiver -c render --tail=20",
							cfg.KubeconfigFile(), f.ns)).Result()
						if !strings.Contains(logs, "send_batch_max_size") {
							t.Fatalf("the render init container failed, but not for the refused setting:\n%s", logs)
						}
						t.Logf("render refused the config at init, as intended:\n%s", logs)
						return ctx
					}
					if time.Now().After(end) {
						t.Fatalf("no receiver pod's render init container failed within 2m; statuses:\n%s\n%s",
							out, describeNS(cfg, f.ns))
					}
					time.Sleep(3 * time.Second)
				}
			}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
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

// directProbeTenant tags the test's own requests that go straight to the
// receiver, bypassing the gateway: they must not read as a dropped tenant.
var directProbeTenant = map[string]string{gateway.TenantHeader: "direct-probe"}

// receiverSinkPod is the backend the receiver exports to. mendhak/http-https-
// echo logs every request — path and headers — which is how the test reads
// back the tenant each forwarded request carried. LOG_WITHOUT_NEWLINE puts
// each request on one log line; ECHO_BACK_TO_CLIENT=false answers with an
// empty 200, so Alloy's OTLP exporter sees a plain success.
func receiverSinkPod(ns, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"app": name}},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "sink",
				Image: "mendhak/http-https-echo:32",
				Ports: []corev1.ContainerPort{{ContainerPort: 8080}},
				Env: []corev1.EnvVar{
					{Name: "LOG_WITHOUT_NEWLINE", Value: "true"},
					{Name: "ECHO_BACK_TO_CLIENT", Value: "false"},
				},
			}},
		},
	}
}

func receiverSinkService(ns, name string, port int32) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			Ports:    []corev1.ServicePort{{Port: port, TargetPort: intstr.FromInt32(port), Protocol: corev1.ProtocolTCP}},
		},
	}
}

// otlpSpanJSON is one span as an OTLP/HTTP JSON request body, tagged by its
// name so a failure can say which request went astray.
func otlpSpanJSON(name string) string {
	return fmt.Sprintf(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"receiver-e2e"}}]},`+
		`"scopeSpans":[{"spans":[{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"eee19b7ec3c1b174",`+
		`"name":%q,"kind":1,"startTimeUnixNano":"1700000000000000000","endTimeUnixNano":"1700000001000000000"}]}]}]}`, name)
}

// postSpanOnce POSTs one OTLP/JSON span from a one-shot pod in podNS and
// returns the HTTP status curl reported ("000" when the connection failed).
//
// kubectl runs from an explicit argv (os/exec), NOT utils.RunCommand: gexe,
// underneath it, re-tokenises the command string with its own parser, which
// mangled both the JSON body and the `sh -c` script — the probe failed for a
// reason that had nothing to do with the network it was meant to test.
func postSpanOnce(cfg *envconf.Config, podNS, name, url string, headers map[string]string) string {
	var hdr strings.Builder
	for k, v := range headers {
		fmt.Fprintf(&hdr, " -H %s", shellQuote(k+": "+v))
	}
	// `; true`: only the HTTP code (000 when the connection failed) is wanted,
	// not curl's own exit status (28 on a timeout).
	script := fmt.Sprintf("curl -s -o /dev/null -w %%{http_code} --max-time 10 -X POST -H %s%s --data %s %s; true",
		shellQuote("Content-Type: application/json"), hdr.String(), shellQuote(otlpSpanJSON(name)), shellQuote(url))
	cmd := exec.Command("kubectl", "--kubeconfig", cfg.KubeconfigFile(), "-n", podNS,
		"run", name, "--image=curlimages/curl:8.11.1", "--restart=Never", "--rm", "--attach",
		"--quiet", "--pod-running-timeout=2m", "--command", "--", "sh", "-c", script)
	// Judged by the printed status, not kubectl's exit code: a pod that
	// could not even start prints no status, which reads as a failure too.
	out, _ := cmd.CombinedOutput() //nolint:errcheck // see above
	return lastLine(string(out))
}

// postSpanUntil POSTs a span through the gateway until the receiver accepts it
// (HTTP 200): nginx needs a moment to program a new route.
func postSpanUntil(t *testing.T, cfg *envconf.Config, podNS, gwNS, gwSvc, path, span string, headers map[string]string) {
	t.Helper()
	url := fmt.Sprintf("http://%s.%s.svc.cluster.local%s", gwSvc, gwNS, path)
	end := time.Now().Add(gatewayProbeDeadline)
	var code string
	for i := 0; time.Now().Before(end); i++ {
		if code = postSpanOnce(cfg, podNS, fmt.Sprintf("%s-%d", span, i), url, headers); code == "200" {
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("POST %s through the gateway never got HTTP 200 from the receiver within %s (last: %q)\n%s",
		path, gatewayProbeDeadline, code, receiverDiagnostics(cfg, podNS, gwNS, gwSvc))
}

// sinkTenantCounts reads the sink's request log and counts OTLP trace
// exports by the X-Scope-OrgID they carried; "" counts exports with none.
func sinkTenantCounts(t *testing.T, cfg *envconf.Config, ns, sink string) map[string]int {
	t.Helper()
	p := utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s -n %s logs %s", cfg.KubeconfigFile(), ns, sink))
	counts := map[string]int{}
	for _, line := range strings.Split(p.Result(), "\n") {
		start := strings.Index(line, "{")
		if start < 0 {
			continue
		}
		var req struct {
			Path    string            `json:"path"`
			Headers map[string]string `json:"headers"`
		}
		if json.Unmarshal([]byte(line[start:]), &req) != nil || req.Path != "/v1/traces" {
			continue
		}
		counts[req.Headers["x-scope-orgid"]]++
	}
	return counts
}

// waitSinkTenants waits until the sink has seen at least one export for every
// tenant in want, and returns the final counts.
func waitSinkTenants(t *testing.T, cfg *envconf.Config, ns, sink string, want []string, deadline time.Duration) map[string]int {
	t.Helper()
	end := time.Now().Add(deadline)
	for {
		counts := sinkTenantCounts(t, cfg, ns, sink)
		missing := []string{}
		for _, w := range want {
			if counts[w] == 0 {
				missing = append(missing, w)
			}
		}
		if len(missing) == 0 {
			return counts
		}
		if time.Now().After(end) {
			t.Fatalf("the backend never saw an export for tenant(s) %v within %s; saw %v\n%s",
				missing, deadline, counts, describeNS(cfg, ns))
		}
		time.Sleep(3 * time.Second)
	}
}

func waitSinkTenantAbove(t *testing.T, cfg *envconf.Config, ns, sink, tenant string, above int, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	for sinkTenantCounts(t, cfg, ns, sink)[tenant] <= above {
		if time.Now().After(end) {
			t.Fatalf("tenant %s's export count never rose above %d within %s", tenant, above, deadline)
		}
		time.Sleep(3 * time.Second)
	}
}

// shellQuote single-quotes s for sh, which the curl command line runs under.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// lastLine is the final non-empty line of kubectl run's output — the curl
// status code — past any kubectl notices printed before it.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// receiverDiagnostics gathers what a failed probe needs to be understood:
// the receiver namespace, the gateway data plane's own logs (did nginx get
// the request, and what did the upstream do?), and the receiver's logs.
func receiverDiagnostics(cfg *envconf.Config, ns, gwNS, gwDeploy string) string {
	run := func(cmd string) string {
		return utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s %s", cfg.KubeconfigFile(), cmd)).Result()
	}
	return describeNS(cfg, ns) +
		"\n--- gateway data plane (" + gwNS + "/" + gwDeploy + ") logs ---\n" +
		run(fmt.Sprintf("-n %s logs deploy/%s --all-containers --tail=40", gwNS, gwDeploy)) +
		"\n--- pods in " + gwNS + " ---\n" + run(fmt.Sprintf("-n %s get pods -o wide --show-labels", gwNS)) +
		"\n--- receiver logs ---\n" +
		run(fmt.Sprintf("-n %s logs -l app.kubernetes.io/component=receiver -c alloy --tail=40", ns)) +
		"\n--- receiver NetworkPolicy ---\n" + run(fmt.Sprintf("-n %s get networkpolicy -l app.kubernetes.io/component=receiver -o yaml", ns))
}
