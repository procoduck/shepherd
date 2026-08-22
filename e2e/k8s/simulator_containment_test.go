//go:build e2ek8s

package k8s_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/utils"
)

// TestSimulatorContainmentProbes is Layer B of docs/kind-test-environment-plan.md
// §5 — the reason the Kubernetes suite exists. deploy/helm/chart_test.go greps
// `helm template` output and asserts the NetworkPolicy YAML renders; nothing
// has ever dialled OUT of a real simulator Pod to see whether that policy does
// anything. This does, from inside the Pod itself.
//
// Seven probes, each dialled from an ephemeral debug container attached to the
// simulator Pod (kubectl debug — nothing added to the shipped distroless
// image, which has no shell for `kubectl exec` to use anyway). An ephemeral
// container shares its Pod's network namespace unconditionally, so "inside the
// pod" here means the real thing, not a same-namespace neighbour:
//
//	P-harness    reachable        the sandbox can still reach its own capture
//	                               receivers — containment did not become
//	                               deny-everything. THE VACUITY GUARD: every
//	                               other row below is a denial, and a suite of
//	                               only-denials passes perfectly on a pod with
//	                               no working network at all. Dialled at the
//	                               Pod's own IP, which Kubernetes guarantees is
//	                               always reachable regardless of policy ("HTTP
//	                               request from a Pod to itself is not
//	                               subject to NetworkPolicy") — so this proves
//	                               the harness process and the dial mechanism
//	                               both work, independent of what the policy
//	                               says, exactly what a vacuity guard needs.
//	P-shepherd   denied           closes B-CONTAIN-1 for Kubernetes: the
//	                               sandbox cannot reach Shepherd's own Service.
//	P-incluster  denied           no lateral movement to an arbitrary other
//	                               in-cluster Service (the suite's own shared
//	                               Postgres, in a different namespace).
//	P-node       denied           the node the Pod is scheduled on is not
//	                               reachable — the Kubernetes answer to
//	                               B-CONTAIN-2.
//	P-external   denied           no egress to the internet. Dialled by IP
//	                               literal, mirroring e2e/sandbox_egress_test.go's
//	                               P-deny-ip, so DNS-openness cannot confuse the
//	                               result.
//	P-dns-only   resolves, denied DNS is open by design (the harness ports
//	                               above are ultimately still reached via the
//	                               Pod's own IP, but discovery.relabel-style
//	                               user config could still ask to resolve
//	                               anything); resolving a name must not imply
//	                               reaching what it resolves to.
//	P-apiserver  denied           the sandbox cannot reach the API server
//	                               either, and has no ServiceAccount token
//	                               mounted to use if it somehow could.
//
// Runs only after TestCNIEnforcesNetworkPolicy (see cniVerified in
// negative_control_test.go) — a denial observed against a CNI that does not
// enforce NetworkPolicy would mean nothing, which is the exact trap this suite
// already refuses to fall into once at the cluster level; this feature refuses
// to fall into it a second time at its own level.
func TestSimulatorContainmentProbes(t *testing.T) {
	var (
		f      *fixture
		pod    string
		podIP  string
		nodeIP string
	)
	const release = "shepherd"

	feat := features.New("simulator containment probes").
		WithLabel("suite", "containment").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// Any denial reported by this feature would be meaningless if the
			// CNI does not enforce NetworkPolicy at all. requireCNIVerified
			// runs the negative control on demand rather than trusting
			// filename-sort ordering.
			requireCNIVerified(t)

			f = newFixture(ctx, t, cfg, "containment")
			helmRun(t, cfg, f, "install", release, simulatorOn()...)
			waitDeploymentAvailable(t, cfg, f.ns, release+"-simulator")

			pod, podIP = simulatorPodNameAndIP(t, ctx, cfg, f.ns, release)
			nodeIP = nodeInternalIP(t, ctx, cfg, nodeNameOfPod(t, ctx, cfg, f.ns, pod))
			return ctx
		}).
		Assess("P-harness: the sandbox CAN reach its own capture receiver",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// If this fails, nothing below means anything: it is the proof
				// that the debug container, the network path out of the pod's
				// own namespace and the harness process itself all work. A pod
				// with no working network at all would otherwise pass every
				// denial below for free.
				ok, _, out := probeFromPodUntil(cfg, f.ns, pod, "harness", podIP, harnessCapturePort, true, probeMustSucceedDeadline)
				if !ok {
					t.Fatalf("the simulator could not reach its OWN capture receiver (%s:%d) within %s — "+
						"every denial probe in this feature would be indistinguishable from a pod with no "+
						"working network at all, so none of them can be trusted until this is fixed.\n"+
						"last probe output: %s", podIP, harnessCapturePort, probeMustSucceedDeadline, out)
				}
				return ctx
			}).
		Assess("P-shepherd: the sandbox canNOT reach Shepherd's own Service",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				target := shepherdServiceFQDN(f.ns, release)
				denied, connected, out := probeFromPodUntil(cfg, f.ns, pod, "shepherd", target, shepherdServicePort, false, probeMustDenyDeadline)
				requireDenied(t, denied, connected, out, fmt.Sprintf(
					"B-CONTAIN-1 IS OPEN ON THIS CLUSTER: the sandbox reached Shepherd's own "+
						"Service (%s:%d). A run inside this simulator executes user-authored Alloy config; "+
						"if it can scrape Shepherd's control plane, that data returns to the user in run "+
						"results. probe output:\n%s", target, shepherdServicePort, out))
				return ctx
			}).
		Assess("P-incluster: the sandbox canNOT reach an arbitrary other in-cluster Service",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// The suite's own shared Postgres (main_test.go), in a
				// different namespace — real, already running, and exactly
				// the kind of lateral-movement target a malicious pipeline
				// would go looking for. No throwaway fixture pod needed.
				target := fmt.Sprintf("%s.%s.svc.cluster.local", pgName, infraNamespace)
				denied, connected, out := probeFromPodUntil(cfg, f.ns, pod, "incluster", target, 5432, false, probeMustDenyDeadline)
				requireDenied(t, denied, connected, out, fmt.Sprintf(
					"the sandbox reached an unrelated in-cluster Service (%s:5432) that has nothing "+
						"to do with the simulator — lateral movement to any Service the cluster happens to "+
						"run is possible. probe output:\n%s", target, out))
				return ctx
			}).
		Assess("P-node: the sandbox canNOT reach the node it runs on",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// Port 10250 is the kubelet API — virtually always listening on
				// a Kubernetes node, so a false "denied" cannot be mistaken for
				// "nothing was listening there anyway".
				denied, connected, out := probeFromPodUntil(cfg, f.ns, pod, "node", nodeIP, 10250, false, probeMustDenyDeadline)
				requireDenied(t, denied, connected, out, fmt.Sprintf(
					"the sandbox reached its own node's kubelet API (%s:10250) — this is the "+
						"Kubernetes form of B-CONTAIN-2, and unlike the Docker-bridge version this one is "+
						"not supposed to be a known limitation. probe output:\n%s", nodeIP, out))
				return ctx
			}).
		Assess("P-external: the sandbox canNOT egress to the internet",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// A literal IP, exactly like e2e/sandbox_egress_test.go's
				// P-deny-ip: dialling a name would route the result through
				// whether DNS resolved it, which is a different (and, per
				// P-dns-only below, deliberately open) question.
				denied, connected, out := probeFromPodUntil(cfg, f.ns, pod, "external", externalProbeIP, externalProbePort, false, probeMustDenyDeadline)
				requireDenied(t, denied, connected, out, fmt.Sprintf(
					"the sandbox reached the public internet (%s:%d) — a user-authored pipeline "+
						"could exfiltrate captured data to any host it names. probe output:\n%s",
					externalProbeIP, externalProbePort, out))
				return ctx
			}).
		Assess("P-dns-only: DNS resolves, but connecting to what it resolves is still denied",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// DNS is open by design (egress to kube-system:53) so a
				// pipeline can still name a host; that must never be read as
				// "and therefore it can reach it". Resolve a real external
				// name, then dial the address it resolved to — not a stand-in
				// literal — so this fails if resolution ever stops being open
				// as loudly as it fails if the connect denial ever breaks.
				ip, resolveOut := resolveFromPod(cfg, f.ns, pod, externalProbeName)
				if ip == "" {
					t.Fatalf("DNS resolution of %s from inside the sandbox failed or produced no parseable "+
						"address — either DNS is unexpectedly closed (a regression the opposite direction "+
						"from what this probe exists to catch) or this environment has no working upstream "+
						"DNS, which would make this probe meaningless here.\nnslookup output:\n%s",
						externalProbeName, resolveOut)
				}
				denied, connected, dialOut := probeFromPodUntil(cfg, f.ns, pod, "dns-connect", ip, externalProbePort, false, probeMustDenyDeadline)
				requireDenied(t, denied, connected, dialOut, fmt.Sprintf(
					"DNS resolved %s to %s, and the sandbox then REACHED it — DNS being open was "+
						"supposed to be harmless because the network still denies the connect. It did not "+
						"here. probe output:\n%s", externalProbeName, ip, dialOut))
				return ctx
			}).
		Assess("P-apiserver: the sandbox canNOT reach the API server, and has no token to present if it could",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				apiIP := kubernetesServiceClusterIP(t, ctx, cfg)
				denied, connected, out := probeFromPodUntil(cfg, f.ns, pod, "apiserver", apiIP, 443, false, probeMustDenyDeadline)
				requireDenied(t, denied, connected, out, fmt.Sprintf(
					"the sandbox reached the Kubernetes API server (%s:443) — a user-authored "+
						"pipeline that can talk to the control plane has a very different blast radius than "+
						"one confined to the harness. probe output:\n%s", apiIP, out))

				var p corev1.Pod
				if err := cfg.Client().Resources(f.ns).Get(ctx, pod, f.ns, &p); err != nil {
					t.Fatalf("re-reading simulator pod %s/%s to check its ServiceAccount token mount: %v", f.ns, pod, err)
				}
				if p.Spec.AutomountServiceAccountToken == nil || *p.Spec.AutomountServiceAccountToken {
					t.Fatalf("the simulator Pod's automountServiceAccountToken is not explicitly false "+
						"(got %v) — the network denial above is not the only thing standing between the "+
						"sandbox and the API server, and this is the other one", p.Spec.AutomountServiceAccountToken)
				}
				for _, v := range p.Spec.Volumes {
					if strings.HasPrefix(v.Name, "kube-api-access-") {
						t.Fatalf("the simulator Pod has a %q volume mounted despite "+
							"automountServiceAccountToken=false — a live credential is present even though "+
							"the flag says there should not be one", v.Name)
					}
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

// TestSimulatorContainmentKillProbe is the executable form of the plan's rule
// "a containment suite that stays green with the policy removed is not testing
// the policy". It installs its own release, confirms Shepherd is unreachable
// exactly like TestSimulatorContainmentProbes found, DELETES the simulator's
// NetworkPolicy, and requires that both Shepherd and external egress become
// reachable. If they do not, the denials reported above were never coming from
// this policy in the first place — a false pass this suite would otherwise have
// no way to notice.
//
// Own fixture, own namespace, run last in this file (after
// TestSimulatorContainmentProbes) so a reader sees the probes prove containment
// before this proves what is actually providing it. No restore step: the
// policy is deleted inside a throwaway namespace that Teardown destroys in its
// entirety, so there is nothing left for a restored policy to protect.
//
// Why only two of the six denials get a post-delete flip: all six ride ONE
// policy object's single default-deny egress with narrow allows — there is no
// per-target rule whose removal could flip one denial and not another. Two
// flips in independent denial classes (an in-namespace Service and an external
// IP literal) establish that THE POLICY is the causal control; repeating the
// flip for node/apiserver/incluster would re-prove the same causation while
// depending on those targets accepting connections from a policy-free pod,
// which is their operators' choice, not this chart's.
func TestSimulatorContainmentKillProbe(t *testing.T) {
	var (
		f   *fixture
		pod string
	)
	const release = "shepherd"

	feat := features.New("kill probe: containment depends on the NetworkPolicy, not on something else").
		WithLabel("suite", "containment").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// See TestSimulatorContainmentProbes for why an unverified CNI
			// makes every result here meaningless too.
			requireCNIVerified(t)

			f = newFixture(ctx, t, cfg, "killprobe")
			helmRun(t, cfg, f, "install", release, simulatorOn()...)
			waitDeploymentAvailable(t, cfg, f.ns, release+"-simulator")
			pod, _ = simulatorPodNameAndIP(t, ctx, cfg, f.ns, release)

			// The pre-condition the rest of this feature contrasts against: if
			// Shepherd were already reachable before the policy is deleted,
			// "and it's reachable after deleting the policy too" would prove
			// nothing.
			denied, connected, out := probeFromPodUntil(cfg, f.ns, pod, "kill-pre", shepherdServiceFQDN(f.ns, release), shepherdServicePort, false, probeMustDenyDeadline)
			requireDenied(t, denied, connected, out,
				"Shepherd was already reachable from the simulator BEFORE its NetworkPolicy was "+
					"deleted — the kill probe has nothing to contrast against and cannot prove anything. "+
					"probe output:\n"+out)
			return ctx
		}).
		Assess("deleting the simulator's NetworkPolicy makes Shepherd's Service reachable",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				npName := release + "-simulator"
				if p := utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s -n %s delete networkpolicy %s --wait",
					cfg.KubeconfigFile(), f.ns, npName)); p.Err() != nil {
					t.Fatalf("deleting NetworkPolicy %s/%s: %v\n%s", f.ns, npName, p.Err(), p.Result())
				}

				ok, _, out := probeFromPodUntil(cfg, f.ns, pod, "kill-post-shepherd", shepherdServiceFQDN(f.ns, release), shepherdServicePort, true, probeMustSucceedDeadline)
				if !ok {
					t.Fatalf("WITH THE NETWORKPOLICY DELETED, SHEPHERD IS STILL UNREACHABLE from the "+
						"simulator after %s. This is not the good kind of green: if "+
						"TestSimulatorContainmentProbes' P-shepherd denial holds even with this policy gone, "+
						"that denial is not coming from this policy — meaning it proves nothing, and the real "+
						"containment mechanism (whatever it is) is untested. probe output:\n%s",
						probeMustSucceedDeadline, out)
				}
				return ctx
			}).
		Assess("deleting the simulator's NetworkPolicy also opens external egress",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				ok, _, out := probeFromPodUntil(cfg, f.ns, pod, "kill-post-external", externalProbeIP, externalProbePort, true, probeMustSucceedDeadline)
				if !ok {
					t.Fatalf("WITH THE NETWORKPOLICY DELETED, EXTERNAL EGRESS IS STILL DENIED after %s — "+
						"same concern as the Shepherd case above: TestSimulatorContainmentProbes' P-external "+
						"denial would not be attributable to this policy, so it would not be testing what "+
						"this suite claims it tests. probe output:\n%s", probeMustSucceedDeadline, out)
				}
				return ctx
			}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// The whole namespace — release, policy-shaped hole and all — goes
			// with it. Nothing here needs the policy restored.
			f.cleanup(cfg)
			return ctx
		}).
		Feature()

	testenv.Test(t, feat)
}

