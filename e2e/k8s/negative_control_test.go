//go:build e2ek8s

package k8s_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/utils"
)

// TestCNIEnforcesNetworkPolicy is the gate every containment result in this
// suite depends on, and it must run before any of them.
//
// A denied connection proves nothing on its own. If the CNI does not implement
// NetworkPolicy, every deny-assertion in the suite passes for the wrong reason
// and reports containment that does not exist — silent by construction, exactly
// like the compose `internal: true` assertions that asserted a YAML key, and
// the self-skipping specs before them.
//
// So this proves the enforcer before anything trusts a denial:
//
//	Phase 1  prober -> target with NO policy   MUST CONNECT
//	         (proves the prober, image, DNS and dial method all work; without
//	         this a later "denied" could just mean the prober was broken)
//	Phase 2  apply default-deny INGRESS, dial  MUST FAIL
//	         (proves the CNI enforces the Ingress policy type)
//	Phase 3  swap it for default-deny EGRESS,  MUST FAIL
//	         dial again
//	         (proves the Egress policy type separately — it is the direction
//	         every simulator containment denial actually depends on, and a CNI
//	         could implement one without the other)
//
// If phase 2 still connects, this FAILS — it does not skip. A skip here would
// be indistinguishable from success in CI, which is the precise failure mode
// this repo has already removed once.
//
// Phase 2 is also the executable form of the warning in the deployment docs:
// it demonstrates, in the repo, exactly what an operator loses on a
// non-enforcing CNI such as Flannel.
func TestCNIEnforcesNetworkPolicy(t *testing.T) {
	const (
		targetName = "np-target"
		proberName = "np-prober"
		targetPort = 80
	)

	feat := features.New("CNI enforces NetworkPolicy").
		WithLabel("suite", "containment").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// Its own namespace, like every other feature: the default-deny
			// policy below is namespace-scoped, and applying it to a shared
			// namespace would cut off whatever else happened to be running.
			cniFixture = newFixture(ctx, t, cfg, "cni")
			ns := cniFixture.ns
			client := cfg.Client()

			target := servePod(targetName, ns)
			if err := client.Resources().Create(ctx, target); err != nil {
				t.Fatalf("creating target pod: %v", err)
			}
			if err := wait.For(
				conditions.New(client.Resources()).PodReady(target),
				wait.WithTimeout(3*time.Minute),
				wait.WithInterval(2*time.Second),
			); err != nil {
				t.Fatalf("target pod never became ready: %v", err)
			}

			svc := targetService(targetName, ns, targetPort)
			if err := client.Resources().Create(ctx, svc); err != nil {
				t.Fatalf("creating target service: %v", err)
			}
			return ctx
		}).
		Assess("phase 1: the target IS reachable with no policy in place",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// The positive control. If this never succeeds the harness is
				// broken and every denial below would be meaningless.
				//
				// Polled rather than dialled once: a Pod being Ready does not mean
				// its Service endpoints are programmed yet, and a single early dial
				// fails for a reason that has nothing to do with policy. Observed
				// exactly that flake while building this.
				ok, out := dialUntilIn(ctx, cfg, cniFixture.ns, proberName+"-open", targetName, targetPort, true, connectDeadline)
				if !ok {
					t.Fatalf("the probe never reached the target in %s, BEFORE any policy was applied — "+
						"the harness itself is broken, so no denial in this suite would mean anything.\n"+
						"last probe output: %s", connectDeadline, out)
				}
				return ctx
			}).
		Assess("phase 2: a default-deny policy makes it unreachable",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				if err := cfg.Client().Resources().Create(ctx, denyAllIngress(cniFixture.ns)); err != nil {
					t.Fatalf("creating default-deny policy: %v", err)
				}
				// Polled rather than slept: policy programming is not instantaneous,
				// and a fixed sleep either wastes time or races the CNI into a false
				// "not enforcing" verdict.
				denied, out := dialUntilIn(ctx, cfg, cniFixture.ns, proberName+"-denied", targetName, targetPort, false, denyDeadline)
				if !denied {
					t.Fatalf("THE CNI DOES NOT ENFORCE NETWORKPOLICY.\n\n"+
						"A default-deny NetworkPolicy is in place and the target is still reachable, so "+
						"every containment assertion in this suite would pass for the wrong reason.\n\n"+
						"This is exactly the exposure the deployment docs warn about: on a non-enforcing "+
						"CNI (Flannel, or AWS VPC CNI without its policy agent) the simulator's policy "+
						"applies successfully and silently does nothing, leaving a sandbox that executes "+
						"user-authored config able to reach any pod, Service or node it can route to.\n\n"+
						"The probe still connected after %s.\nlast probe output: %s", denyDeadline, out)
				}
				return ctx
			}).
		Assess("phase 3: a default-deny EGRESS policy also makes it unreachable",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// Egress is the policy type every simulator containment denial
				// actually rides on (the chart's policy is default-deny egress
				// with narrow allows), and a CNI could enforce Ingress but not
				// Egress. Swap the policies so the only possible deny source is
				// the Egress one: with ingress-deny gone, a still-denied dial
				// can only mean the prober's own egress was policed. (The deny
				// may land at DNS resolution rather than the connect — both are
				// egress from the prober, and either proves the policy type is
				// enforced, which is all this gate certifies.)
				if err := cfg.Client().Resources().Delete(ctx, denyAllIngress(cniFixture.ns)); err != nil {
					t.Fatalf("deleting the phase-2 ingress policy: %v", err)
				}
				if err := cfg.Client().Resources().Create(ctx, denyAllEgress(cniFixture.ns)); err != nil {
					t.Fatalf("creating default-deny egress policy: %v", err)
				}
				denied, out := dialUntilIn(ctx, cfg, cniFixture.ns, proberName+"-egress", targetName, targetPort, false, denyDeadline)
				if !denied {
					t.Fatalf("THE CNI ENFORCES INGRESS BUT NOT EGRESS NETWORKPOLICY.\n\n"+
						"A default-deny egress policy is in place (and the ingress one removed) yet the "+
						"prober still connected — the exact policy type the simulator's containment relies "+
						"on is not enforced, so every egress denial this suite would report downstream "+
						"would pass for the wrong reason.\n\n"+
						"The probe still connected after %s.\nlast probe output: %s", denyDeadline, out)
				}
				cniVerified = true
				return ctx
			}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			cniFixture.cleanup(cfg)
			return ctx
		}).
		Feature()

	testenv.Test(t, feat)
}

