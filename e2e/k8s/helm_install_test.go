//go:build e2ek8s

package k8s_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/utils"
)

// TestHelmChartInstalls covers what `make helm-lint` structurally cannot.
//
// helm-lint runs `helm lint` and `helm template`. Neither applies anything, so
// a chart that renders perfectly and fails on apply — a bad probe, an
// unschedulable resource request, a missing RBAC verb, a Secret key the
// Deployment references but nothing creates — ships undetected. This installs
// it into a real cluster with the simulator enabled and waits for it to work.
//
// It is also the prerequisite for the containment probes: those need a real
// simulator Pod with the chart's real NetworkPolicy attached to it.
func TestHelmChartInstalls(t *testing.T) {
	feat := features.New("helm chart installs and runs").
		WithLabel("suite", "chart").
		Setup(deployPostgres).
		Setup(loadImages).
		Setup(createChartSecret).
		Assess("helm install succeeds with the simulator enabled",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				p := utils.RunCommand(fmt.Sprintf(
					"helm install %s %s --kubeconfig %s --namespace %s "+
						"--set image.repository=shepherd --set image.tag=local "+
						"--set image.pullPolicy=Never "+
						"--set simulator.enabled=true "+
						"--set simulator.image.repository=shepherd-simulator "+
						"--set simulator.image.tag=local "+
						"--set simulator.image.pullPolicy=Never "+
						"--set replicas=1 "+
						"--set existingSecret=%s "+
						"--wait --timeout 5m",
					releaseName, chartPath, cfg.KubeconfigFile(), cfg.Namespace(), chartSecretName,
				))
				if p.Err() != nil {
					// helm --wait failing is the interesting case; surface the
					// cluster's own view rather than just helm's exit code.
					t.Fatalf("helm install failed: %v\n%s\n%s", p.Err(), p.Result(), describeNamespace(cfg))
				}
				return ctx
			}).
		Assess("migrations were actually applied to the database",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// NOT "the Job still exists": it is a hook with
				// hook-delete-policy: hook-succeeded, so Helm removes it on
				// success and listing Jobs finds nothing. Asserting on the Job
				// would fail for the one reason that means everything worked.
				//
				// Nor is "helm install succeeded" enough on its own — that says
				// the hook exited 0, not that the schema moved. Ask the database.
				out := psql(cfg, "select count(*) from schema_migrations")
				n, err := strconv.Atoi(strings.TrimSpace(out))
				if err != nil || n < 1 {
					t.Fatalf("expected at least one applied migration, got %q (err %v)\n%s",
						out, err, describeNamespace(cfg))
				}
				t.Logf("schema_migrations rows: %d", n)
				return ctx
			}).
		Assess("the shepherd Deployment becomes Available",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				waitDeploymentAvailable(ctx, t, cfg, releaseName)
				return ctx
			}).
		Assess("the simulator Deployment becomes Available",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// If this is the thing that fails, containment is untestable —
				// so it is worth its own assertion rather than being folded in.
				waitDeploymentAvailable(ctx, t, cfg, releaseName+"-simulator")
				return ctx
			}).
		Assess("shepherd answers /healthz through its Service",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// Through the Service, not the Pod IP: that exercises the
				// selector, which is a thing charts get wrong and templating
				// cannot catch.
				ok, out := dialUntil(ctx, cfg, "chart-health", releaseName, 8080, true, connectDeadline)
				if !ok {
					t.Fatalf("shepherd Service never accepted a connection in %s: %s", connectDeadline, out)
				}
				return ctx
			}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// Left installed if the run is being kept for debugging.
			if keepCluster() {
				return ctx
			}
			_ = utils.RunCommand(fmt.Sprintf("helm uninstall %s --kubeconfig %s --namespace %s",
				releaseName, cfg.KubeconfigFile(), cfg.Namespace()))
			return ctx
		}).
		Feature()

	testenv.Test(t, feat)
}

const (
	releaseName     = "shepherd"
	chartPath       = "../../deploy/helm/shepherd"
	chartSecretName = "shepherd-e2e-secrets"
	pgName          = "postgres"
)

// psql runs a query inside the fixture Postgres pod and returns stdout.
func psql(cfg *envconf.Config, query string) string {
	p := utils.RunCommand(fmt.Sprintf(
		"kubectl --kubeconfig %s -n %s exec %s -- psql -U shepherd -d shepherd -tAc %s",
		cfg.KubeconfigFile(), cfg.Namespace(), pgName, strconv.Quote(query),
	))
	return p.Result()
}