const (
	// probeMustSucceedDeadline bounds a probe expected to connect. Generous
	// enough for the simulator pod's own network path to settle right after
	// `helm install --wait` returned Available, without being the 90s the
	// chart-install spec budgets for Service endpoint programming — these
	// probes dial a Pod IP or, post-kill-probe, a Service that is already
	// long since programmed.
	probeMustSucceedDeadline = 30 * time.Second
	// probeMustDenyDeadline bounds a probe expected to be refused or time out.
	// This is a CONVERGENCE budget, not a per-run cost: the retry loop returns
	// on the FIRST observed denial, so on a converged cluster each denial
	// costs one ~5s attempt regardless of this value. The deadline only burns
	// fully while the dial keeps SUCCEEDING — either a real containment hole
	// (worth 90s to report confidently) or Calico/Felix still programming the
	// freshly-installed policy. The latter was measured at >17s on a loaded
	// 3-node kind (the first run of this suite failed P-shepherd on exactly
	// that race: the same dial, by pod IP, Service IP and FQDN, was denied
	// minutes later), so 15s was empirically too short a window.
	probeMustDenyDeadline = 90 * time.Second
	// probeDialTimeoutSecs is nc's own -w bound, per attempt.
	probeDialTimeoutSecs = 5

	// harnessCapturePort is the simulator's own capture-receiver port
	// (deploy/helm/shepherd/templates/deployment-simulator.yaml) — one of the
	// harness endpoints the sandboxed Alloy child process is allowed to
	// reach. Proving reachability to this one is enough for P-harness; the
	// NetworkPolicy opens all four harness ports identically.
	harnessCapturePort = 9110
	// shepherdServicePort is the one port Shepherd's Service exposes
	// (deploy/helm/shepherd/templates/service.yaml) — both its API and its
	// metrics are served on it, so a single denied dial covers both.
	shepherdServicePort = 8080

	// externalProbeIP is a literal, not a name: dialling a name would leave
	// the result entangled with whether DNS resolved it, and DNS is
	// deliberately open (P-dns-only). 1.1.1.1:443 is chosen for the same
	// reason e2e/sandbox_egress_test.go dials the canary by IP for P-deny-ip —
	// a well-known, virtually-always-listening public address, so a denial
	// cannot be mistaken for "nothing was there".
	externalProbeIP   = "1.1.1.1"
	externalProbePort = 443
	// externalProbeName is resolved (not dialled directly) by P-dns-only to
	// prove DNS resolution itself is open; the address it resolves to is then
	// dialled and must still be denied.
	externalProbeName = "example.com"
)

