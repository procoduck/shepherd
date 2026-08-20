//go:build e2ek8s

// Package k8s_test is the Kubernetes end-to-end suite: the things
// docker-compose structurally cannot verify.
//
// Kubernetes is the production target; compose is a local-development
// convenience. Three consequences follow, and this suite exists for them:
//
//   - S3 sandbox containment is a NetworkPolicy, and a NetworkPolicy is only
//     enforced if the CNI implements it. deploy/helm/chart_test.go greps
//     `helm template` output and asserts the YAML RENDERS — the same
//     asserts-the-declaration-not-the-effect pattern this repo condemned for
//     compose. Nothing has ever dialled out of a simulator pod.
//   - `make helm-lint` lints and templates the chart but never installs it, so
//     a manifest that renders and fails on apply ships undetected.
//   - Nothing exercises Shepherd against real telemetry backends, so "the
//     pipeline is correct" has never meant "the data arrived".
//
// Deliberately a separate build tag (e2ek8s) from the compose suite's `e2e`:
// the two must never run in one invocation by accident, and this one costs
// minutes before its first assertion.
//
// Plan and rationale: docs/kind-test-environment-plan.md
package k8s_test

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/envfuncs"
	"sigs.k8s.io/e2e-framework/pkg/utils"
	"sigs.k8s.io/e2e-framework/support/kind"
)

var (
	testenv env.Environment

	// clusterName is randomised so a leftover cluster from a killed run, or a
	// second concurrent run, can never collide with this one. `make
	// e2e-k8s-clean` removes any shepherd-e2e-* cluster.
	clusterName string
)

const (
	// calicoManifest is applied after cluster creation because kind is
	// configured with disableDefaultCNI. Pinned rather than "latest": a CNI
	// upgrade changing enforcement behaviour must be a deliberate commit, not
	// something that arrives silently on a Tuesday.
	calicoManifest = "https://raw.githubusercontent.com/projectcalico/calico/v3.28.2/manifests/calico.yaml"

	// cniReadyTimeout is generous: on a cold machine Calico pulls several
	// images before a single node reports Ready.
	cniReadyTimeout = 5 * time.Minute
)

func TestMain(m *testing.M) {
	testenv = env.New()
	clusterName = envconf.RandomName("shepherd-e2e", 20)

	cfgPath, err := filepath.Abs(filepath.Join("testdata", "kind-cluster.yaml"))
	if err != nil {
		log.Fatalf("resolving kind config: %v", err)
	}
	cluster := kind.NewCluster(clusterName).WithOpts(kind.WithImage(kindNodeImage()))

	testenv.Setup(
		envfuncs.CreateClusterWithConfig(cluster, clusterName, cfgPath),
		assertPodCIDRDoesNotOverlapNodes,
		installCNI,
		waitForNodesReady,
		waitForClusterDNS,
		// Cluster-wide and created once: `kind load` is a node-level operation,
		// and one Postgres serves every feature (each gets its own DATABASE
		// inside it — see fixtures_test.go for the isolation rules).
		loadImages,
		deploySharedPostgres,
	)

	// Finish runs on success, on failure and on panic. The one case it cannot
	// cover is SIGKILL, which is what `make e2e-k8s-clean` is for.
	testenv.Finish(
		exportDiagnosticsOnFailure,
		destroyUnlessKept(clusterName),
	)

	// Not `os.Exit(testenv.Run(m))`: Finish does NOT run when Setup fails, and a
	// Setup failure is exactly when a cluster is most likely to be left behind —
	// observed here, twice, while getting the CNI right. This sweep is
	// idempotent with the DestroyCluster in Finish (deleting an already-deleted
	// cluster is a no-op), so the normal path is unaffected.
	code := testenv.Run(m)
	sweepCluster(clusterName)
	os.Exit(code)
}

// keepCluster reports whether the caller asked to leave the cluster running.
func keepCluster() bool { return os.Getenv("E2E_K8S_KEEP") == "1" }

// sweepCluster is the last-resort teardown for paths Finish never reaches.
func sweepCluster(name string) {
	if keepCluster() {
		return
	}
	if p := utils.RunCommand(fmt.Sprintf("kind delete cluster --name %s", name)); p.Err() != nil {
		// Already gone on the happy path — only worth a line when it is not.
		log.Printf("cluster sweep for %q: %v (already deleted is expected)", name, p.Err())
	}
}

// kindNodeImage lets CI pin the Kubernetes version without editing the config.
func kindNodeImage() string {
	if v := os.Getenv("E2E_K8S_NODE_IMAGE"); v != "" {
		return v
	}
	return "kindest/node:v1.31.4"
}

// podCIDR must match testdata/kind-cluster.yaml's networking.podSubnet.
const podCIDR = "10.244.0.0/16"