// cniFixture is this feature's namespace, shared between its Setup and Assess
// closures.
var cniFixture *fixture

// cniVerified is set true only once all three phases of
// TestCNIEnforcesNetworkPolicy have passed: connect-with-no-policy succeeded,
// connect-under-ingress-deny failed, and connect-under-egress-deny failed.
// Every containment probe in simulator_containment_test.go checks this before
// doing anything else — a denial observed against an unverified CNI is exactly
// the meaningless result §4 of the plan warns about, one level up from what
// P-harness guards inside a single feature. Deliberately package-level rather
// than threaded through Context: TestMain runs test functions from this
// package sequentially in one process (no t.Parallel anywhere in this suite),
// and the filename (negative_control_test.go sorts before
// simulator_containment_test.go) is what makes `go test` run this feature
// first. That ordering is source-order within the package, which
// `go test -shuffle=on` randomizes — under shuffle the containment features
// fail loudly on this gate rather than silently probing an unverified CNI,
// which is the acceptable direction; do not wire -shuffle into this suite's
// invocation without replacing the ordering with an explicit TestMain sequence.
var cniVerified bool

const (
	// connectDeadline bounds how long the positive control waits for Service
	// endpoints to be programmed.
	connectDeadline = 90 * time.Second
	// denyDeadline bounds how long we wait for the CNI to program a policy
	// before concluding it does not enforce at all.
	denyDeadline = 90 * time.Second
)

// dialUntil retries dial until it reaches `want` (true = must connect, false =
// must be refused) or the deadline expires. Returns whether it got there, plus
// the last probe output for the failure message.
//
// Each attempt uses a fresh pod name: `kubectl run --rm` leaves nothing behind
// on success, but a failed attach can, and a name collision would then be
// reported as the probe result rather than as the collision it is.
// dialUntilIn retries dialIn until it reaches `want` (true = must connect,
// false = must be refused) or the deadline expires. Returns whether it got
// there, plus the last probe output for the failure message.
//
// The namespace is an explicit parameter, never cfg.Namespace(): features own
// their own namespaces (fixtures_test.go), and there is no environment-level
// namespace to fall back to.
//
// Polled rather than dialled once: a Pod being Ready does not mean its Service
// endpoints are programmed, and policy programming is not instantaneous. A
// single dial fails — or passes — for reasons that have nothing to do with what
// is being tested.
func dialUntilIn(ctx context.Context, cfg *envconf.Config, ns, base, host string, port int, want bool, deadline time.Duration) (bool, string) {
	end := time.Now().Add(deadline)
	var last string
	for i := 0; time.Now().Before(end); i++ {
		out, err := dialIn(ctx, cfg, ns, fmt.Sprintf("%s-%d", base, i), host, port)
		last = out
		if (err == nil) == want {
			return true, last
		}
		time.Sleep(5 * time.Second)
	}
	return false, last
}

// dial runs a one-shot pod that tries to open a TCP connection to host:port and
// exits non-zero if it cannot. It returns the pod's output so a failure can be
// read rather than guessed at.
//
// A TCP connect, not an HTTP request: NetworkPolicy operates at L3/L4, and an
// HTTP-level failure could mean an application error rather than a blocked
// connection. `nc -z` with a timeout distinguishes them.
func dialIn(ctx context.Context, cfg *envconf.Config, ns, podName, host string, port int) (string, error) {
	cmd := fmt.Sprintf(
		"kubectl --kubeconfig %s -n %s run %s "+
			"--image=busybox:1.36 --restart=Never --rm --attach --quiet --pod-running-timeout=2m "+
			"--command -- timeout 8 nc -z -w 5 %s %d",
		cfg.KubeconfigFile(), ns, podName, host, port,
	)
	p := utils.RunCommand(cmd)
	return strings.TrimSpace(p.Result()), p.Err()
}

// servePod is a minimal always-listening TCP target.
func servePod(name, ns string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"app": name}},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "serve",
				Image: "busybox:1.36",
				// A trivial listener that survives repeated probes: each accepted
				// connection is answered and the loop continues.
				Command: []string{"sh", "-c", "while true; do echo ok | nc -l -p 80; done"},
				Ports:   []corev1.ContainerPort{{ContainerPort: 80}},
			}},
		},
	}
}

func targetService(name, ns string, port int) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			Ports: []corev1.ServicePort{{
				Port:       int32(port),
				TargetPort: intstr.FromInt32(int32(port)),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}

// denyAllIngress is the canonical default-deny: an empty podSelector selects
// every pod in the namespace, and an Ingress policyType with no rules permits
// nothing.
func denyAllIngress(ns string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "default-deny-ingress", Namespace: ns},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}
}

// denyAllEgress is phase 3's counterpart: an Egress policyType with no rules
// denies every outbound connection from every pod in the namespace.
func denyAllEgress(ns string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "default-deny-egress", Namespace: ns},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
		},
	}
}