// probeFromPod dials host:port from INSIDE podName using an ephemeral debug
// container — kubectl debug adds a throwaway container to the running Pod
// rather than kubectl exec-ing into the simulator's own container, which is
// distroless and read-only and has no nc, no shell, nothing to exec into. An
// ephemeral container always shares its Pod's network namespace, with no
// `--target` needed for that (target only shares the PID namespace), so this
// really is "from inside the simulator pod" and not a lookalike run elsewhere
// in the namespace.
//
// containerSuffix must be unique per call within one Pod: ephemeral containers
// cannot be removed once added, so a second probe reusing a name would collide
// with the first rather than replacing it.
// probeFromPod dials host:port from an ephemeral debug container in podName's
// netns and reports the outcome via OUTPUT SENTINELS, not the exit code:
// `kubectl debug --attach` does not reliably propagate the debug container's
// exit status (unlike `kubectl exec`), so judging by p.Err() made a blackholed
// nc indistinguishable from a successful one — every deny probe in this file
// reported "reached" on a cluster whose policy provably denied the same dial
// (the bug that produced the suite's first P-shepherd false alarm). stdout IS
// captured across attach (resolveFromPod parses nslookup output the same way),
// so the sentinel is the reliable channel. Returns connected=false with
// ok=false when neither sentinel came back (attach flake) — callers must
// retry, never count a missing answer as a denial.
func probeFromPod(cfg *envconf.Config, ns, podName, containerSuffix, host string, port int) (connected, ok bool, raw string) {
	name := "probe-" + containerSuffix
	cmd := fmt.Sprintf(
		"kubectl --kubeconfig %s -n %s debug pod/%s --image=busybox:1.36 -c %s --attach --quiet -- "+
			"sh -c 'if timeout %d nc -z -w %d %s %d; then echo PROBE-CONNECTED; else echo PROBE-DENIED; fi'",
		cfg.KubeconfigFile(), ns, podName, name, probeDialTimeoutSecs+2, probeDialTimeoutSecs, host, port,
	)
	p := utils.RunCommand(cmd)
	raw = strings.TrimSpace(p.Result())
	switch {
	case strings.Contains(raw, "PROBE-CONNECTED"):
		return true, true, raw
	case strings.Contains(raw, "PROBE-DENIED"):
		return false, true, raw
	default:
		return false, false, raw
	}
}