// describeNamespace gathers enough to diagnose a failure without a live
// cluster. A pod list alone is not enough: a Job that never creates a pod shows
// as 0/1 with nothing to look at, and the reason lives in its events.
func describeNamespace(cfg *envconf.Config) string {
	var out string
	for _, cmd := range []string{
		"get pods,jobs,deploy,svc,secrets -o wide",
		"describe job shepherd-migrate",
		"get events --sort-by=.lastTimestamp",
		// The logs are the point once a pod actually starts: everything above
		// explains why a pod does not exist, and nothing explains why one exited.
		"logs --selector=job-name=shepherd-migrate --tail=40 --prefix",
		"logs --selector=app.kubernetes.io/name=shepherd --tail=40 --prefix",
	} {
		p := utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s -n %s %s",
			cfg.KubeconfigFile(), cfg.Namespace(), cmd))
		out += fmt.Sprintf("\n--- kubectl %s ---\n%s\n", cmd, p.Result())
	}
	return out
}

func waitDeploymentAvailable(ctx context.Context, t *testing.T, cfg *envconf.Config, name string) {
	t.Helper()
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cfg.Namespace()}}
	if err := wait.For(
		conditions.New(cfg.Client().Resources()).DeploymentAvailable(name, cfg.Namespace()),
		wait.WithTimeout(5*time.Minute),
		wait.WithInterval(5*time.Second),
	); err != nil {
		diag := utils.RunCommand(fmt.Sprintf(
			"kubectl --kubeconfig %s -n %s describe deployment %s",
			cfg.KubeconfigFile(), cfg.Namespace(), name))
		t.Fatalf("deployment %q never became Available: %v\n%s", dep.Name, err, diag.Result())
	}
}

// deployPostgres stands up the database the chart needs. Deliberately a bare
// Pod and Service rather than a chart dependency: this is a test fixture, not
// a recommendation, and it must not drift into looking like one.
func deployPostgres(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
	ns := cfg.Namespace()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: pgName, Namespace: ns, Labels: map[string]string{"app": pgName}},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "postgres",
				Image: "postgres:16-alpine",
				Env: []corev1.EnvVar{
					{Name: "POSTGRES_PASSWORD", Value: "e2e"},
					{Name: "POSTGRES_USER", Value: "shepherd"},
					{Name: "POSTGRES_DB", Value: "shepherd"},
				},
				Ports: []corev1.ContainerPort{{ContainerPort: 5432}},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						Exec: &corev1.ExecAction{Command: []string{"pg_isready", "-U", "shepherd"}},
					},
					InitialDelaySeconds: 3,
					PeriodSeconds:       3,
				},
			}},
		},
	}
	if err := cfg.Client().Resources().Create(ctx, pod); err != nil {
		t.Fatalf("creating postgres pod: %v", err)
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: pgName, Namespace: ns},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": pgName},
			Ports: []corev1.ServicePort{{
				Port: 5432, TargetPort: intstr.FromInt32(5432), Protocol: corev1.ProtocolTCP,
			}},
		},
	}
	if err := cfg.Client().Resources().Create(ctx, svc); err != nil {
		t.Fatalf("creating postgres service: %v", err)
	}
	if err := wait.For(
		conditions.New(cfg.Client().Resources()).PodReady(pod),
		wait.WithTimeout(3*time.Minute),
		wait.WithInterval(3*time.Second),
	); err != nil {
		t.Fatalf("postgres never became ready: %v", err)
	}
	return ctx
}

// loadImages puts the locally-built images into the kind nodes. The chart is
// installed with pullPolicy=Never so a missing image fails loudly here instead
// of the cluster silently pulling something else from a registry.
func loadImages(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
	for _, img := range []string{"shepherd:local", "shepherd-simulator:local"} {
		if p := utils.RunCommand(fmt.Sprintf("docker image inspect %s", img)); p.Err() != nil {
			t.Fatalf("image %s is not present locally — build it first "+
				"(make docker-build-local docker-build-simulator): %v", img, p.Err())
		}
		if p := utils.RunCommand(fmt.Sprintf("kind load docker-image %s --name %s", img, clusterName)); p.Err() != nil {
			t.Fatalf("loading %s into kind: %v: %s", img, p.Err(), p.Result())
		}
	}
	return ctx
}

// createChartSecret supplies the values the chart requires but does not invent:
// a database URL and an encryption key.
func createChartSecret(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: chartSecretName, Namespace: cfg.Namespace()},
		StringData: map[string]string{
			"SHEPHERD_DATABASE_URL": fmt.Sprintf(
				"postgres://shepherd:e2e@%s.%s.svc.cluster.local:5432/shepherd?sslmode=disable",
				pgName, cfg.Namespace()),
			// Decodes to exactly "e2e-test-key-not-a-real-secret!!" — 32 bytes,
			// which the config validator requires. An obviously-fake fixture
			// that must never look like something to copy into a deployment.
			// (The first attempt here decoded to 29 bytes and the migrate Job
			// died with "must be a base64-encoded 32-byte value".)
			"SHEPHERD_SECURITY_ENCRYPTION_KEY": "ZTJlLXRlc3Qta2V5LW5vdC1hLXJlYWwtc2VjcmV0ISE=",
		},
	}
	if err := cfg.Client().Resources().Create(ctx, sec); err != nil {
		t.Fatalf("creating chart secret: %v", err)
	}
	return ctx
}