// assertPodCIDRDoesNotOverlapNodes fails fast when the configured pod CIDR
// contains the node IPs.
//
// This is not hypothetical. Calico's kind guidance says to use 192.168.0.0/16,
// which is fine on Docker's usual 172.17.x bridge and wrong on OrbStack, where
// the kind network came up as 192.168.117.0/24 — inside the pool. Nothing
// reported an error: calico-kube-controllers simply crash-looped and CoreDNS
// never went Ready, surfacing five minutes later as a DNS timeout in an
// unrelated spec. Checking it here turns that into one clear sentence.
func assertPodCIDRDoesNotOverlapNodes(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
	_, pool, err := net.ParseCIDR(podCIDR)
	if err != nil {
		return ctx, fmt.Errorf("parsing pod CIDR %q: %w", podCIDR, err)
	}
	// The typed client rather than kubectl -o jsonpath: the jsonpath for an
	// InternalIP contains quotes, and round-tripping those through a shell
	// command string is a quoting bug waiting to happen (it was one).
	var nodes corev1.NodeList
	if err := cfg.Client().Resources().List(ctx, &nodes); err != nil {
		return ctx, fmt.Errorf("listing nodes: %w", err)
	}
	for _, n := range nodes.Items {
		for _, addr := range n.Status.Addresses {
			if addr.Type != corev1.NodeInternalIP {
				continue
			}
			if ip := net.ParseIP(addr.Address); ip != nil && pool.Contains(ip) {
				return ctx, fmt.Errorf(
					"pod CIDR %s contains node IP %s — the container runtime's network overlaps the pod "+
						"network, which manifests as calico-kube-controllers crash-looping and CoreDNS never "+
						"becoming Ready, NOT as an error. Pick a pod subnet outside your Docker/OrbStack "+
						"network in e2e/k8s/testdata/kind-cluster.yaml (and update podCIDR here to match)",
					podCIDR, ip)
			}
		}
	}
	return ctx, nil
}

// installCNI applies Calico. kind was told not to install kindnetd, so until
// this succeeds no pod can schedule — which is why a failure here fails the
// whole run loudly rather than leaving specs to time out mysteriously.
func installCNI(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
	log.Printf("installing Calico (%s)", calicoManifest)
	if p := utils.RunCommand(
		fmt.Sprintf("kubectl --kubeconfig %s apply -f %s", cfg.KubeconfigFile(), calicoManifest),
	); p.Err() != nil {
		return ctx, fmt.Errorf("applying Calico: %w: %s", p.Err(), p.Result())
	}
	return ctx, nil
}

// waitForNodesReady blocks until every node is Ready, i.e. until the CNI is
// actually serving. Without this the first spec races the CNI rollout and fails
// for a reason that has nothing to do with what it is testing.
func waitForNodesReady(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
	log.Printf("waiting up to %s for nodes to become Ready", cniReadyTimeout)
	if p := utils.RunCommand(fmt.Sprintf(
		"kubectl --kubeconfig %s wait --for=condition=Ready nodes --all --timeout=%ds",
		cfg.KubeconfigFile(), int(cniReadyTimeout.Seconds()),
	)); p.Err() != nil {
		return ctx, fmt.Errorf("nodes did not become Ready — CNI rollout failed: %w: %s", p.Err(), p.Result())
	}
	return ctx, nil
}

// waitForClusterDNS blocks until CoreDNS is Available.
//
// Nodes report Ready as soon as the CNI is serving, but CoreDNS is an ordinary
// Deployment that must then schedule and start — so a spec resolving a Service
// name immediately after "nodes Ready" can fail with `nc: bad address`, which
// reads like a policy denial and is not one. Observed exactly that while
// building this suite: the first run passed only because image pulls happened
// to give CoreDNS enough time.
func waitForClusterDNS(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
	log.Printf("waiting for CoreDNS to become Available")
	if p := utils.RunCommand(fmt.Sprintf(
		"kubectl --kubeconfig %s -n kube-system wait --for=condition=Available deployment/coredns --timeout=%ds",
		cfg.KubeconfigFile(), int(cniReadyTimeout.Seconds()),
	)); p.Err() != nil {
		return ctx, fmt.Errorf("CoreDNS never became Available — Service DNS would fail: %w: %s", p.Err(), p.Result())
	}
	return ctx, nil
}

// exportDiagnosticsOnFailure dumps cluster state when the run failed, because
// the very next thing Finish does is delete the cluster that holds the
// evidence. Best-effort: a diagnostics failure must never mask the real one.
func exportDiagnosticsOnFailure(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
	if !testing.Testing() {
		return ctx, nil
	}
	dir := os.Getenv("E2E_K8S_ARTIFACTS")
	if dir == "" {
		return ctx, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("diagnostics: could not create %s: %v", dir, err)
		return ctx, nil
	}
	log.Printf("exporting cluster diagnostics to %s", dir)
	_ = utils.RunCommand(fmt.Sprintf("kind export logs %s --name %s", dir, clusterName))
	return ctx, nil
}

// destroyUnlessKept tears the cluster down unless E2E_K8S_KEEP=1, matching the
// compose suite's E2E_KEEP convention for debugging a failure in place.
func destroyUnlessKept(name string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		if keepCluster() {
			log.Printf("E2E_K8S_KEEP=1 — leaving cluster %q up; delete it with: kind delete cluster --name %s", name, name)
			return ctx, nil
		}
		return envfuncs.DestroyCluster(name)(ctx, cfg)
	}
}