// probeFromPodUntil retries probeFromPod until the dial's observed outcome
// matches `want` (true = must connect, false = must be refused/time out) or
// the deadline expires. Mirrors dialUntilIn in negative_control_test.go for
// the same reason: a single dial can fail or pass for reasons that have
// nothing to do with what is being tested (a NetworkPolicy that has not
// finished programming yet, in particular), and each attempt needs a fresh
// container name regardless. Attempts where the probe produced no sentinel
// (attach flake) count for neither outcome.
// The second return distinguishes the two ways `met` can be false: true means
// at least one attempt produced a sentinel showing the OPPOSITE outcome (for a
// deny probe: the dial really connected — a breach); false means no attempt
// ever produced a sentinel at all (attach flake — the property is UNCONFIRMED,
// which must fail loud but must not be reported as a breach).
func probeFromPodUntil(cfg *envconf.Config, ns, podName, base, host string, port int, want bool, deadline time.Duration) (met, observedOpposite bool, last string) {
	end := time.Now().Add(deadline)
	for i := 0; time.Now().Before(end); i++ {
		connected, ok, out := probeFromPod(cfg, ns, podName, fmt.Sprintf("%s-%d", base, i), host, port)
		last = out
		if ok && connected == want {
			return true, observedOpposite, last
		}
		if ok {
			observedOpposite = true
		}
		time.Sleep(3 * time.Second)
	}
	return false, observedOpposite, last
}

// requireDenied asserts a deny-probe outcome with honest attribution: a
// sentinel-confirmed connect fails with the probe's own breach message, while
// a deadline full of sentinel-less attach flakes fails as UNCONFIRMED — loud
// either way, but never misreporting harness trouble as a containment breach.
func requireDenied(t *testing.T, denied, connected bool, out, breachMsg string) {
	t.Helper()
	if denied {
		return
	}
	if !connected {
		t.Fatalf("denial could not be CONFIRMED: no probe attempt returned a PROBE-* sentinel within "+
			"the deadline (kubectl debug attach flake?). Failing loud, but this is NOT evidence of a "+
			"breach — fix the probe path and re-run.\nlast output:\n%s", out)
	}
	t.Fatal(breachMsg)
}

// resolvedAddressRE matches busybox nslookup's plain "Address: <ip>" answer
// lines. Deliberately excludes the server line ("Address:\t<ip>:53") by
// anchoring on end-of-line immediately after a dotted-quad — the server line
// has a ":53" suffix and IPv6 answers contain colons, so neither matches.
var resolvedAddressRE = regexp.MustCompile(`(?m)^Address:\s+(\d+\.\d+\.\d+\.\d+)\s*$`)

// resolveFromPod runs nslookup for name from inside podName and returns the
// first resolved IPv4 address, or "" if none was found (including on any
// command error — a failed lookup and a lookup with unparseable output both
// mean "resolution did not demonstrably work", which is the only thing the
// caller needs to know).
func resolveFromPod(cfg *envconf.Config, ns, podName, name string) (string, string) {
	cmd := fmt.Sprintf(
		"kubectl --kubeconfig %s -n %s debug pod/%s --image=busybox:1.36 -c probe-dns-resolve --attach --quiet -- "+
			"timeout 8 nslookup %s",
		cfg.KubeconfigFile(), ns, podName, name,
	)
	p := utils.RunCommand(cmd)
	out := strings.TrimSpace(p.Result())
	m := resolvedAddressRE.FindStringSubmatch(out)
	if len(m) < 2 {
		return "", out
	}
	return m[1], out
}

// simulatorPodNameAndIP finds the (single-replica) simulator Pod for release
// and returns its name and Pod IP, read through the typed client rather than
// `kubectl -o jsonpath` — main_test.go's assertPodCIDRDoesNotOverlapNodes
// already hit the quoting bug that comes from round-tripping a jsonpath
// expression through a shell command string once; no reason to hit it twice.
func simulatorPodNameAndIP(t *testing.T, ctx context.Context, cfg *envconf.Config, ns, release string) (string, string) {
	t.Helper()
	var pods corev1.PodList
	sel := fmt.Sprintf("app.kubernetes.io/instance=%s,app.kubernetes.io/component=simulator", release)
	if err := cfg.Client().Resources(ns).List(ctx, &pods, resources.WithLabelSelector(sel)); err != nil {
		t.Fatalf("listing simulator pods in %s: %v", ns, err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("expected exactly one simulator pod in %s (selector %q), found %d", ns, sel, len(pods.Items))
	}
	p := pods.Items[0]
	if p.Status.PodIP == "" {
		t.Fatalf("simulator pod %s/%s has no PodIP yet — it should already be Ready at this point", ns, p.Name)
	}
	return p.Name, p.Status.PodIP
}

// nodeNameOfPod returns the node a pod is scheduled on.
func nodeNameOfPod(t *testing.T, ctx context.Context, cfg *envconf.Config, ns, podName string) string {
	t.Helper()
	var p corev1.Pod
	if err := cfg.Client().Resources(ns).Get(ctx, podName, ns, &p); err != nil {
		t.Fatalf("reading pod %s/%s: %v", ns, podName, err)
	}
	if p.Spec.NodeName == "" {
		t.Fatalf("pod %s/%s has no NodeName set — it should already be scheduled and Ready at this point", ns, podName)
	}
	return p.Spec.NodeName
}

// nodeInternalIP reads a node's InternalIP through the typed client, same
// reasoning as simulatorPodNameAndIP.
func nodeInternalIP(t *testing.T, ctx context.Context, cfg *envconf.Config, nodeName string) string {
	t.Helper()
	var n corev1.Node
	if err := cfg.Client().Resources().Get(ctx, nodeName, "", &n); err != nil {
		t.Fatalf("reading node %s: %v", nodeName, err)
	}
	for _, addr := range n.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			return addr.Address
		}
	}
	t.Fatalf("node %s has no InternalIP in its status", nodeName)
	return ""
}

// kubernetesServiceClusterIP reads the API server's own ClusterIP from the
// well-known `kubernetes` Service in the `default` namespace, which every
// cluster has.
func kubernetesServiceClusterIP(t *testing.T, ctx context.Context, cfg *envconf.Config) string {
	t.Helper()
	var svc corev1.Service
	if err := cfg.Client().Resources("default").Get(ctx, "kubernetes", "default", &svc); err != nil {
		t.Fatalf("reading the kubernetes Service in default: %v", err)
	}
	if svc.Spec.ClusterIP == "" {
		t.Fatalf("the kubernetes Service in default has no ClusterIP")
	}
	return svc.Spec.ClusterIP
}

// shepherdServiceFQDN is Shepherd's own Service DNS name from inside the
// cluster. Fully qualified rather than the short in-namespace form: the
// simulator and Shepherd share a namespace in this suite's fixtures, but
// spelling it out means this keeps meaning the same thing if that ever
// changes.
func shepherdServiceFQDN(ns, release string) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local", release, ns)
}
